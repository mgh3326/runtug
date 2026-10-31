package runtug

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Role routing (hk document policy/model-routing, operator 2026-11-01).
//
// A role table constrains the candidate set before policy/quota-stop orders
// the pools: only the role's "allowed" catalog profiles are candidates, grade
// and gate still apply, and when every allowed profile is exhausted the
// role's on_exhaustion decides — wait or escalate refuse, downgrade_to retries
// with only the listed profiles. Selection never falls back to a profile
// outside the role.
//
// The table is read from hk at selection time and the last good copy is
// cached under the runtug state dir. hk unreachable: the cache is used
// (policy_source=cache@vN). No cache: lead and verifier are refused, every
// other role proceeds unfiltered (policy_source=builtin_default).

const routingDocKey = "policy/model-routing"

// RoutingPolicy is the machine-readable v1 block of policy/model-routing.
// Unknown fields are ignored so the desk can extend the document.
type RoutingPolicy struct {
	Version int                    `json:"version"`
	Roles   map[string]RoutingRule `json:"roles"`
	RoleMap map[string]string      `json:"runtug_role_map"`
}

// RoutingRule is one routing role's entry. Allowed names catalog profiles
// (pacelever launch profiles); "*" admits any profile.
type RoutingRule struct {
	Allowed      []string `json:"allowed"`
	Effort       string   `json:"effort"`
	OnExhaustion string   `json:"on_exhaustion"`
	DowngradeTo  []string `json:"downgrade_to,omitempty"`
	FamilyRule   string   `json:"family_rule,omitempty"`
	Fixed        bool     `json:"fixed,omitempty"`
}

// routingRoles are the values --routing-role accepts.
var routingRoles = []string{"lead", "implementer", "explorer", "verifier", "reviewer", "judge"}

// routingStrictRoles are refused when neither hk nor a cached copy can say
// which profiles they may run on.
var routingStrictRoles = []string{"lead", "verifier"}

// builtinRoleMap maps seat roles to routing roles when no table is readable,
// so a builder is still known to be a lead (and refused) without one.
var builtinRoleMap = map[string]string{"builder": "lead", "worker": "implementer", "tester": "verifier", "reviewer": "reviewer", "counsel": "lead"}

const (
	exhaustWait      = "wait"
	exhaustEscalate  = "escalate"
	exhaustDowngrade = "downgrade_to"
)

// Policy sources stamped on the selection record.
const (
	routingSourceHK      = "hk"
	routingSourceBuiltin = "builtin_default"
)

func (p RoutingPolicy) validate() error {
	if p.Version != 1 {
		return fmt.Errorf("unsupported routing policy version %d", p.Version)
	}
	if len(p.Roles) == 0 {
		return fmt.Errorf("routing policy has no roles")
	}
	for name, r := range p.Roles {
		if len(r.Allowed) == 0 {
			return fmt.Errorf("routing role %s allows nothing", name)
		}
		switch r.OnExhaustion {
		case exhaustWait, exhaustEscalate:
			if len(r.DowngradeTo) > 0 {
				return fmt.Errorf("routing role %s lists downgrade_to without on_exhaustion downgrade_to", name)
			}
		case exhaustDowngrade:
			if len(r.DowngradeTo) == 0 || slices.Contains(r.DowngradeTo, "*") {
				return fmt.Errorf("routing role %s needs explicit downgrade_to profiles", name)
			}
		default:
			return fmt.Errorf("routing role %s has unknown on_exhaustion %q", name, r.OnExhaustion)
		}
	}
	for seat, role := range p.RoleMap {
		if _, ok := p.Roles[role]; !ok {
			return fmt.Errorf("runtug_role_map maps %s to unknown role %s", seat, role)
		}
	}
	return nil
}

// parseRoutingDoc extracts and validates the first ```json block of the
// document body.
func parseRoutingDoc(body string) (RoutingPolicy, error) {
	var p RoutingPolicy
	_, rest, ok := strings.Cut(body, "```json")
	if !ok {
		return p, fmt.Errorf("no json block")
	}
	block, _, ok := strings.Cut(rest, "```")
	if !ok {
		return p, fmt.Errorf("unterminated json block")
	}
	return parseRoutingJSON([]byte(block))
}
func parseRoutingJSON(b []byte) (RoutingPolicy, error) {
	var p RoutingPolicy
	if e := uniqueJSON(b); e != nil {
		return p, e
	}
	if e := json.Unmarshal(b, &p); e != nil {
		return p, e
	}
	return p, p.validate()
}

// stateDir is runtug's local state directory ($XDG_STATE_HOME/runtug, else
// ~/.local/state/runtug); New sets StateDir from the environment.
func (a *App) stateDir() string {
	if a.StateDir != "" {
		return a.StateDir
	}
	return filepath.Join(a.Home, ".local", "state", "runtug")
}
func (a *App) routingCachePath() string { return filepath.Join(a.stateDir(), "model-routing.json") }

// routingCache is the on-disk last good copy of the policy.
type routingCache struct {
	Version   int             `json:"version"`
	FetchedAt string          `json:"fetched_at"`
	Policy    json.RawMessage `json:"policy"`
}

func (a *App) readRoutingCache() (RoutingPolicy, error) {
	b, e := os.ReadFile(a.routingCachePath())
	if e != nil {
		return RoutingPolicy{}, e
	}
	var c routingCache
	if e = json.Unmarshal(b, &c); e != nil {
		return RoutingPolicy{}, e
	}
	p, e := parseRoutingJSON(c.Policy)
	if e == nil && p.Version != c.Version {
		e = fmt.Errorf("cache version %d does not match its policy v%d", c.Version, p.Version)
	}
	return p, e
}

// writeRoutingCache replaces the cache atomically when the policy changed.
func (a *App) writeRoutingCache(p RoutingPolicy) error {
	policy, e := json.Marshal(p)
	if e != nil {
		return e
	}
	path := a.routingCachePath()
	if old, e := os.ReadFile(path); e == nil {
		var c routingCache
		if json.Unmarshal(old, &c) == nil && c.Version == p.Version && bytes.Equal(c.Policy, policy) {
			return nil
		}
	}
	b, e := json.MarshalIndent(routingCache{Version: p.Version, FetchedAt: a.Now().UTC().Format(time.RFC3339), Policy: policy}, "", "  ")
	if e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".model-routing-*.json")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if _, e = f.Write(append(b, '\n')); e == nil {
		e = f.Close()
	} else {
		f.Close()
	}
	if e != nil {
		return e
	}
	return os.Rename(f.Name(), path)
}

// routingPolicy reads the table from hk, refreshing the cache, else falls
// back to the cache. The source is "hk" or "cache@v<N>".
func (a *App) routingPolicy(ctx context.Context) (RoutingPolicy, string, error) {
	var why string
	r := a.call(ctx, "handoffkeep", "doc", "get", routingDocKey)
	if r.Code == 0 {
		var doc struct {
			Body string `json:"body"`
		}
		e := json.Unmarshal([]byte(r.Out), &doc)
		var p RoutingPolicy
		if e == nil {
			p, e = parseRoutingDoc(doc.Body)
		}
		if e == nil {
			if e := a.writeRoutingCache(p); e != nil {
				a.warn("routing policy cache not written: %v", e)
			}
			return p, routingSourceHK, nil
		}
		why = "unreadable " + routingDocKey + ": " + e.Error()
	} else {
		why = fmt.Sprintf("hk doc get %s exit %d: %s", routingDocKey, r.Code, clip(strings.TrimSpace(r.Err), 200))
	}
	p, e := a.readRoutingCache()
	if e != nil {
		return p, "", fmt.Errorf("%s; no usable cache (%v)", why, e)
	}
	a.warn("%s; using the cached routing policy v%d", why, p.Version)
	return p, fmt.Sprintf("cache@v%d", p.Version), nil
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// routing is the role decision applied to one selection. A nil *routing or
// a nil allowed list filters nothing.
type routing struct {
	role    string // routing role; "" when the seat role is unmapped
	version int    // table version; 0 for builtin_default
	source  string // hk, cache@vN or builtin_default
	rule    *RoutingRule
	allowed []string // nil = any profile
	// downgradedFrom names the role whose exhaustion led to this downgrade
	// selection.
	downgradedFrom string
}

func (r *routing) admits(profile string) bool {
	return r == nil || r.allowed == nil || slices.Contains(r.allowed, profile)
}

// routeFor resolves the routing role for a seat role (or the explicit
// override) against the policy table.
func (a *App) routeFor(ctx context.Context, seatRole, override string) (*routing, error) {
	if override != "" && !slices.Contains(routingRoles, override) {
		return nil, fail(exitUsage, "--routing-role must be one of %s", strings.Join(routingRoles, "|"))
	}
	p, source, e := a.routingPolicy(ctx)
	if e != nil {
		role := firstNonempty(override, builtinRoleMap[seatRole])
		if slices.Contains(routingStrictRoles, role) {
			return nil, fail(exitConfig, "routing role %s refused: %s unavailable (%v)", role, routingDocKey, e)
		}
		a.warn("%s unavailable (%v); %s proceeds unfiltered (builtin_default)", routingDocKey, e, firstNonempty(role, seatRole))
		return &routing{role: role, source: routingSourceBuiltin}, nil
	}
	role := firstNonempty(override, p.RoleMap[seatRole])
	r := &routing{role: role, version: p.Version, source: source}
	if role == "" {
		return r, nil
	}
	rule, ok := p.Roles[role]
	if !ok {
		return nil, fail(exitConfig, "%s v%d has no role %s", routingDocKey, p.Version, role)
	}
	r.rule = &rule
	if !slices.Contains(rule.Allowed, "*") {
		r.allowed = slices.Clone(rule.Allowed)
	}
	return r, nil
}

// effort is the role's starting effort, else the chosen rung's.
func (r *routing) effort(rung string) string {
	if r != nil && r.rule != nil && r.rule.Effort != "" {
		return r.rule.Effort
	}
	return rung
}

// record renders the routing stamp carried in the selection record.
func (r *routing) record(refusal string) map[string]any {
	m := map[string]any{"routing_role": r.role, "routing_version": r.version, "policy_source": r.source}
	if r.allowed != nil {
		m["allowed"] = r.allowed
	} else if r.rule != nil {
		m["allowed"] = []string{"*"}
	}
	if r.rule != nil {
		m["on_exhaustion"] = r.rule.OnExhaustion
		if r.rule.Effort != "" {
			m["effort"] = r.rule.Effort
		}
	}
	if r.downgradedFrom != "" {
		m["downgraded_from"] = r.downgradedFrom
	}
	if refusal != "" {
		m["refused"] = refusal
	}
	return m
}

// quotaDrop reports whether a candidate's drop reason is quota exhaustion
// (pool projection, an exhausted window, unreadable status, no presence or a
// gate refusal) rather than a static mismatch (grade, gate class, seat).
func quotaDrop(reason string) bool {
	switch reason {
	case "projected_exhaust", "window_exhausted", "quota_unknown", "no_presence":
		return true
	}
	return strings.HasPrefix(reason, "gate_refused")
}

// Refusal reasons a role selection can end with.
const (
	reasonRoleNoCandidate  = "role_no_candidate"
	reasonRoleExhaustWait  = "role_exhausted_wait"
	reasonRoleExhaustEscal = "role_exhausted_escalate"
)

// roleExhausted handles a routed selection that admitted nothing. With no
// in-role candidate left after the static filters it refuses
// role_no_candidate; otherwise every in-role candidate was quota-exhausted
// and the role's on_exhaustion decides. A downgrade retries selection once
// with only the downgrade_to profiles.
func (a *App) roleExhausted(ctx context.Context, s Spawn, req selReq, p *selPlan) (*selPlan, error) {
	rt := req.route
	var earliest *float64
	exhausted := 0
	for _, c := range p.cands {
		if !quotaDrop(c.reason) {
			continue
		}
		exhausted++
		if c.resetHrs != nil && (earliest == nil || *c.resetHrs < *earliest) {
			earliest = c.resetHrs
		}
	}
	allowed := "*"
	if rt.allowed != nil {
		allowed = strings.Join(rt.allowed, ",")
	}
	if exhausted == 0 {
		p.routeRefusal = reasonRoleNoCandidate
		return p, fail(exitSeatRefused, "%s: no %s profile (%s) passes grade %s, gate and seat rules", reasonRoleNoCandidate, rt.role, allowed, req.grade)
	}
	reset := "unknown"
	if earliest != nil {
		reset = fmt.Sprintf("%s (in %.1fh)", p.at.Add(time.Duration(*earliest*float64(time.Hour))).UTC().Format(time.RFC3339), *earliest)
	}
	if rt.downgradedFrom != "" {
		p.routeRefusal = reasonRoleExhaustWait
		return p, fail(exitRoleWait, "%s: role %s and its downgrade_to profiles (%s) are exhausted; earliest reset %s", reasonRoleExhaustWait, rt.downgradedFrom, allowed, reset)
	}
	switch rt.rule.OnExhaustion {
	case exhaustDowngrade:
		d := *rt
		d.allowed, d.downgradedFrom = slices.Clone(rt.rule.DowngradeTo), rt.role
		next := req
		next.route = &d
		q, e := a.selectFor(ctx, s, next)
		if q != nil {
			q.before = p
		}
		return q, e
	case exhaustEscalate:
		p.routeRefusal = reasonRoleExhaustEscal
		return p, fail(exitRoleEscalate, "%s: every %s profile (%s) is exhausted; ask the parent lane", reasonRoleExhaustEscal, rt.role, allowed)
	default:
		p.routeRefusal = reasonRoleExhaustWait
		return p, fail(exitRoleWait, "%s: every %s profile (%s) is exhausted; earliest reset %s", reasonRoleExhaustWait, rt.role, allowed, reset)
	}
}
