package runtug

import (
	"cmp"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
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
//
// "waste_risk" names when a paid pool is about to lose quota at its longest
// window's reset: the projected unused share at that reset is at least
// min_unused_pct (default 20), however far the reset is. An optional
// horizon_hours (default 0 = no limit) additionally requires the reset to
// fall within that many hours. Such a pool ranks ahead of the free seats by
// waste urgency, and its sub-daily windows refuse only when exhausted.
type PoolPolicy struct {
	Version     int              `json:"version"`
	SourceDoc   string           `json:"source_doc"`
	Order       []string         `json:"order"`
	Free        []FreeSet        `json:"free"`
	CreditPools []string         `json:"credit_pools"`
	WasteBelow  *float64         `json:"waste_below_projected_pct"`
	ExcludeAt   *float64         `json:"exclude_at_projected_pct"`
	WasteRisk   *WasteRiskPolicy `json:"waste_risk"`
}

// WasteRiskPolicy holds the waste-risk override thresholds; absent fields
// take their defaults.
type WasteRiskPolicy struct {
	HorizonHours *float64 `json:"horizon_hours"`
	MinUnusedPct *float64 `json:"min_unused_pct"`
}

const (
	defaultWasteRiskHorizonHours = 0.0
	defaultWasteRiskMinUnusedPct = 20.0
)

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
	// validate fills defaults in place; call it before p is copied out.
	e := p.validate()
	return p, e
}

// validate checks a decoded pool policy and fills absent thresholds with
// their defaults. It is rerun after simulate's --policy-set overrides.
func (p *PoolPolicy) validate() error {
	if p.Version != 1 || len(p.Order) == 0 {
		return fail(exitConfig, "invalid pool order policy")
	}
	for _, tier := range poolTiers {
		if !slices.Contains(p.Order, tier) {
			return fail(exitConfig, "pool order policy missing tier %q", tier)
		}
	}
	for _, tier := range p.Order {
		if !slices.Contains(poolTiers, tier) {
			return fail(exitConfig, "unknown pool order tier %q", tier)
		}
	}
	for _, fs := range p.Free {
		if !validID(fs.Pool) {
			return fail(exitConfig, "invalid free pool name %q", fs.Pool)
		}
	}
	for _, pool := range p.CreditPools {
		if !validID(pool) {
			return fail(exitConfig, "invalid credit pool name %q", pool)
		}
	}
	for _, x := range []**float64{&p.WasteBelow, &p.ExcludeAt} {
		if *x == nil {
			v := 100.0
			*x = &v
		}
	}
	if *p.WasteBelow < 0 || *p.ExcludeAt <= 0 || *p.WasteBelow > *p.ExcludeAt {
		return fail(exitConfig, "invalid projected-at-reset thresholds")
	}
	if p.WasteRisk == nil {
		p.WasteRisk = &WasteRiskPolicy{}
	}
	if p.WasteRisk.HorizonHours == nil {
		v := defaultWasteRiskHorizonHours
		p.WasteRisk.HorizonHours = &v
	}
	if p.WasteRisk.MinUnusedPct == nil {
		v := defaultWasteRiskMinUnusedPct
		p.WasteRisk.MinUnusedPct = &v
	}
	if h, m := *p.WasteRisk.HorizonHours, *p.WasteRisk.MinUnusedPct; !finite(h) || !finite(m) || h < 0 || m < 0 || m > 100 {
		return fail(exitConfig, "invalid waste_risk thresholds")
	}
	return nil
}

func finite(x float64) bool { return !math.IsNaN(x) && !math.IsInf(x, 0) }
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
	urgency   *float64
	resetHrs  *float64
	reason    string
	outcome   string
	judgments []quotaJudgment
	// exhausted marks a free rung whose account projection reaches the
	// exclusion threshold: only an admit on a free bucket may keep it.
	exhausted bool
	// risk marks a paid pool at waste risk (PoolPolicy.wasteRisk); unused is
	// its projected unused share at the longest window's reset.
	risk   bool
	unused *float64
}

type selPlan struct {
	policy   PoolPolicy
	order    []string
	req      selReq
	cands    []*selCand
	ranked   []*selCand // tiered candidates in rank order
	chosen   *selCand
	degraded []string
	at       time.Time
	// routeRefusal names the role refusal (role_no_candidate,
	// role_exhausted_wait, role_exhausted_escalate) when routing ended the
	// selection; before is the role's own exhausted attempt when this plan
	// is its downgrade_to retry.
	routeRefusal string
	before       *selPlan
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
	// excludeFamilies drops seats of those families; onlyFamily keeps just that
	// one (delegate --panel picks one seat per distinct family).
	excludeFamilies []string
	onlyFamily      string
	// route is the role routing decision (routing.go); nil filters nothing.
	route *routing
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
			case slices.Contains(req.excludeFamilies, seat.Family):
				reason = "family_excluded"
			case req.onlyFamily != "" && seat.Family != req.onlyFamily:
				reason = "family_filtered"
			case !slices.Contains(roles, req.role):
				reason = "seat_role"
			case req.role == "builder" && row.Gate != "default":
				reason = "builder_gate"
			case (req.role == "tester" || req.role == "reviewer") && req.builderFamily == "":
				reason = "builder_family_unreadable"
			case (req.role == "tester" || req.role == "reviewer") && seat.Family == req.builderFamily:
				reason = "family_excluded"
			case seat.seatE6(row.Effort) && a.envOr("RUNTUG_E6_ARM", "SCOPEFUEL_E6_ARM") != seat.Canon+"@"+row.Effort:
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
func (p PoolPolicy) isFree(row CatalogRow) bool {
	for _, fs := range p.Free {
		if fs.Pool == row.Pool && (len(fs.Models) == 0 || slices.Contains(fs.Models, row.ModelID)) {
			return true
		}
	}
	return false
}
func (p PoolPolicy) tierOf(row CatalogRow, order []string, pools map[string]PoolView, poolsOK bool) (string, int) {
	if p.isFree(row) {
		return "free", slices.Index(order, "free")
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

// wasteRisk reports whether a paid pool is about to lose quota: its longest
// window's (a day or more) projected unused share at reset is at least the
// policy minimum and, when the policy sets a horizon above 0, that reset
// falls within it. The unused
// share is (100 − projected) of that window, i.e. waste urgency × hours to
// reset. Free seats and credit pools are never at waste risk, and a pool
// without a readable ok spend status, or whose longest window cannot be
// paced, is not either. The returned share is set whenever it is readable.
func (p PoolPolicy) wasteRisk(row CatalogRow, free bool, v PoolView, ok bool) (bool, *float64) {
	if !ok || v.Urgency == nil || v.ResetHours == nil || *v.ResetHours <= 0 {
		return false, nil
	}
	unused := *v.Urgency * *v.ResetHours
	if free || slices.Contains(p.CreditPools, row.Pool) || v.Status != "ok" || v.Stale || v.Class != "spend" {
		return false, &unused
	}
	wr := p.WasteRisk
	if h := *wr.HorizonHours; h > 0 && *v.ResetHours > h {
		return false, &unused
	}
	return unused >= *wr.MinUnusedPct, &unused
}

// exhaustReason names why a non-free candidate's pool excludes it, or "".
// A pool not at waste risk is excluded when its projection — every window's
// pace, sub-daily included — reaches the exclusion threshold. A waste-risk
// pool is used up rather than paced: only its windows of a day or more are
// projected, and a sub-daily window refuses only once exhausted (used at or
// above 100%).
func (p PoolPolicy) exhaustReason(v PoolView, risk bool) string {
	if !risk {
		if v.Projected != nil && *v.Projected >= *p.ExcludeAt {
			return "projected_exhaust"
		}
		return ""
	}
	if v.ShortUsed != nil && *v.ShortUsed >= 100 || v.Blocking != nil && *v.Blocking >= 100 {
		return "window_exhausted"
	}
	long := v.LongProjected
	if long == nil && v.Urgency != nil && v.ResetHours != nil {
		x := 100 - *v.Urgency**v.ResetHours
		long = &x
	}
	if long != nil && *long >= *p.ExcludeAt {
		return "projected_exhaust"
	}
	return ""
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
//
// An exhausted rung (its account projects past the exclusion threshold) is
// admitted only on a free-bucket gate basis; any other basis drops it.
func (sp *selPresence) admit(ctx context.Context, qr QuotaRequest, seat *Seat, pool, host string, exhausted bool) ([]quotaJudgment, error) {
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
		if err == nil && exhausted && !strings.HasPrefix(j.Basis, freeBasisPrefix) {
			j.Outcome, j.ExitCode = "refused", exitSeatRefused
			err = fail(exitSeatRefused, "projected_exhaust: gate basis %q is not a free bucket", j.Basis)
		}
		judgments = append(judgments, j)
		if err == nil {
			return judgments, nil
		}
		last = err
	}
	if last == nil {
		last = fail(exitEvictionUnconfirmed, "no host has usable quota evidence")
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
// wins, then the pool with the higher waste urgency — the unused projected
// share per hour until its longest window's reset; a pool whose reset can be
// paced always ranks ahead of one whose cannot — then the smaller effort.
// Sub-daily windows (e.g. 5h) never rank a pool; they gate admission only.
// A pool whose reset cannot be paced ranks by its projected use at reset. A
// pool projected to run out before reset is never a candidate.
func (a *App) selectProfile(ctx context.Context, s Spawn) (*selPlan, error) {
	if a.Quota == nil {
		return nil, fail(exitConfig, "auto selection needs the quota catalog")
	}
	req := selReq{t: s.T, role: s.Role, effort: s.Effort, allowEscalation: s.AllowEscalation, builderFamily: s.ParentFamily, excludeFamilies: s.ExcludeFamilies, onlyFamily: s.OnlyFamily}
	tags, e := parseTags(s.Tags)
	if e != nil {
		return nil, e
	}
	req.tags = tags
	task, _, e := a.taskPrecheck(ctx, s)
	if e != nil {
		return nil, e
	}
	if s.Grade != "" {
		req.grade = s.Grade
	} else if m := taskMarker.FindStringSubmatch(task.Title); m != nil {
		req.grade = strings.ToUpper(m[1])
		if strings.ToUpper(m[2]) != s.T {
			return nil, fail(exitSeatRefused, "task scale does not match --t")
		}
	} else {
		return nil, fail(exitSeatRefused, "auto selection needs a readable task grade")
	}
	if _, ok := gradeOrder[req.grade]; !ok {
		return nil, fail(exitSeatRefused, "unreadable task grade %q", req.grade)
	}
	req.urgency, req.urgencySource = resolveUrgency(s.Urgency, task)
	if !s.Unrouted {
		if req.route, e = a.routeFor(ctx, s.Role, s.RoutingRole); e != nil {
			return nil, e
		}
	}
	return a.selectFor(ctx, s, req)
}

// selectFor ranks and admits the catalog rungs for a resolved request. It is
// the selection core shared by spawn and `runtug plan`: it reads the catalog
// and pool status, asks presence and the quota gates, and writes nothing.
func (a *App) selectFor(ctx context.Context, s Spawn, req selReq) (*selPlan, error) {
	policy, e := a.poolPolicy()
	if e != nil {
		return nil, e
	}
	p := &selPlan{policy: policy, order: slices.Clone(policy.Order), req: req, at: a.Now()}
	rows, e := a.Quota.Catalog(ctx)
	if e != nil {
		return nil, fail(exitConfig, "bench catalog unreadable: %v", e)
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
		case !req.route.admits(row.Profile):
			c.reason = "role_filtered"
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
			c.blocking, c.projected, c.urgency, c.resetHrs = v.Blocking, v.Projected, v.Urgency, v.ResetHours
			free := policy.isFree(row)
			c.risk, c.unused = policy.wasteRisk(row, free, v, poolsErr == nil)
			if c.reason == "" && free && v.Projected != nil && *v.Projected >= *policy.ExcludeAt {
				// The account projection does not bind a free bucket; the gate
				// names its basis at admission, which decides.
				c.exhausted = true
			} else if c.reason == "" && !free {
				c.reason = policy.exhaustReason(v, c.risk)
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
	orderSelCands(pending)
	p.ranked = pending
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
		c.judgments, e = pres.admit(ctx, qr, c.seat, c.row.Pool, s.Host, c.exhausted)
		if e != nil {
			if c.exhausted && strings.HasPrefix(e.Error(), "projected_exhaust") {
				c.reason, c.outcome = "projected_exhaust", "dropped:projected_exhaust"
				continue
			}
			c.reason = fmt.Sprintf("gate_refused:%d", exitCode(e))
			c.outcome = "dropped:gate_refused"
			continue
		}
		c.outcome = "chosen"
		p.chosen = c
	}
	if p.chosen == nil {
		if req.route != nil && req.route.rule != nil {
			return a.roleExhausted(ctx, s, req, p)
		}
		return p, fail(exitSeatRefused, "auto selection admitted no candidate")
	}
	return p, nil
}

// orderSelCands ranks tiered candidates: waste-risk pools first (by waste
// urgency), then policy tier, then the lowest grade
// that fits, then waste urgency — the unused projected share per hour until
// the pool's longest window's reset, highest first. A candidate whose pool
// reset can be paced always ranks ahead of one whose cannot; candidates
// without urgency keep the lower projected use at reset (the used
// percentage when it cannot be paced yet), then smaller effort, catalog
// score, profile.
func orderSelCands(pending []*selCand) {
	slices.SortStableFunc(pending, func(x, y *selCand) int {
		// Waste-risk pools rank ahead of every tier, free included, and among
		// themselves by waste urgency first; everything else keeps the tier
		// order.
		if x.risk != y.risk {
			if x.risk {
				return -1
			}
			return 1
		}
		if x.risk {
			if c := cmp.Compare(*y.urgency, *x.urgency); c != 0 {
				return c
			}
		} else if x.tier != y.tier {
			return x.tier - y.tier
		}
		// Grade fit: the lowest grade that satisfies the task.
		if gx, gy := gradeOrder[x.row.Grade], gradeOrder[y.row.Grade]; gx != gy {
			return gx - gy
		}
		// Paceable resets rank ahead of unpaceable ones: mixing the urgency
		// and projected orders in one comparison is not a strict weak
		// ordering.
		if (x.urgency != nil) != (y.urgency != nil) {
			if x.urgency != nil {
				return -1
			}
			return 1
		}
		if x.urgency != nil {
			if c := cmp.Compare(*y.urgency, *x.urgency); c != 0 {
				return c
			}
		}
		if c := cmp.Compare(rankBlocking(firstPct(x.projected, x.blocking)), rankBlocking(firstPct(y.projected, y.blocking))); c != 0 {
			return c
		}
		if ex, ey := rankEffort(x.row.Effort), rankEffort(y.row.Effort); ex != ey {
			return ex - ey
		}
		if c := cmp.Compare(rankScore(x.row.Score), rankScore(y.row.Score)); c != 0 {
			return c
		}
		if c := cmp.Compare(x.row.Profile, y.row.Profile); c != 0 {
			return c
		}
		return cmp.Compare(x.row.Effort, y.row.Effort)
	})
}

// horizonClause renders the optional waste-risk horizon for the rule text.
func horizonClause(h float64) string {
	if h > 0 {
		return fmt.Sprintf(", resetting within %gh", h)
	}
	return ", any reset distance"
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
		row["waste_risk"] = c.risk
		if c.unused != nil {
			row["unused_at_reset_pct"] = *c.unused
		}
		if c.tier >= 0 {
			// Ranked candidates state how they were ordered: by waste urgency
			// when the pool's longest window's reset was paceable, else by
			// the projected-at-reset fallback.
			if c.urgency != nil {
				row["waste_urgency"] = *c.urgency
				row["waste_order"] = "reset"
			} else {
				row["waste_order"] = "projected_fallback"
			}
			if c.resetHrs != nil {
				row["reset_hours"] = *c.resetHrs
			}
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
		"rule":           fmt.Sprintf("waste-risk pools first (longest window projected >= %g%% unused at reset%s; ranked by waste urgency; sub-daily windows refuse only when exhausted), then tier, then lowest grade >= %s, then waste urgency (unused share per hour to the longest window's reset; a paceable reset ranks ahead of an unpaceable one, which keeps the lower projected-at-reset order; sub-daily windows never rank), then smaller effort; waste < %g%%, excluded >= %g%% projected", *p.policy.WasteRisk.MinUnusedPct, horizonClause(*p.policy.WasteRisk.HorizonHours), p.req.grade, *p.policy.WasteBelow, *p.policy.ExcludeAt),
		"waste_risk":     map[string]any{"horizon_hours": *p.policy.WasteRisk.HorizonHours, "min_unused_pct": *p.policy.WasteRisk.MinUnusedPct},
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
		canon := c.row.Profile
		if c.seat != nil {
			canon = c.seat.Canon
		}
		chosen := map[string]any{"seat": c.seatName, "canon": canon, "effort": c.row.Effort, "model_id": c.row.ModelID, "pool": c.row.Pool, "gate": c.row.Gate, "tier": c.tierName, "grade": c.row.Grade, "waste_risk": c.risk}
		why := fmt.Sprintf("tier=%s grade=%s for task %s", c.tierName, c.row.Grade, p.req.grade)
		if c.risk {
			why = "waste_risk " + why
		}
		if c.projected != nil {
			chosen["projected_pct"] = *c.projected
			why += fmt.Sprintf(" projected=%.0f%%", *c.projected)
		}
		if c.urgency != nil {
			chosen["waste_urgency"] = *c.urgency
			why += fmt.Sprintf(" waste_urgency=%.2f", *c.urgency)
		}
		chosen["why"] = why
		rec["chosen"] = chosen
	}
	if len(p.degraded) > 0 {
		rec["degraded"] = p.degraded
	}
	if p.req.route != nil {
		rt := p.req.route.record(p.routeRefusal)
		if p.before != nil {
			// The role's own attempt, every profile exhausted, before the
			// downgrade_to retry this record chose from.
			rt["before_downgrade"] = p.before.record()["candidates"]
		}
		rec["routing"] = rt
	}
	return rec
}
