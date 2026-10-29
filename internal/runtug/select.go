package runtug

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

//go:embed pool-order.json
var defaultPoolOrder []byte

// PoolPolicy is the versioned model-selection order from
// internal/runtug/pool-order.json. It encodes the operator's paid/credit pool
// order (source_doc) without embedding model IDs or grades: "free" matches
// catalog rows by (pool, model) so the SWE-2 free bucket is tried first,
// "credit_pools" names spend pools drawn on credit, and a subscription pool
// whose projected use at reset stays below "waste_below_projected_pct" is
// promoted to waste because the rest is lost at reset. A pool projected at or
// above "exclude_at_projected_pct" is never selected. Both default to 100
// when absent; an explicit 0 waste threshold disables projected waste.
type PoolPolicy struct {
	Version     int       `json:"version"`
	SourceDoc   string    `json:"source_doc"`
	Order       []string  `json:"order"`
	Free        []FreeSet `json:"free"`
	CreditPools []string  `json:"credit_pools"`
	WasteBelow  *float64  `json:"waste_below_projected_pct"`
	ExcludeAt   *float64  `json:"exclude_at_projected_pct"`
}
type FreeSet struct {
	Pool   string   `json:"pool"`
	Models []string `json:"models"`
}

var poolTiers = []string{"free", "waste", "credit", "ranked"}

func LoadPoolPolicy(path string) (PoolPolicy, error) {
	b := defaultPoolOrder
	if path == "" {
		path = os.Getenv("RUNTUG_POOL_POLICY")
	}
	if path != "" {
		var e error
		b, e = os.ReadFile(expand(path))
		if e != nil {
			return PoolPolicy{}, e
		}
	}
	var p PoolPolicy
	if e := decode(b, &p); e != nil {
		return p, e
	}
	if p.Version != 1 || len(p.Order) == 0 {
		return p, fail(70, "invalid pool order policy")
	}
	for _, tier := range poolTiers {
		if !slices.Contains(p.Order, tier) {
			return p, fail(70, "pool order policy missing tier %q", tier)
		}
	}
	for _, tier := range p.Order {
		if !slices.Contains(poolTiers, tier) {
			return p, fail(70, "unknown pool order tier %q", tier)
		}
	}
	for _, fs := range p.Free {
		if !validID(fs.Pool) {
			return p, fail(70, "invalid free pool name %q", fs.Pool)
		}
	}
	for _, pool := range p.CreditPools {
		if !validID(pool) {
			return p, fail(70, "invalid credit pool name %q", pool)
		}
	}
	for _, x := range []**float64{&p.WasteBelow, &p.ExcludeAt} {
		if *x == nil {
			v := 100.0
			*x = &v
		}
	}
	if *p.WasteBelow < 0 || *p.ExcludeAt <= 0 || *p.WasteBelow > *p.ExcludeAt {
		return p, fail(70, "invalid projected-at-reset thresholds")
	}
	return p, nil
}
func (a *App) poolPolicy() (PoolPolicy, error) {
	return LoadPoolPolicy(str(a.Config.Local, "pool_policy"))
}

// selCand is one catalog rung evaluated for this spawn. Reason records why it
// was dropped; Tier and TierName record the policy bucket it ranked under.
type selCand struct {
	row       CatalogRow
	seat      *Seat
	seatName  string
	tier      int
	tierName  string
	blocking  *float64
	projected *float64
	reason    string
	outcome   string
	judgments []quotaJudgment
}

type selPlan struct {
	policy   PoolPolicy
	order    []string
	req      selReq
	cands    []*selCand
	chosen   *selCand
	degraded []string
	at       time.Time
}
type selReq struct {
	grade           string
	t               string
	role            string
	tags            []string
	effort          string
	allowEscalation bool
	builderFamily   string
	urgency         string
	urgencySource   string
}

// effortRank orders rung efforts cheap-first; an empty effort is the catalog's
// default rung and ranks before named rungs.
var effortRank = map[string]int{"": 0, "low": 1, "medium": 2, "high": 3, "xhigh": 4, "max": 5, "ultra": 6}

func rankEffort(e string) int {
	if n, ok := effortRank[e]; ok {
		return n
	}
	return len(effortRank)
}

// seatRoles resolves the role list a seat admits for one catalog effort,
// mirroring Seat.Check's rung lookup. A missing rung is a refusal.
func (s Seat) seatRoles(effort string) ([]string, bool) {
	if len(s.Rungs) == 0 {
		return s.Roles, true
	}
	r, ok := s.Rungs[effort]
	if !ok {
		return nil, false
	}
	return r.Roles, true
}
func (s Seat) seatE6(effort string) bool {
	e6 := s.E6
	if len(s.Rungs) > 0 {
		if r, ok := s.Rungs[effort]; ok && r.E6 != nil {
			e6 = *r.E6
		}
	}
	return e6
}

// seatFor picks the seat alias that should launch one catalog rung. Seat
// candidates share the rung's catalog profile as their canon; an exact
// name match wins, then the lexically smallest alias. Role, rung, pinned
// effort, builder-family and E6 rules mirror Seat.Check.
func (a *App) seatFor(row CatalogRow, req selReq) (string, *Seat, string) {
	var names []string
	for name, seat := range a.Policy.Profiles {
		if seat.Canon == row.Profile {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return "", nil, "no_seat"
	}
	slices.Sort(names)
	if exact, ok := a.Policy.Profiles[row.Profile]; ok && row.Effort == "" && exact.Canon == row.Profile && (exact.needsEffort() || exact.Effort != "") {
		// The profile's own seat launches it at a declared rung, so an alias
		// seat cannot stand in for the rung-less default row.
		return "", nil, "effort_empty"
	}
	if i := slices.Index(names, row.Profile); i > 0 {
		names = append([]string{row.Profile}, slices.Concat(names[:i], names[i+1:])...)
	}
	firstReason := ""
	for _, name := range names {
		seat := a.Policy.Profiles[name]
		reason := ""
		if row.Effort != "" && seat.Route != "" {
			// A routed harness runs at its own default; it has no named rung.
			reason = "seat_rung"
		} else if row.Effort == "" && (seat.needsEffort() || seat.Effort != "") {
			// A rung-less row is the profile's default rung. A seat whose
			// launcher needs an effort, or that launches at its own default
			// rung, cannot represent it.
			reason = "effort_empty"
		} else if seat.Pin && seat.Effort != row.Effort {
			reason = "effort_mismatch"
		} else if roles, ok := seat.seatRoles(row.Effort); !ok {
			reason = "seat_rung"
		} else {
			switch {
			case !slices.Contains(roles, req.role):
				reason = "seat_role"
			case req.role == "builder" && row.Gate != "default":
				reason = "builder_gate"
			case (req.role == "tester" || req.role == "reviewer") && req.builderFamily == "":
				reason = "builder_family_unreadable"
			case (req.role == "tester" || req.role == "reviewer") && seat.Family == req.builderFamily:
				reason = "family_excluded"
			case seat.seatE6(row.Effort) && os.Getenv("SCOPEFUEL_E6_ARM") != seat.Canon+"@"+row.Effort:
				reason = "e6_marker"
			}
		}
		if reason == "" {
			return name, &seat, ""
		}
		if firstReason == "" {
			firstReason = reason
		}
	}
	return "", nil, firstReason
}

// tierOf classes a candidate under the operator pool order. A subscription
// pool is waste when its projected use at reset stays below the policy
// threshold; without a projection the quota adapter's verdict flag decides.
// Pools without a readable spend status are unknown rather than ranked.
func (p PoolPolicy) tierOf(row CatalogRow, order []string, pools map[string]PoolView, poolsOK bool) (string, int) {
	for _, fs := range p.Free {
		if fs.Pool == row.Pool && (len(fs.Models) == 0 || slices.Contains(fs.Models, row.ModelID)) {
			return "free", slices.Index(order, "free")
		}
	}
	if !poolsOK {
		return "", -1
	}
	v, ok := pools[row.Pool]
	if !ok || v.Status != "ok" || v.Stale || v.Class != "spend" {
		return "", -1
	}
	tier := "ranked"
	if v.Projected != nil && *v.Projected < *p.WasteBelow || v.Projected == nil && v.Waste != nil && *v.Waste {
		tier = "waste"
	}
	if slices.Contains(p.CreditPools, row.Pool) {
		tier = "credit"
	}
	return tier, slices.Index(order, tier)
}

// Urgency values and the sources a resolved urgency can come from.
const (
	urgencyNormal = "normal"
	urgencyUrgent = "urgent"
)

// urgentPriorities are the hk task priorities that make a spawn urgent when
// --urgency is omitted (policy/quota-stop: urgent_priorities [1, 2]).
var urgentPriorities = []int{1, 2}

// resolveUrgency applies the explicit flag first, then the bound task's hk
// priority, then the normal default. The returned source names which won.
func resolveUrgency(flag string, task Task) (string, string) {
	if flag != "" {
		return flag, "flag"
	}
	if n, ok := taskPriority(task.Priority); ok && slices.Contains(urgentPriorities, n) {
		return urgencyUrgent, "task-priority"
	}
	return urgencyNormal, "default"
}
func taskPriority(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), n == float64(int(n))
	case string:
		i, e := strconv.Atoi(strings.TrimSpace(n))
		return i, e == nil
	}
	return 0, false
}

// selHost is one spawn host as the presence rule sees it.
type selHost struct {
	name     string
	local    bool
	cfg      map[string]any
	presence string
	awake    bool
	wakeable bool
	eligible bool
	excluded bool
}

// selPresence enumerates the spawn hosts once for a selection round and
// answers "does any host that could take this job show fresh presence for the
// candidate's account". Enumeration mirrors plan(): local plus configured
// hosts, hub views for connectivity, wakeable hosts counted while asleep.
type selPresence struct {
	a        *App
	hub      map[string]HostView
	presErr  bool
	presence []PresenceRecord
	accounts map[string]SharedAccount
	hosts    []selHost
	presTTL  time.Duration
	now      time.Time
	degraded []string
}

func (a *App) newSelPresence(ctx context.Context, s Spawn, required []string) *selPresence {
	sp := &selPresence{a: a, now: a.Now(), presTTL: a.placementTTL("presence_ttl_s", 86400), accounts: map[string]SharedAccount{}}
	sp.hub, sp.degraded = a.hostViews(ctx, s.CWD)
	if a.Source == nil {
		sp.presErr = true
		sp.degraded = append(sp.degraded, "presence_unavailable")
	} else if records, e := a.Source.Presence(ctx); e != nil {
		sp.presErr = true
		sp.degraded = append(sp.degraded, "presence_degraded")
	} else {
		sp.presence = records
	}
	machine := a.machine()
	add := func(name string, cfg map[string]any, local bool) {
		h := selHost{name: name, local: local, cfg: cfg, presence: firstNonempty(str(cfg, "presence_id"), name)}
		if local {
			h.presence = firstNonempty(str(cfg, "presence_id"), machine)
			h.awake = true
			if !boolean(cfg, "spawn", true) {
				h.excluded = true
			}
		} else {
			h.wakeable = str(cfg, "wake") == "panewire"
			if hv, reported := sp.hub[name]; reported {
				h.awake = hv.Connected
				if strings.Contains(hv.Reason, "not_accepting") {
					h.excluded = true
				}
			} else {
				h.awake = !h.wakeable || sp.hub == nil
			}
		}
		h.eligible = true
		for _, t := range required {
			if !slices.Contains(stringsValue(cfg, "tags"), t) {
				h.eligible = false
			}
		}
		sp.hosts = append(sp.hosts, h)
	}
	add(machine, a.Config.Local, true)
	names := slices.Clone(a.Config.HostOrder)
	var extra []string
	for name := range a.Config.Hosts {
		if !slices.Contains(names, name) {
			extra = append(extra, name)
		}
	}
	slices.Sort(extra)
	for _, name := range append(names, extra...) {
		add(name, a.Config.Hosts[name], false)
	}
	return sp
}

// present reports whether some spawnable, tag-eligible host that can run the
// seat's capability shows fresh presence for the pool's account. Degraded
// presence or quota-cache reads make the answer false (fail closed).
func (sp *selPresence) present(ctx context.Context, seat *Seat, pool string) bool {
	if sp.presErr || pool == "" {
		return false
	}
	keys := neededAccounts(nil, pool, sp.presence)
	if len(keys) == 0 {
		return false
	}
	for _, k := range keys {
		if _, ok := sp.accounts[k]; !ok {
			acc, e := sp.a.Source.SharedAccount(ctx, k)
			if e != nil {
				acc = SharedAccount{Key: k, Degraded: true}
			}
			sp.accounts[k] = acc
		}
	}
	for _, h := range sp.hosts {
		if !h.eligible || h.excluded || !h.awake && !h.wakeable {
			continue
		}
		if seat.Capability != "standard" && !boolean(h.cfg, seat.Capability, false) {
			continue
		}
		if sp.a.candidateLogin(sp.presence, sp.accounts, keys, h.presence, sp.now, sp.presTTL) == loginPresent {
			return true
		}
	}
	return false
}

// admit tries this rung on each eligible host. A remote attempt always names
// that host's current account, even when the spawner has a different login.
func (sp *selPresence) admit(ctx context.Context, qr QuotaRequest, seat *Seat, pool, host string) ([]quotaJudgment, error) {
	var judgments []quotaJudgment
	var last error
	for _, h := range sp.hosts {
		if host != "" && host != "auto" && host != h.name && !(h.local && host == "local") {
			continue
		}
		if !h.eligible || h.excluded || !h.awake && !h.wakeable || seat.Capability != "standard" && !boolean(h.cfg, seat.Capability, false) {
			continue
		}
		key := hostAccount(sp.presence, pool, h.presence, sp.now, sp.presTTL)
		// Preserve the selector's existing pool-level presence prerequisite.
		// Local admission itself uses the local gate without requiring that
		// this host also publish readable shared evidence.
		j, err := sp.a.judgeHost(ctx, qr, pool, h.name, h.local, key, sp.accounts[key])
		judgments = append(judgments, j)
		if err == nil {
			return judgments, nil
		}
		last = err
	}
	if last == nil {
		last = fail(4, "no host has usable quota evidence")
	}
	return judgments, last
}

// selectProfile runs automatic model selection when -m is omitted or "auto":
// the bench catalog's live rungs are filtered to the task's grade, admission
// gate, role and effort constraints, ordered by the versioned pool-order
// policy, then admitted in order through presence evidence and the existing
// quota gate. The first admitted rung wins. Every row's outcome is recorded.
//
// The tier order is the policy order for every urgency (policy/quota-stop:
// free first, always). Inside a tier the lowest grade that satisfies the task
// wins, then the pool with the lower projected use at reset, then the smaller
// effort. A pool projected to run out before reset is never a candidate.
func (a *App) selectProfile(ctx context.Context, s Spawn) (*selPlan, error) {
	if a.Quota == nil {
		return nil, fail(70, "auto selection needs the quota catalog")
	}
	req := selReq{t: s.T, role: s.Role, effort: s.Effort, allowEscalation: s.AllowEscalation, builderFamily: s.ParentFamily}
	tags, e := parseTags(s.Tags)
	if e != nil {
		return nil, e
	}
	req.tags = tags
	task, _, e := a.taskPrecheck(ctx, s)
	if e != nil {
		return nil, e
	}
	if m := taskMarker.FindStringSubmatch(task.Title); m != nil {
		req.grade = strings.ToUpper(m[1])
		if strings.ToUpper(m[2]) != s.T {
			return nil, fail(77, "task scale does not match --t")
		}
	} else {
		return nil, fail(77, "auto selection needs a readable task grade")
	}
	if _, ok := gradeOrder[req.grade]; !ok {
		return nil, fail(77, "unreadable task grade %q", req.grade)
	}
	policy, e := a.poolPolicy()
	if e != nil {
		return nil, e
	}
	req.urgency, req.urgencySource = resolveUrgency(s.Urgency, task)
	p := &selPlan{policy: policy, order: slices.Clone(policy.Order), req: req, at: a.Now()}
	rows, e := a.Quota.Catalog(ctx)
	if e != nil {
		return nil, fail(70, "bench catalog unreadable: %v", e)
	}
	pools, poolsErr := a.Quota.Pools(ctx)
	if poolsErr != nil {
		p.degraded = append(p.degraded, "quota_status_unavailable")
	}
	var pending []*selCand
	for _, row := range rows {
		c := &selCand{row: row, tier: -1}
		p.cands = append(p.cands, c)
		_, validGrade := gradeOrder[row.Grade]
		switch {
		case row.RetiredAt != nil:
			c.reason = "retired"
		case row.Profile == "" || row.ModelID == "" || row.Pool == "":
			c.reason = "catalog_incomplete"
		case !validGrade:
			c.reason = "grade_invalid"
		case gradeOrder[row.Grade] < gradeOrder[req.grade]:
			c.reason = "grade_insufficient"
		case row.Gate != "default" && row.Gate != "escalation" && row.Gate != "consult_only":
			c.reason = "gate_unknown"
		case row.Gate != "default" && !req.allowEscalation:
			c.reason = "gate_class"
		case req.effort != "" && row.Effort != req.effort:
			c.reason = "effort_mismatch"
		case !validID(row.Pool):
			c.reason = "pool_invalid"
		default:
			if name, seat, reason := a.seatFor(row, req); seat == nil {
				c.reason = reason
			} else {
				c.seat = seat
				c.seatName = name
			}
		}
		if v, ok := pools[row.Pool]; ok {
			c.blocking, c.projected = v.Blocking, v.Projected
			if c.reason == "" && v.Projected != nil && *v.Projected >= *policy.ExcludeAt {
				c.reason = "projected_exhaust"
			}
		}
		if c.reason != "" {
			c.outcome = "dropped:" + c.reason
			continue
		}
		tierName, n := policy.tierOf(row, p.order, pools, poolsErr == nil)
		if n < 0 {
			c.reason = "quota_unknown"
			c.outcome = "dropped:quota_unknown"
			continue
		}
		c.tier, c.tierName = n, tierName
		pending = append(pending, c)
	}
	slices.SortStableFunc(pending, func(x, y *selCand) int {
		if x.tier != y.tier {
			return x.tier - y.tier
		}
		// Grade fit: the lowest grade that satisfies the task.
		if gx, gy := gradeOrder[x.row.Grade], gradeOrder[y.row.Grade]; gx != gy {
			return gx - gy
		}
		// Then the pool with more room at reset; a pool that cannot be
		// paced yet ranks by its used percentage.
		if bx, by := rankBlocking(firstPct(x.projected, x.blocking)), rankBlocking(firstPct(y.projected, y.blocking)); bx != by {
			if bx < by {
				return -1
			}
			return 1
		}
		if ex, ey := rankEffort(x.row.Effort), rankEffort(y.row.Effort); ex != ey {
			return ex - ey
		}
		if sx, sy := rankScore(x.row.Score), rankScore(y.row.Score); sx != sy {
			if sx < sy {
				return -1
			}
			return 1
		}
		if x.row.Profile != y.row.Profile {
			if x.row.Profile < y.row.Profile {
				return -1
			}
			return 1
		}
		if x.row.Effort < y.row.Effort {
			return -1
		}
		return 1
	})
	pres := a.newSelPresence(ctx, s, req.tags)
	p.degraded = append(p.degraded, pres.degraded...)
	for _, c := range pending {
		if p.chosen != nil {
			c.outcome = "below_choice"
			continue
		}
		if !pres.present(ctx, c.seat, c.row.Pool) {
			c.reason = "no_presence"
			c.outcome = "dropped:no_presence"
			continue
		}
		qr := QuotaRequest{Profile: c.seat.Canon, GateProfile: c.seat.gateName(), Effort: c.row.Effort, Role: s.Role, OperatorRequest: s.OperatorRequest, RequestedBy: s.RequestedBy, Purpose: s.Purpose}
		c.judgments, e = pres.admit(ctx, qr, c.seat, c.row.Pool, s.Host)
		if e != nil {
			c.reason = fmt.Sprintf("gate_refused:%d", exitCode(e))
			c.outcome = "dropped:gate_refused"
			continue
		}
		c.outcome = "chosen"
		p.chosen = c
	}
	if p.chosen == nil {
		return p, fail(77, "auto selection admitted no candidate")
	}
	return p, nil
}
func firstPct(a, b *float64) *float64 {
	if a != nil {
		return a
	}
	return b
}
func rankBlocking(b *float64) float64 {
	if b == nil {
		return 1e9
	}
	return *b
}
func rankScore(s *float64) float64 {
	if s == nil {
		return -1e9
	}
	return -(*s)
}
func gatePool(gate string) string {
	if strings.HasPrefix(strings.TrimSpace(gate), "{") {
		var receipt struct {
			Pool string `json:"provider_id"`
		}
		if json.Unmarshal([]byte(gate), &receipt) == nil {
			return receipt.Pool
		}
		return ""
	}
	for _, word := range strings.Fields(strings.SplitN(gate, "\n", 2)[0]) {
		if w, ok := strings.CutPrefix(word, "pool="); ok {
			return w
		}
	}
	return ""
}

// record renders the selection decision embedded in the job.placement event:
// the request, the policy version, every catalog rung's outcome and the
// chosen seat.
func (p *selPlan) record() map[string]any {
	cands := []any{}
	for _, c := range p.cands {
		row := map[string]any{"profile": c.row.Profile, "effort": c.row.Effort, "grade": c.row.Grade, "pool": c.row.Pool, "gate": c.row.Gate, "model_id": c.row.ModelID, "outcome": c.outcome}
		if c.seatName != "" {
			row["seat"] = c.seatName
		}
		if c.tierName != "" {
			row["tier"] = c.tierName
		}
		if c.reason != "" {
			row["reason"] = c.reason
		}
		if c.projected != nil {
			row["projected_pct"] = *c.projected
		}
		if c.blocking != nil {
			row["used_pct"] = *c.blocking
		}
		if len(c.judgments) > 0 {
			row["quota_gates"] = c.judgments
		}
		cands = append(cands, row)
	}
	tags := p.req.tags
	if tags == nil {
		tags = []string{}
	}
	rec := map[string]any{
		"mode":           "auto",
		"decision":       fmt.Sprintf("urgency=%s source=%s tiers=%s", p.req.urgency, p.req.urgencySource, strings.Join(p.order, ">")),
		"rule":           fmt.Sprintf("tier, then lowest grade >= %s, then lower projected-at-reset, then smaller effort; waste < %g%%, excluded >= %g%% projected", p.req.grade, *p.policy.WasteBelow, *p.policy.ExcludeAt),
		"policy_version": p.policy.Version,
		"policy_source":  p.policy.SourceDoc,
		"requested":      map[string]any{"grade": p.req.grade, "t": p.req.t, "role": p.req.role, "tags": tags, "effort": p.req.effort, "allow_escalation": p.req.allowEscalation},
		"urgency":        p.req.urgency,
		"urgency_source": p.req.urgencySource,
		"tiers":          p.order,
		"candidates":     cands,
		"at":             p.at.Format(time.RFC3339Nano),
	}
	if c := p.chosen; c != nil {
		chosen := map[string]any{"seat": c.seatName, "canon": c.seat.Canon, "effort": c.row.Effort, "model_id": c.row.ModelID, "pool": c.row.Pool, "gate": c.row.Gate, "tier": c.tierName, "grade": c.row.Grade}
		why := fmt.Sprintf("tier=%s grade=%s for task %s", c.tierName, c.row.Grade, p.req.grade)
		if c.projected != nil {
			chosen["projected_pct"] = *c.projected
			why += fmt.Sprintf(" projected=%.0f%%", *c.projected)
		}
		chosen["why"] = why
		rec["chosen"] = chosen
	}
	if len(p.degraded) > 0 {
		rec["degraded"] = p.degraded
	}
	return rec
}
