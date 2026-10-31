package runtug

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

// `runtug simulate --selection FILE` replays automatic model selection's
// ranking on a recorded or hand-written input document. It reads no quota,
// asks no gate and spawns nothing: it prints the ranked candidate order with
// reasons and the rung that would be tried first.
//
// Input shape (JSON):
//
//	{
//	  "now": "2026-10-31T07:24:00Z",        // optional; --now or the clock wins
//	  "grade": "A",                          // task grade (required)
//	  "role": "worker", "effort": "high",    // optional request constraints
//	  "allow_escalation": false,
//	  "credit_pools": ["anthropic-api"],     // optional; policy list is default
//	  "pools": [
//	    {"id": "claude", "class": "spend", "waste": true,
//	     "used_pct": 31, "projected_pct": 31.8,
//	     "resets_at": "2026-10-31T16:50:00Z", "window": "7d"}
//	  ],
//	  "candidates": [
//	    {"profile": "sonnet", "seat": "sonnet", "effort": "high",
//	     "grade": "A", "pool": "claude", "gate": "default",
//	     "model_id": "m", "free": false}
//	  ]
//	}
//
// A pool's projection comes from `projected_pct` when given, else from
// pacelever's pace over `used_pct`/`window`/`resets_at` (or `reset_hours`
// relative to now). Waste urgency is (100 − projected) / hours-to-reset of
// the pool's longest window of a day or more — sub-daily windows (e.g. 5h)
// never rank — and pools without a readable reset fall back to projected
// order. A `free`
// candidate is excluded only by an observed free limit, never by its shared
// pool's usage or projection.
//
// A job event envelope works too: when the document carries
// `payload.selection`, that record supplies the request under `requested`
// (grade, role, effort, allow_escalation), `at` as now and the `candidates`
// table; unknown extra fields are ignored. A recorded record has no `pools`
// array, so pool views are rebuilt from the candidate rows — per pool the
// first row's `used_pct`/`projected_pct`. Recorded rows carry no reset
// instant, so those pools rank by the projected fallback until
// `--set pool:reset=…` supplies one.
//
//	--set pool:used=N,reset=RFC3339|+Nh[,window=7d][,projected=N]
//
// overrides or creates a pool entry and recomputes its projection and
// urgency from the given window (the entry's own, else 7d).
type simDoc struct {
	Now             string    `json:"now"`
	Grade           string    `json:"grade"`
	Role            string    `json:"role"`
	Effort          string    `json:"effort"`
	AllowEscalation bool      `json:"allow_escalation"`
	CreditPools     []string  `json:"credit_pools"`
	Pools           []simPool `json:"pools"`
	Candidates      []simCand `json:"candidates"`
}

type simPool struct {
	ID         string   `json:"id"`
	Status     string   `json:"status"`
	Stale      bool     `json:"stale"`
	Class      string   `json:"class"`
	Waste      *bool    `json:"waste"`
	UsedPct    *float64 `json:"used_pct"`
	Projected  *float64 `json:"projected_pct"`
	ResetsAt   string   `json:"resets_at"`
	ResetHours *float64 `json:"reset_hours"`
	Window     string   `json:"window"`
}

type simCand struct {
	Profile string `json:"profile"`
	Seat    string `json:"seat"`
	Effort  string `json:"effort"`
	Grade   string `json:"grade"`
	Pool    string `json:"pool"`
	Gate    string `json:"gate"`
	ModelID string `json:"model_id"`
	Free    bool   `json:"free"`
	// FreeLimit records an observed free limit (the free rung's gate refusal):
	// the only way a free-tier seat is excluded.
	FreeLimit bool     `json:"free_limit"`
	Score     *float64 `json:"score"`
	RetiredAt *string  `json:"retired_at"`
}

// simSelRecord is the selection record a job.placement event embeds under
// payload.selection: the request under `requested`, `at`, and candidate rows
// that carry their pool's used/projected reading. Fields beyond these are
// ignored.
type simSelRecord struct {
	At         string       `json:"at"`
	Requested  simRequest   `json:"requested"`
	Candidates []simRecCand `json:"candidates"`
}

type simRequest struct {
	Grade           string `json:"grade"`
	Role            string `json:"role"`
	Effort          string `json:"effort"`
	AllowEscalation bool   `json:"allow_escalation"`
}

// simRecCand is a recorded candidate row: the catalog fields plus the pool
// reading and outcome the record kept on the rung.
type simRecCand struct {
	simCand
	UsedPct   *float64 `json:"used_pct"`
	Projected *float64 `json:"projected_pct"`
	Outcome   string   `json:"outcome"`
}

// simSet is one --set override: pool plus the keys it rewrites.
type simSet struct {
	pool      string
	used      *float64
	projected *float64
	reset     string // RFC3339 or +Nh relative to now
	window    string
}

func parseSimSet(v string) (simSet, error) {
	var s simSet
	name, rest, ok := strings.Cut(v, ":")
	if !ok || !validID(name) {
		return s, fail(exitUsage, "invalid --set %q: want pool:used=N,reset=RFC3339|+Nh[,window=D][,projected=N]", v)
	}
	s.pool = name
	for _, kv := range strings.Split(rest, ",") {
		k, val, ok := strings.Cut(strings.TrimSpace(kv), "=")
		if !ok {
			return s, fail(exitUsage, "invalid --set term %q", kv)
		}
		switch k {
		case "used", "projected":
			n, e := strconv.ParseFloat(val, 64)
			if e != nil || math.IsNaN(n) || math.IsInf(n, 0) {
				return s, fail(exitUsage, "invalid --set %s value %q", k, val)
			}
			if k == "used" {
				s.used = &n
			} else {
				s.projected = &n
			}
		case "reset":
			if strings.HasPrefix(val, "+") {
				d := strings.TrimPrefix(val, "+")
				if _, e := windowDuration(d); e != nil {
					if _, e := time.ParseDuration(d); e != nil {
						return s, fail(exitUsage, "invalid --set reset %q", val)
					}
				}
			} else if _, e := time.Parse(time.RFC3339Nano, val); e != nil {
				return s, fail(exitUsage, "invalid --set reset %q", val)
			}
			s.reset = val
		case "window":
			if _, e := windowDuration(val); e != nil {
				return s, fail(exitUsage, "invalid --set window %q", val)
			}
			s.window = val
		default:
			return s, fail(exitUsage, "unknown --set key %q", k)
		}
	}
	return s, nil
}

// resetClock parses a reset spec: an RFC3339 instant or +Nh/+7d relative to
// now. It returns the instant and the hours left.
func resetClock(spec string, now time.Time) (time.Time, float64, error) {
	if strings.HasPrefix(spec, "+") {
		d := strings.TrimPrefix(spec, "+")
		var dur time.Duration
		if w, e := windowDuration(d); e == nil {
			dur = w
		} else if p, e := time.ParseDuration(d); e == nil {
			dur = p
		} else {
			return time.Time{}, 0, fail(exitUsage, "invalid reset %q", spec)
		}
		return now.Add(dur), dur.Hours(), nil
	}
	t, e := time.Parse(time.RFC3339Nano, spec)
	if e != nil {
		return time.Time{}, 0, fail(exitUsage, "invalid reset %q", spec)
	}
	return t, t.Sub(now).Hours(), nil
}

// pace fills projected, urgency and reset hours for one pool view from its
// used share, window and reset. An explicit projected value wins over the
// paced projection; urgency always derives from the projection in use.
func (v *PoolView) pace(used, projected *float64, window string, left float64, now time.Time, resetsAt time.Time) {
	p := projected
	if p == nil && used != nil && !resetsAt.IsZero() {
		p = projectAtReset(used, window, resetsAt.Format(time.RFC3339Nano), now)
	}
	v.Projected = p
	v.Urgency, v.ResetHours = nil, nil
	// Sub-daily windows never rank a pool; an unlabeled window ranks (the
	// fixture's only window is its longest by definition).
	if w, e := windowDuration(window); e == nil && w < 24*time.Hour {
		return
	}
	if p != nil && left > 0 {
		u, h := (100-*p)/left, left
		v.Urgency, v.ResetHours = &u, &h
	}
}

// simPoolView reduces one input pool to the PoolView the selector ranks on,
// and reports the pool's reset instant (zero when the input gave none).
func simPoolView(p simPool, now time.Time) (PoolView, time.Time) {
	v := PoolView{Status: firstNonempty(p.Status, "ok"), Stale: p.Stale, Class: p.Class, Waste: p.Waste, Blocking: p.UsedPct}
	if v.Class == "" {
		v.Class = "spend"
	}
	var reset time.Time
	var left float64
	switch {
	case p.ResetsAt != "":
		if t, h, e := resetClock(p.ResetsAt, now); e == nil {
			reset, left = t, h
		}
	case p.ResetHours != nil:
		reset, left = now.Add(time.Duration(*p.ResetHours*float64(time.Hour))), *p.ResetHours
	}
	v.pace(p.UsedPct, p.Projected, p.Window, left, now, reset)
	return v, reset
}

// applySet rewrites (or creates) a pool entry and repaces it. A --set that
// omits reset keeps the instant the input recorded for the pool, whatever
// its window — including sub-daily windows that never rank by urgency.
func applySet(pools map[string]PoolView, windows map[string]string, resets map[string]time.Time, s simSet, now time.Time) {
	v := pools[s.pool]
	if v.Status == "" {
		v.Status, v.Class = "ok", "spend"
	}
	if s.used != nil {
		v.Blocking = s.used
	}
	window := firstNonempty(s.window, windows[s.pool], "7d")
	var reset time.Time
	var left float64
	if s.reset != "" {
		if t, h, e := resetClock(s.reset, now); e == nil {
			reset, left = t, h
		}
	} else if t, ok := resets[s.pool]; ok {
		reset, left = t, t.Sub(now).Hours()
	}
	projected := s.projected
	if projected == nil && reset.IsZero() {
		// Without a reset instant nothing can re-pace the pool: keep the
		// recorded projection rather than dropping it.
		projected = v.Projected
	}
	v.pace(v.Blocking, projected, window, left, now, reset)
	windows[s.pool] = window
	if !reset.IsZero() {
		resets[s.pool] = reset
	}
	pools[s.pool] = v
}

// simulate runs `runtug simulate`: rank a recorded selection input and print
// the decision record. It touches no runner, no quota CLI and no job store.
func (a *App) simulate(ctx context.Context, args []string) error {
	f := flags("simulate")
	selection := f.String("selection", "", "selection input JSON (fixture or job event)")
	nowFlag := f.String("now", "", "override the document's now (RFC3339)")
	var sets []string
	f.Func("set", "pool:used=N,reset=RFC3339|+Nh[,window=D][,projected=N] (repeatable)", func(v string) error {
		sets = append(sets, v)
		return nil
	})
	if e := f.Parse(args); e != nil {
		return fail(exitUsage, "%v", e)
	}
	if *selection == "" {
		return fail(exitUsage, "simulate needs --selection FILE")
	}
	b, e := os.ReadFile(expand(*selection))
	if e != nil {
		return fail(exitConfig, "selection input unreadable: %v", e)
	}
	var raw map[string]any
	if e := json.Unmarshal(b, &raw); e != nil {
		return fail(exitConfig, "invalid selection input JSON: %v", e)
	}
	// A job event carries its selection record under payload.selection.
	recorded := false
	if payload, ok := raw["payload"].(map[string]any); ok {
		if sel, ok := payload["selection"].(map[string]any); ok {
			raw = sel
			recorded = true
		} else {
			raw = payload
		}
	}
	// A bare recorded selection record, or a payload carrying its fields
	// directly, is recognised by its nested request.
	if _, ok := raw["requested"].(map[string]any); ok {
		recorded = true
	}
	var doc simDoc
	if recorded {
		// A recorded selection record carries fields the input document does
		// not; decode leniently, let `requested`/`at` fill what the record
		// nests, and rebuild a missing pools table from the candidate rows.
		if e := json.Unmarshal(mustJSON(raw), &doc); e != nil {
			return fail(exitConfig, "invalid selection input: %v", e)
		}
		var rec simSelRecord
		if e := json.Unmarshal(mustJSON(raw), &rec); e != nil {
			return fail(exitConfig, "invalid selection input: %v", e)
		}
		if doc.Now == "" {
			doc.Now = rec.At
		}
		if doc.Grade == "" {
			doc.Grade = rec.Requested.Grade
		}
		if doc.Role == "" {
			doc.Role = rec.Requested.Role
		}
		if doc.Effort == "" {
			doc.Effort = rec.Requested.Effort
		}
		doc.AllowEscalation = doc.AllowEscalation || rec.Requested.AllowEscalation
		seen := map[string]bool{}
		for _, p := range doc.Pools {
			seen[p.ID] = true
		}
		for _, rc := range rec.Candidates {
			if rc.Pool != "" && !seen[rc.Pool] && (rc.UsedPct != nil || rc.Projected != nil) {
				seen[rc.Pool] = true
				doc.Pools = append(doc.Pools, simPool{ID: rc.Pool, UsedPct: rc.UsedPct, Projected: rc.Projected})
			}
		}
	} else if e := decode(mustJSON(raw), &doc); e != nil {
		return fail(exitConfig, "invalid selection input: %v", e)
	}
	now := a.Now()
	if doc.Now != "" {
		if t, e := time.Parse(time.RFC3339Nano, doc.Now); e == nil {
			now = t
		} else {
			return fail(exitUsage, "invalid document now %q", doc.Now)
		}
	}
	if *nowFlag != "" {
		t, e := time.Parse(time.RFC3339Nano, *nowFlag)
		if e != nil {
			return fail(exitUsage, "invalid --now %q", *nowFlag)
		}
		now = t
	}
	policy, e := a.poolPolicy()
	if e != nil {
		return e
	}
	if len(doc.CreditPools) > 0 {
		policy.CreditPools = doc.CreditPools
	}
	if _, ok := gradeOrder[doc.Grade]; !ok {
		return fail(exitUsage, "selection input needs a readable grade, got %q", doc.Grade)
	}
	pools := map[string]PoolView{}
	windows := map[string]string{}
	resets := map[string]time.Time{}
	for _, p := range doc.Pools {
		if !validID(p.ID) {
			return fail(exitConfig, "invalid pool id %q", p.ID)
		}
		v, reset := simPoolView(p, now)
		pools[p.ID] = v
		windows[p.ID] = p.Window
		if !reset.IsZero() {
			resets[p.ID] = reset
		}
	}
	for _, s := range sets {
		set, e := parseSimSet(s)
		if e != nil {
			return e
		}
		applySet(pools, windows, resets, set, now)
	}
	p := &selPlan{policy: policy, order: slices.Clone(policy.Order), at: now}
	p.req = selReq{grade: doc.Grade, role: firstNonempty(doc.Role, "worker"), effort: doc.Effort, allowEscalation: doc.AllowEscalation, urgency: urgencyNormal, urgencySource: "simulation"}
	var pending []*selCand
	for _, sc := range doc.Candidates {
		row := CatalogRow{Profile: sc.Profile, Effort: sc.Effort, Grade: sc.Grade, ModelID: sc.ModelID, Pool: sc.Pool, Gate: sc.Gate, Score: sc.Score, RetiredAt: sc.RetiredAt}
		c := &selCand{row: row, seatName: firstNonempty(sc.Seat, sc.Profile), tier: -1}
		p.cands = append(p.cands, c)
		_, validGrade := gradeOrder[row.Grade]
		free := sc.Free || policy.isFree(row)
		switch {
		case row.RetiredAt != nil:
			c.reason = "retired"
		case row.Profile == "" || row.ModelID == "" || row.Pool == "":
			c.reason = "catalog_incomplete"
		case sc.FreeLimit:
			c.reason = "free_limit"
		case !validGrade:
			c.reason = "grade_invalid"
		case gradeOrder[row.Grade] < gradeOrder[p.req.grade]:
			c.reason = "grade_insufficient"
		case row.Gate != "default" && row.Gate != "escalation" && row.Gate != "consult_only":
			c.reason = "gate_unknown"
		case row.Gate != "default" && !p.req.allowEscalation:
			c.reason = "gate_class"
		case p.req.effort != "" && row.Effort != p.req.effort:
			c.reason = "effort_mismatch"
		case !validID(row.Pool):
			c.reason = "pool_invalid"
		}
		if v, ok := pools[row.Pool]; ok {
			c.blocking, c.projected, c.urgency, c.resetHrs = v.Blocking, v.Projected, v.Urgency, v.ResetHours
			// A free rung is excluded only by an observed free limit, never by
			// its shared pool's used or projected share.
			if c.reason == "" && !free && v.Projected != nil && *v.Projected >= *policy.ExcludeAt {
				c.reason = "projected_exhaust"
			}
		}
		if c.reason != "" {
			c.outcome = "dropped:" + c.reason
			continue
		}
		tierName, n := simTierOf(policy, row, free, pools)
		if n < 0 {
			c.reason = "quota_unknown"
			c.outcome = "dropped:quota_unknown"
			continue
		}
		c.tier, c.tierName = n, tierName
		pending = append(pending, c)
	}
	orderSelCands(pending)
	for i, c := range pending {
		if i == 0 {
			c.outcome = "chosen"
			p.chosen = c
		} else {
			c.outcome = "below_choice"
		}
	}
	rec := p.record()
	rec["mode"] = "simulate"
	if len(sets) > 0 {
		rec["overrides"] = sets
	}
	out, e := json.MarshalIndent(rec, "", "  ")
	if e != nil {
		return e
	}
	fmt.Fprintln(a.Out, string(out))
	if p.chosen == nil {
		return fail(exitSeatRefused, "simulation admitted no candidate")
	}
	return nil
}

// simTierOf is PoolPolicy.tierOf for simulate: the free flag on the input
// candidate (or the policy free set) decides the free tier, and every pool is
// expected in the input table — pools without an entry are quota_unknown.
func simTierOf(policy PoolPolicy, row CatalogRow, free bool, pools map[string]PoolView) (string, int) {
	if free {
		return "free", slices.Index(policy.Order, "free")
	}
	v, ok := pools[row.Pool]
	if !ok || v.Status != "ok" || v.Stale || v.Class != "spend" {
		return "", -1
	}
	tier := "ranked"
	if v.Projected != nil && *v.Projected < *policy.WasteBelow || v.Projected == nil && v.Waste != nil && *v.Waste {
		tier = "waste"
	}
	if slices.Contains(policy.CreditPools, row.Pool) {
		tier = "credit"
	}
	return tier, slices.Index(policy.Order, tier)
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
