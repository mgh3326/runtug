package runtug

import (
	"cmp"
	"context"
	"slices"
	"strings"
)

const (
	outcomeAdmitted          = "ADMITTED"
	outcomeQuotaExhausted    = "QUOTA_EXHAUSTED"
	outcomeQuotaUnknown      = "QUOTA_UNKNOWN"
	outcomeAuthUnavailable   = "AUTH_UNAVAILABLE"
	outcomeNoEligibleHost    = "NO_ELIGIBLE_HOST"
	outcomePolicyUnavailable = "POLICY_UNAVAILABLE"
	outcomeNoRoleCandidate   = "NO_ROLE_CANDIDATE"
)

type accountBinding struct {
	key   string
	hosts []selHost
}

func (sp *selPresence) hostEligible(h selHost, seat *Seat) bool {
	return h.eligible && !h.excluded && (h.awake || h.wakeable) && (seat.Capability == "standard" || boolean(h.cfg, seat.Capability, false))
}

func (sp *selPresence) hasHost(seat *Seat) bool {
	return slices.ContainsFunc(sp.hosts, func(h selHost) bool { return sp.hostEligible(h, seat) })
}

// bindings groups current logins by account, so two hosts never supply two
// copies of the same quota. Hosts only compete inside their account.
func (sp *selPresence) bindings(seat *Seat, pool string) []accountBinding {
	var bindings []accountBinding
	if sp.presErr {
		return bindings
	}
	for _, h := range sp.hosts {
		if !sp.hostEligible(h, seat) {
			continue
		}
		key := hostAccount(sp.presence, pool, h.presence, sp.now, sp.presTTL)
		if key == "" {
			continue
		}
		i := slices.IndexFunc(bindings, func(b accountBinding) bool { return b.key == key })
		if i < 0 {
			bindings = append(bindings, accountBinding{key: key})
			i = len(bindings) - 1
		}
		bindings[i].hosts = append(bindings[i].hosts, h)
	}
	for _, b := range bindings {
		byScore := !slices.ContainsFunc(b.hosts, func(h selHost) bool { return h.score == nil })
		slices.SortStableFunc(b.hosts, func(x, y selHost) int {
			if x.awake != y.awake {
				if x.awake {
					return -1
				}
				return 1
			}
			if byScore {
				return cmp.Compare(*y.score, *x.score)
			}
			return cmp.Compare(loadOrInf(x.load), loadOrInf(y.load))
		})
	}
	return bindings
}

// accountRefresh is one bounded refresh operation. A transport failure can
// move it to another holder; a completed measurement ends the operation.
type accountRefresh struct {
	measured string
	host     string
	err      error
}

// account caches both the reading and the refresh cause per account, so a
// second profile cannot repeat a measurement or disguise an infrastructure
// failure as missing quota evidence.
func (sp *selPresence) account(ctx context.Context, key, pool string, hosts []selHost, force bool) SharedAccount {
	acc, read := sp.accounts[key]
	if !read {
		var err error
		acc, err = sp.a.Source.SharedAccount(ctx, key)
		if err != nil {
			acc = SharedAccount{Key: key, Degraded: true}
		}
		sp.accounts[key] = acc
	}
	if _, refreshed := sp.refreshed[key]; refreshed || !force && sharedReason(key, acc) == "" && acc.Quota != nil {
		return acc
	}
	ordered := slices.Clone(hosts)
	for _, local := range sp.hosts {
		if local.local && hostAccount(sp.presence, pool, local.presence, sp.now, sp.presTTL) == key {
			ordered = slices.DeleteFunc(ordered, func(h selHost) bool { return h.name == local.name })
			ordered = append([]selHost{local}, ordered...)
			break
		}
	}
	refresh, cancel := context.WithTimeout(ctx, sp.a.placementTTL("measure_timeout_s", 60))
	defer cancel()
	result := accountRefresh{measured: "failed"}
	for _, h := range ordered {
		result.host = h.name
		if h.local {
			binary := "pacelever"
			if q, ok := sp.a.Quota.(CLIQuota); ok {
				binary = q.Binary
			}
			result.err = check(sp.a.Runner.Run(refresh, Command{Name: binary, Args: []string{"refresh", pool}}), "account measurement")
		} else {
			result.err = sp.a.measureTarget(refresh, h.name, pool)
		}
		if refresh.Err() != nil {
			result.err = fail(exitTimeout, "account measurement deadline expired")
			break
		}
		if result.err == nil {
			result.measured = "ok"
			var err error
			acc, err = sp.a.Source.SharedAccount(refresh, key)
			if refresh.Err() != nil {
				result.measured, result.err = "failed", fail(exitTimeout, "account measurement deadline expired during observation read")
				acc = SharedAccount{Key: key, Degraded: true}
				sp.refreshed[key], sp.accounts[key] = result, acc
				return acc
			}
			if err != nil {
				acc = SharedAccount{Key: key, Degraded: true}
			}
			sp.refreshed[key], sp.accounts[key] = result, acc
			return acc
		}
		if !hostGateFailure(result.err) {
			break
		}
	}
	acc = SharedAccount{Key: key, Degraded: true}
	sp.refreshed[key], sp.accounts[key] = result, acc
	return acc
}

func (sp *selPresence) accountFailureCode(key string) string {
	if err := sp.refreshed[key].err; err != nil {
		return gateOutcome(err)
	}
	return outcomeQuotaUnknown
}

func (sp *selPresence) unknownJudgment(b accountBinding, why string) quotaJudgment {
	if why == "" {
		why = "no_account_reading"
	}
	host, status := b.hosts[0], exitEvictionUnconfirmed
	r := sp.refreshed[b.key]
	if r.measured != "" {
		why = "measure_" + why
		if r.measured == "failed" {
			why = "measure_failed"
		}
		for _, h := range b.hosts {
			if h.name == r.host {
				host = h
				break
			}
		}
		if r.err != nil {
			status = exitCode(r.err)
		}
	}
	source := "shared"
	if host.local {
		source = "local"
	}
	return quotaJudgment{Host: host.name, AccountKey: b.key, Source: source, Outcome: "unknown", ExitCode: status, Reason: why, Measured: r.measured}
}

func (p *selPlan) classify(c *selCand, v PoolView) {
	c.blocking, c.projected, c.urgency, c.resetHrs = v.Blocking, v.Projected, v.Urgency, v.ResetHours
	free := p.policy.isFree(c.row)
	c.risk, c.unused = p.policy.wasteRisk(c.row, free, v, true)
	if free {
		c.exhausted = v.Projected != nil && *v.Projected >= *p.policy.ExcludeAt
	} else {
		c.reason = p.policy.exhaustReason(v, c.risk)
		if c.reason == "" && v.Blocking != nil && *v.Blocking >= 100 {
			c.reason = "window_exhausted"
		}
	}
	if c.reason != "" {
		c.code = outcomeQuotaExhausted
	} else {
		c.tierName, c.tier = p.policy.tierOf(c.row, p.order, map[string]PoolView{c.row.Pool: v}, true)
		if c.tier < 0 || !free && v.Blocking == nil && v.Projected == nil {
			c.reason, c.code = "quota_unknown", outcomeQuotaUnknown
		}
	}
	if c.reason != "" {
		c.outcome = "dropped:" + c.reason
	}
}

func gateOutcome(err error) string {
	switch exitCode(err) {
	case 3:
		if strings.Contains(err.Error(), "quota exhausted") {
			return outcomeQuotaExhausted
		}
		return outcomeNoRoleCandidate
	case exitEvictionUnconfirmed:
		return outcomeQuotaUnknown
	case 6:
		return outcomeAuthUnavailable
	case exitSeatRefused:
		return outcomeNoRoleCandidate
	case exitUnreachable, exitTimeout, exitPlacement, exitLocalDisabled:
		return outcomeNoEligibleHost
	default:
		return outcomePolicyUnavailable
	}
}

func hostGateFailure(err error) bool {
	code := gateOutcome(err)
	return code == outcomeNoEligibleHost || code == outcomePolicyUnavailable
}

func (sp *selPresence) admitAccount(ctx context.Context, qr QuotaRequest, c *selCand, p *selPlan) ([]quotaJudgment, error) {
	ordered := slices.Clone(c.hosts)
	// A holder that just completed the refresh is known reachable.
	if host := sp.refreshed[c.accountKey].host; host != "" {
		if i := slices.IndexFunc(ordered, func(h selHost) bool { return h.name == host }); i > 0 {
			ordered[0], ordered[i] = ordered[i], ordered[0]
		}
	}
	// An earlier rung may have refreshed this account after candidates were
	// ranked. Reuse its current reading before admitting another rung.
	acc := sp.accounts[c.accountKey]
	if why := sharedReason(c.accountKey, acc); why != "" || acc.Quota == nil {
		c.code = sp.accountFailureCode(c.accountKey)
		c.reason = strings.ToLower(c.code)
		j := sp.unknownJudgment(accountBinding{key: c.accountKey, hosts: ordered}, why)
		if cause := sp.refreshed[c.accountKey].err; cause != nil {
			return []quotaJudgment{j}, cause
		}
		return []quotaJudgment{j}, fail(exitEvictionUnconfirmed, "account quota remains unknown after refresh")
	}
	p.classify(c, *acc.Quota)
	if c.reason != "" {
		j := quotaJudgment{Host: ordered[0].name, AccountKey: c.accountKey, Source: "shared", Outcome: "unknown", ExitCode: exitEvictionUnconfirmed, Reason: c.reason}
		if ordered[0].local {
			j.Source = "local"
		}
		if c.code == outcomeQuotaExhausted {
			j.Outcome, j.ExitCode = "refused", 3
		}
		return []quotaJudgment{j}, fail(j.ExitCode, "%s: %s", c.code, c.reason)
	}
	var judgments []quotaJudgment
	var last error
	for _, h := range ordered {
		acc := sp.accounts[c.accountKey]
		j, err := sp.a.judgeHost(ctx, qr, c.row.Pool, h.name, h.local, c.accountKey, acc)
		if err != nil && gateOutcome(err) == outcomeQuotaUnknown {
			if _, refreshed := sp.refreshed[c.accountKey]; !refreshed {
				acc = sp.account(ctx, c.accountKey, c.row.Pool, c.hosts, true)
				if cause := sp.refreshed[c.accountKey].err; cause != nil {
					j.Reason, j.Measured, j.ExitCode = "measure_failed", "failed", exitCode(cause)
					return append(judgments, j), cause
				}
				if sharedReason(c.accountKey, acc) == "" && acc.Quota != nil {
					p.classify(c, *acc.Quota)
					if c.reason == "" {
						j, err = sp.a.judgeHost(ctx, qr, c.row.Pool, h.name, h.local, c.accountKey, acc)
					}
				}
			}
		}
		j.Measured = sp.refreshed[c.accountKey].measured
		if err == nil && c.exhausted && !strings.HasPrefix(j.Basis, freeBasisPrefix) {
			c.reason, c.code = "projected_exhaust", outcomeQuotaExhausted
			j.Outcome, j.ExitCode = "refused", exitSeatRefused
			err = fail(exitSeatRefused, "projected_exhaust: gate basis %q is not a free bucket", j.Basis)
		}
		judgments = append(judgments, j)
		if err == nil {
			return judgments, nil
		}
		last = err
		if !hostGateFailure(err) {
			return judgments, err
		}
	}
	return judgments, last
}

func (a *App) selectionRefused(ctx context.Context, s Spawn, req selReq, p *selPlan) (*selPlan, error) {
	for _, code := range []string{outcomeQuotaUnknown, outcomeAuthUnavailable, outcomeNoEligibleHost, outcomePolicyUnavailable, outcomeNoRoleCandidate} {
		if !slices.ContainsFunc(p.cands, func(c *selCand) bool { return c.code == code }) {
			continue
		}
		p.outcome = code
		exit := map[string]int{outcomeQuotaUnknown: exitQuotaUnknown, outcomeAuthUnavailable: exitAuthUnavailable, outcomeNoEligibleHost: exitPlacement, outcomePolicyUnavailable: exitConfig, outcomeNoRoleCandidate: exitSeatRefused}[code]
		return p, fail(exit, "%s: no account admitted routing role %s%s", code, routingRole(req), hostScope(s))
	}
	if slices.ContainsFunc(p.cands, func(c *selCand) bool { return c.code == outcomeQuotaExhausted }) {
		p.outcome = outcomeQuotaExhausted
		if req.route != nil && req.route.rule != nil {
			return a.roleExhausted(ctx, s, req, p)
		}
		return p, fail(exitRoleWait, "%s: every eligible account is measured exhausted%s", outcomeQuotaExhausted, hostScope(s))
	}
	p.outcome, p.routeRefusal = outcomeNoRoleCandidate, reasonRoleNoCandidate
	return p, fail(exitSeatRefused, "%s: %s: no profile passes role, retirement, grade, gate and seat rules", reasonRoleNoCandidate, outcomeNoRoleCandidate)
}

func routingRole(req selReq) string {
	if req.route != nil {
		return req.route.role
	}
	return req.role
}

func hostScope(s Spawn) string {
	if s.Host != "" && s.Host != "auto" {
		return "; --host " + s.Host + " restricts accounts; other hosts' accounts were not considered (not fleet exhaustion)"
	}
	return ""
}
