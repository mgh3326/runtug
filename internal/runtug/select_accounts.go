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

// account refreshes unknown evidence once per account for the entire round,
// including all its profiles and hosts. It never refreshes a different login.
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
	_, refreshed := sp.refreshed[key]
	if !refreshed && (force || sharedReason(key, acc) != "" || acc.Quota == nil) {
		sp.refreshed[key] = "failed"
		h := hosts[0]
		for _, local := range sp.hosts {
			if local.local && hostAccount(sp.presence, pool, local.presence, sp.now, sp.presTTL) == key {
				h = local
				break
			}
		}
		var err error
		if h.local {
			binary := "pacelever"
			if q, ok := sp.a.Quota.(CLIQuota); ok {
				binary = q.Binary
			}
			timeout := sp.a.placementTTL("measure_timeout_s", 60)
			refresh, cancel := context.WithTimeout(ctx, timeout)
			err = check(sp.a.Runner.Run(refresh, Command{Name: binary, Args: []string{"refresh", pool}}), "account measurement")
			if refresh.Err() != nil {
				err = refresh.Err()
			}
			cancel()
		} else {
			err = sp.a.measureTarget(ctx, h.name, pool)
		}
		if err == nil {
			sp.refreshed[key] = "ok"
			acc, err = sp.a.Source.SharedAccount(ctx, key)
			if err != nil {
				acc = SharedAccount{Key: key, Degraded: true}
			}
		} else {
			acc = SharedAccount{Key: key, Degraded: true}
		}
		sp.accounts[key] = acc
	}
	return acc
}

func (sp *selPresence) unknownJudgment(b accountBinding, why string) quotaJudgment {
	if why == "" {
		why = "no_account_reading"
	}
	if measured := sp.refreshed[b.key]; measured != "" {
		why = "measure_" + why
		if measured == "failed" {
			why = "measure_failed"
		}
	}
	return quotaJudgment{Host: b.hosts[0].name, AccountKey: b.key, Source: "shared", Outcome: "unknown", ExitCode: exitEvictionUnconfirmed, Reason: why, Measured: sp.refreshed[b.key]}
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
	case 6:
		return outcomeAuthUnavailable
	case exitSeatRefused:
		return outcomeNoRoleCandidate
	case exitConfig:
		return outcomePolicyUnavailable
	default:
		return outcomeQuotaUnknown
	}
}

func (sp *selPresence) admitAccount(ctx context.Context, qr QuotaRequest, c *selCand, p *selPlan) (quotaJudgment, error) {
	h := c.hosts[0]
	acc := sp.accounts[c.accountKey]
	j, err := sp.a.judgeHost(ctx, qr, c.row.Pool, h.name, false, c.accountKey, acc)
	if err != nil && gateOutcome(err) == outcomeQuotaUnknown {
		if _, refreshed := sp.refreshed[c.accountKey]; !refreshed {
			acc = sp.account(ctx, c.accountKey, c.row.Pool, c.hosts, true)
			if sharedReason(c.accountKey, acc) == "" && acc.Quota != nil {
				p.classify(c, *acc.Quota)
				if c.reason == "" {
					j, err = sp.a.judgeHost(ctx, qr, c.row.Pool, h.name, false, c.accountKey, acc)
				}
			}
		}
	}
	j.Measured = sp.refreshed[c.accountKey]
	if err == nil && c.exhausted && !strings.HasPrefix(j.Basis, freeBasisPrefix) {
		c.reason, c.code = "projected_exhaust", outcomeQuotaExhausted
		j.Outcome, j.ExitCode = "refused", exitSeatRefused
		err = fail(exitSeatRefused, "projected_exhaust: gate basis %q is not a free bucket", j.Basis)
	}
	return j, err
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
