package runtug

import (
	"context"
	"strings"
	"testing"
)

// wasteCase reproduces the 2026-10-31 decision: claude at 31% used of a
// 7-day window resetting in ~9.4 h (projected 31.8%) against codex at 13%
// resetting in ~95 h (projected 30%). The free rung is held (its gate
// refuses), so the choice is between the two waste-tier pools.
func wasteRows() []CatalogRow {
	return []CatalogRow{
		{Profile: "devin-swe2", Effort: "high", Grade: "A", Pool: "devin", Gate: "default", ModelID: "swe-2"},
		{Profile: "sonnet", Effort: "high", Grade: "A", Pool: "claude", Gate: "default", ModelID: "m"},
		{Profile: "codex-luna", Effort: "high", Grade: "A", Pool: "codex", Gate: "default", ModelID: "m"},
	}
}

func wastePools(claudeUsed, claudeProj, claudeUrg, claudeReset, codexUsed, codexProj, codexUrg, codexReset float64, withUrgency bool) map[string]PoolView {
	claude := PoolView{Status: "ok", Class: "spend", Blocking: ptrF(claudeUsed), Projected: ptrF(claudeProj)}
	codex := PoolView{Status: "ok", Class: "spend", Blocking: ptrF(codexUsed), Projected: ptrF(codexProj)}
	if withUrgency {
		claude.Urgency, claude.ResetHours = ptrF(claudeUrg), ptrF(claudeReset)
		codex.Urgency, codex.ResetHours = ptrF(codexUrg), ptrF(codexReset)
	}
	return map[string]PoolView{"devin": {Status: "ok", Class: "spend", Blocking: ptrF(4)}, "claude": claude, "codex": codex}
}

// wasteCase20261031 is the recorded 2026-10-31 reading: claude 31% used,
// projected 31.8% at a reset ~9.4 h out (~68% of the week lost); codex 13%
// used, projected 30% with ~95 h left.
func wastePools20261031() map[string]PoolView {
	return wastePools(31, 31.8, 7.26, 9.4, 13, 30, 0.74, 95, true)
}

func wasteFixture(t *testing.T, pools map[string]PoolView) (*App, Spawn) {
	t.Helper()
	a, s, q := selFixture(t, "A", wasteRows(), pools, []string{"devin", "claude", "codex"})
	q.GateDo = gatePools(map[string]string{"sonnet": "claude", "codex-luna": "codex"}) // devin-swe2 held
	return a, s
}

func TestSelectWasteUrgency(t *testing.T) {
	cases := []struct {
		name  string
		pools map[string]PoolView
		want  string
	}{
		// claude loses ~68% of its week in 9.4 h; codex has 4 days left.
		{"2026-10-31: sooner reset wins", wastePools20261031(), "sonnet"},
		// After claude's reset it reads 0% used with +168 h to the next one.
		{"after claude reset codex wins", wastePools(0, 0, 0.60, 168, 13, 30, 0.74, 95, true), "codex-luna"},
		// Equal urgency falls back to the projected-at-reset order.
		{"equal urgency uses projected order", wastePools(31, 31.8, 1, 9.4, 13, 30, 1, 95, true), "codex-luna"},
		// Unknown resets rank by projected use at reset.
		{"unknown reset uses projected order", wastePools(31, 31.8, 0, 0, 13, 30, 0, 0, false), "codex-luna"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			a, s := wasteFixture(t, tt.pools)
			sel, e := a.selectProfile(context.Background(), s)
			if e != nil {
				t.Fatal(e)
			}
			if sel.chosen.seatName != tt.want {
				t.Fatalf("want %s, got %s", tt.want, sel.chosen.seatName)
			}
		})
	}
}

func TestSelectRecordCarriesWasteUrgency(t *testing.T) {
	a, s := wasteFixture(t, wastePools20261031())
	sel, e := a.selectProfile(context.Background(), s)
	if e != nil {
		t.Fatal(e)
	}
	rec := sel.record()
	if rule := rec["rule"].(string); !strings.Contains(rule, "waste urgency") {
		t.Fatalf("rule must describe the waste-urgency order: %s", rule)
	}
	row := candOutcome(rec, "sonnet", "high")
	if row["waste_urgency"] != 7.26 || row["waste_order"] != "reset" || row["reset_hours"] != 9.4 {
		t.Fatalf("ranked candidate records its urgency and inputs: %v", row)
	}
	if got := candOutcome(rec, "codex-luna", "high")["waste_urgency"]; got != 0.74 {
		t.Fatalf("codex urgency: %v", got)
	}
	chosen := rec["chosen"].(map[string]any)
	if chosen["waste_urgency"] != 7.26 {
		t.Fatalf("chosen records its urgency: %v", chosen)
	}
}

func TestSelectRecordStatesProjectedFallback(t *testing.T) {
	a, s := wasteFixture(t, wastePools(31, 31.8, 0, 0, 13, 30, 0, 0, false))
	sel, e := a.selectProfile(context.Background(), s)
	if e != nil {
		t.Fatal(e)
	}
	if got := candOutcome(sel.record(), "sonnet", "high")["waste_order"]; got != "projected_fallback" {
		t.Fatalf("unknown reset must record the projected fallback: %v", got)
	}
}

// A free rung sharing a pool with paid rungs is excluded only by an observed
// free limit, never by the pool's used or projected share.
func TestSelectFreeRungIgnoresPoolProjection(t *testing.T) {
	rows := []CatalogRow{
		{Profile: "devin-swe2", Effort: "high", Grade: "A", Pool: "devin", Gate: "default", ModelID: "swe-2"},
		{Profile: "devin-ds41", Effort: "high", Grade: "A", Pool: "devin", Gate: "default", ModelID: "m"},
		{Profile: "sonnet", Effort: "high", Grade: "A", Pool: "claude", Gate: "default", ModelID: "m"},
	}
	pools := map[string]PoolView{
		"devin":  {Status: "ok", Class: "spend", Blocking: ptrF(70), Projected: ptrF(106)},
		"claude": {Status: "ok", Class: "spend", Blocking: ptrF(31), Projected: ptrF(31.8)},
	}
	a, s, q := selFixture(t, "A", rows, pools, []string{"devin", "claude"})
	q.GateDo = func(r QuotaRequest) (string, error) {
		if firstNonempty(r.GateProfile, r.Profile) == "devin-swe2" {
			return "pool=devin profile=devin-swe2 gate_basis=free-bucket:devin\n", nil
		}
		return gatePools(map[string]string{"devin-ds41": "devin", "sonnet": "claude"})(r)
	}
	sel, e := a.selectProfile(context.Background(), s)
	if e != nil {
		t.Fatal(e)
	}
	if sel.chosen.seatName != "devin-swe2" {
		t.Fatalf("a projected 106%% pool must not exclude its free rung: %s", sel.chosen.seatName)
	}
	if got := candOutcome(sel.record(), "devin-ds41", "high")["outcome"]; got != "dropped:projected_exhaust" {
		t.Fatalf("paid rungs of the same pool are still refused by projection: %v", got)
	}
}

func TestSelectFreeRungRefusedByObservedLimit(t *testing.T) {
	rows := wasteRows()
	pools := map[string]PoolView{
		"devin":  {Status: "ok", Class: "spend", Blocking: ptrF(70), Projected: ptrF(106)},
		"claude": {Status: "ok", Class: "spend", Blocking: ptrF(31), Projected: ptrF(31.8), Urgency: ptrF(7.26), ResetHours: ptrF(9.4)},
		"codex":  {Status: "ok", Class: "spend", Blocking: ptrF(13), Projected: ptrF(30), Urgency: ptrF(0.74), ResetHours: ptrF(95)},
	}
	a, s, q := selFixture(t, "A", rows, pools, []string{"devin", "claude", "codex"})
	q.GateDo = gatePools(map[string]string{"sonnet": "claude", "codex-luna": "codex"}) // devin-swe2 refused
	sel, e := a.selectProfile(context.Background(), s)
	if e != nil {
		t.Fatal(e)
	}
	if got := candOutcome(sel.record(), "devin-swe2", "high")["outcome"]; got != "dropped:gate_refused" {
		t.Fatalf("an observed free limit refuses the free rung: %v", got)
	}
	if sel.chosen.seatName != "sonnet" {
		t.Fatalf("the next ranked rung is chosen: %s", sel.chosen.seatName)
	}
}
