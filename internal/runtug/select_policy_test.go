package runtug

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// pace is one pool's 7d account bucket as the quota CLI reports it: the used
// percentage now and the fraction of the window already elapsed. The selector
// projects it to used/elapsed at reset, the way pacelever paces a bucket.
type pace struct{ used, elapsed float64 }

// policyCatalog is a fixed bench catalog over the shipped seat policy
// (policy.json): a free SWE-2 bucket, two subscription pools with B..S+
// rungs and the credit pool.
var policyCatalog = []CatalogRow{
	{Profile: "devin-swe2-medium", Effort: "medium", Grade: "A", Pool: "devin", Gate: "default", ModelID: "swe-2-medium"},
	{Profile: "devin-swe2", Effort: "high", Grade: "A+", Pool: "devin", Gate: "default", ModelID: "swe-2"},
	{Profile: "sonnet", Effort: "medium", Grade: "B", Pool: "claude", Gate: "default", ModelID: "m"},
	{Profile: "sonnet", Effort: "high", Grade: "A", Pool: "claude", Gate: "default", ModelID: "m"},
	{Profile: "opus", Effort: "high", Grade: "S", Pool: "claude", Gate: "default", ModelID: "m"},
	{Profile: "opus", Effort: "max", Grade: "S+", Pool: "claude", Gate: "default", ModelID: "m"},
	{Profile: "codex-sol", Effort: "high", Grade: "A", Pool: "codex", Gate: "default", ModelID: "m"},
	{Profile: "codex-sol", Effort: "max", Grade: "S+", Pool: "codex", Gate: "default", ModelID: "m"},
	{Profile: "pi-sonnet55", Effort: "high", Grade: "A", Pool: "anthropic-api", Gate: "default", ModelID: "m"},
}

// paceStatus renders a scopefuel.v1 status document with one 7d account
// bucket per pool, generated at now.
func paceStatus(now time.Time, pools map[string]pace) string {
	var providers []map[string]any
	for id, p := range pools {
		week := 7 * 24 * time.Hour
		reset := now.Add(time.Duration((1 - p.elapsed) * float64(week)))
		providers = append(providers, map[string]any{
			"id": id, "status": "ok", "pool_class": "spend",
			"buckets": []map[string]any{{"window": "7d", "used_pct": p.used, "resets_at": reset.Format(time.RFC3339Nano), "scope": map[string]any{"kind": "account"}}},
		})
	}
	b, _ := json.Marshal(map[string]any{"schema": "scopefuel.v1", "generated_at": now.Format(time.RFC3339Nano), "providers": providers})
	return string(b)
}

// TestSelectionPolicyScenarios is the operator selection policy as a table:
// free first, then subscription quota that would be wasted by reset, then
// credit, then ranked; never a pool projected to run out; the lowest grade
// that fits the task, then the lower projection, then the smaller effort.
// Seats come from policy.json and the pool order and projection thresholds
// from the shipped pool-order policy.
func TestSelectionPolicyScenarios(t *testing.T) {
	// The credit pool has no automated balance reading yet (policy/quota-stop),
	// so it is unknown and dropped unless a row gives it one.
	roomy := map[string]pace{"devin": {10, 0.5}, "claude": {15, 0.5}, "codex": {20, 0.5}}
	with := func(over map[string]pace) map[string]pace {
		m := map[string]pace{}
		for k, v := range roomy {
			m[k] = v
		}
		for k, v := range over {
			m[k] = v
		}
		return m
	}
	exhausted := pace{100, 0.6}
	cases := []struct {
		situation string
		grade, t  string
		priority  string
		pools     map[string]pace
		want      string // profile@effort
		wantPool  string
		wantTier  string
	}{
		{"B task, SWE-2 free has room → SWE-2", "B", "T2", "3", roomy,
			"devin-swe2-medium@medium", "devin", "free"},
		{"urgent B task, SWE-2 free has room → still SWE-2 (free first, always)", "B", "T2", "1", roomy,
			"devin-swe2-medium@medium", "devin", "free"},
		{"B task, SWE-2 exhausted, claude 16% / codex 42% projected → claude B fit, not S+", "B", "T2", "3",
			with(map[string]pace{"devin": exhausted, "claude": {8, 0.5}, "codex": {21, 0.5}}),
			"sonnet@medium", "claude", "waste"},
		{"urgent B task, SWE-2 exhausted, claude 16% / codex 42% → claude B fit, not S+", "B", "T2", "1",
			with(map[string]pace{"devin": exhausted, "claude": {8, 0.5}, "codex": {21, 0.5}}),
			"sonnet@medium", "claude", "waste"},
		{"B task, all paid pools roomy, codex lower → lowest sufficient grade before lower projection", "B", "T2", "3",
			with(map[string]pace{"devin": exhausted, "claude": {30, 0.5}, "codex": {5, 0.5}}),
			"sonnet@medium", "claude", "waste"},
		{"A task, all paid pools roomy → A on the lower projection, not S+", "A", "T2", "3",
			with(map[string]pace{"devin": exhausted, "claude": {30, 0.5}, "codex": {5, 0.5}}),
			"codex-sol@high", "codex", "waste"},
		{"claude projected 120% at reset (30% used, quarter of window) → excluded; codex 80% chosen", "B", "T2", "3",
			with(map[string]pace{"devin": exhausted, "claude": {30, 0.25}, "codex": {40, 0.5}}),
			"codex-sol@high", "codex", "waste"},
		{"credit pool readable, every subscription projected ≥100% → credit pool", "B", "T2", "3",
			with(map[string]pace{"devin": exhausted, "claude": {60, 0.5}, "codex": {70, 0.5}, "anthropic-api": {10, 0.5}}),
			"pi-sonnet55@high", "anthropic-api", "credit"},
		{"credit pool readable, claude 16% → waste before credit", "B", "T2", "3",
			with(map[string]pace{"devin": exhausted, "claude": {8, 0.5}, "anthropic-api": {10, 0.5}}),
			"sonnet@medium", "claude", "waste"},
		{"T3 S task, all roomy → S-grade profile, not S+", "S", "T3", "3", roomy,
			"opus@high", "claude", "waste"},
		{"urgent T3 S task, codex lower → still the S fit on claude", "S", "T3", "1",
			with(map[string]pace{"claude": {30, 0.5}, "codex": {5, 0.5}}),
			"opus@high", "claude", "waste"},
	}
	policy, e := LoadPoolPolicy("")
	if e != nil {
		t.Fatal(e)
	}
	for _, tt := range cases {
		t.Run(tt.situation, func(t *testing.T) {
			present := []string{"devin", "claude", "codex", "anthropic-api"}
			a, s, q := selFixture(t, tt.grade, policyCatalog, nil, present)
			a.Config.Local["pi_anthropic"] = true // the credit seat's capability
			pools, e := CLIQuota{Runner: &fakeRunner{Do: func(Command) Result { return Result{Out: paceStatus(a.Now(), tt.pools)} }}, Binary: "quota"}.Pools(context.Background())
			if e != nil {
				t.Fatal(e)
			}
			q.PoolViews = pools
			// The quota gate itself refuses a pool that is used up now.
			q.GateDo = func(r QuotaRequest) (string, error) {
				pool := map[string]string{"devin-swe2": "devin", "devin-swe2-medium": "devin", "sonnet": "claude", "opus": "claude", "codex-sol": "codex", "pi-sonnet55": "anthropic-api"}[r.Profile]
				if tt.pools[pool].used >= 100 {
					return "", fail(4, "pool %s exhausted", pool)
				}
				return fmt.Sprintf("pool=%s profile=%s\n", pool, r.Profile), nil
			}
			base := a.Runner.(*fakeRunner).Do
			a.Runner.(*fakeRunner).Do = func(c Command) Result {
				if c.Name == "handoffkeep" && strings.HasPrefix(strings.Join(c.Args, " "), "tasks show") {
					return Result{Out: fmt.Sprintf(`{"id":42,"title":"work (grade %s, %s)","state":"ready","priority":%s,"refs":{}}`, tt.grade, tt.t, tt.priority)}
				}
				return base(c)
			}
			s.T = tt.t
			sel, e := a.selectProfile(context.Background(), s)
			if e != nil {
				t.Fatal(e)
			}
			got := sel.chosen.row.Profile + "@" + sel.chosen.row.Effort
			if got != tt.want || sel.chosen.row.Pool != tt.wantPool || sel.chosen.tierName != tt.wantTier {
				t.Fatalf("want %s on %s (%s), got %s grade %s on %s (%s)", tt.want, tt.wantPool, tt.wantTier, got, sel.chosen.row.Grade, sel.chosen.row.Pool, sel.chosen.tierName)
			}
			rec := sel.record()
			if tiers := strings.Join(rec["tiers"].([]string), ","); tiers != strings.Join(policy.Order, ",") {
				t.Fatalf("tier order must be the policy order %v whatever the urgency: %s", policy.Order, tiers)
			}
		})
	}
}
