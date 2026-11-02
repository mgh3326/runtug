package runtug

import (
	"context"
	"encoding/json"
	"math"
	"slices"
	"strings"
	"testing"
	"time"
)

// creditFixture offers the omp-opus credit seat (anthropic-api) against the
// sonnet subscription seat (claude), both admitted by their gates.
func creditFixture(t *testing.T, credit *CreditBalance, claude PoolView) *selPlan {
	t.Helper()
	rows := []CatalogRow{ompRow("omp-opus", "anthropic/claude-opus-5-5", "anthropic-api"), ompRow("sonnet", "claude-sonnet-5-5", "claude")}
	pools := map[string]PoolView{"anthropic-api": {Status: "ok", Class: "spend", Credit: credit}, "claude": claude}
	a, s, q := selFixture(t, "A", rows, pools, []string{"anthropic-api", "claude"})
	serveOMPRouting(t, a)
	q.GateDo = gatePools(map[string]string{"omp-opus": "anthropic-api", "sonnet": "claude"})
	sel, err := a.selectProfile(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	return sel
}

// Acceptance 1: a credit balance expiring in 10 days that the observed pace
// will not spend ranks in tier waste and beats a subscription pool at waste
// risk with a lower urgency; a subscription with a higher urgency still wins.
func TestCreditExpiringRanksAsWaste(t *testing.T) {
	expiring := &CreditBalance{LeftPct: 80, BalanceUSD: ptrF(160), HoursLeft: 240, PacePctH: ptrF(0.1)} // 24% spent by expiry, 56% lost
	cases := []struct {
		name   string
		claude PoolView
		want   string
	}{
		{"subscription with lower urgency loses", riskPool(70, 168), "omp-opus"}, // 30/168 = 0.18 < 0.33
		{"subscription with higher urgency wins", riskPool(32.6, 8), "sonnet"},   // 67.4/8 = 8.4 > 0.33
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			sel := creditFixture(t, expiring, tt.claude)
			if sel.chosen.seatName != tt.want {
				t.Fatalf("want %s, got %s", tt.want, sel.chosen.seatName)
			}
			row := candOutcome(sel.record(), "omp-opus", "high")
			if row["tier"] != "waste" || row["waste_risk"] != true || row["waste_reason"] != "credit_expiring" {
				t.Fatalf("expiring credit must rank as waste risk: %v", row)
			}
			if u := row["waste_urgency"].(float64); math.Abs(u-80.0/240) > 1e-9 {
				t.Fatalf("waste urgency is balance per hour to expiry: %v", u)
			}
			if c := row["credit"].(map[string]any); c["days_left"] != 10.0 || c["balance_pct"] != 80.0 || c["balance_usd"] != 160.0 {
				t.Fatalf("record states days left and balance: %v", c)
			}
			if u := row["unused_at_reset_pct"].(float64); math.Abs(u-56) > 1e-9 {
				t.Fatalf("unused at expiry is the balance less the paced spend: %v", u)
			}
		})
	}
	sel := creditFixture(t, expiring, riskPool(70, 168))
	if got := creditNote(sel.chosen); got != " credit_expiring days_left=10.0 balance_pct=80.0 balance_usd=160.00" {
		t.Fatalf("plan text: %q", got)
	}
}

// Acceptance 2: a credit pool with no recorded expiry, or one the observed
// pace spends before it expires, stays in tier credit behind the subscription.
func TestCreditNotExpiringStaysCredit(t *testing.T) {
	cases := []struct {
		name   string
		credit *CreditBalance
	}{
		{"no balance or expiry recorded", nil},
		{"spendable in time", &CreditBalance{LeftPct: 80, HoursLeft: 240, PacePctH: ptrF(0.5)}}, // 120% spent by expiry
		{"already expired", &CreditBalance{LeftPct: 80, HoursLeft: -1}},
		{"exhausted", &CreditBalance{LeftPct: 0, HoursLeft: 240}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			sel := creditFixture(t, tt.credit, riskPool(70, 168))
			if sel.chosen.seatName != "sonnet" {
				t.Fatalf("credit must rank last: got %s", sel.chosen.seatName)
			}
			row := candOutcome(sel.record(), "omp-opus", "high")
			if row["tier"] != "credit" || row["waste_risk"] != false || row["waste_reason"] != nil {
				t.Fatalf("non-expiring credit stays tier credit: %v", row)
			}
			if creditNote(sel.ranked[slices.IndexFunc(sel.ranked, func(c *selCand) bool { return c.row.Profile == "omp-opus" })]) != "" {
				t.Fatal("no credit_expiring note for a credit-tier pool")
			}
		})
	}
}

// The balance comes from pacelever's manual store: the latest credit entry,
// its expiry, and a pace from the entry it supersedes.
func TestCLIQuotaPoolsReadsCreditBalance(t *testing.T) {
	now := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	status, _ := json.Marshal(map[string]any{"schema": "scopefuel.v1", "generated_at": now.Format(time.RFC3339Nano), "providers": []map[string]any{
		{"id": "anthropic-api", "status": "ok", "pool_class": "spend", "buckets": []map[string]any{{"window": "credit", "used_pct": 25, "scope": map[string]any{"kind": "account"}}}},
	}})
	older := "a1"
	store, _ := json.Marshal(map[string]any{
		"schema": "pacelever.manual.v1",
		"latest": map[string]string{"anthropic-api:credit": "a2", "openai-api:credit": "o1", "claude:7d": "c1"},
		"history": []map[string]any{
			{"manual_observation_id": "a1", "event": "set", "pool": "anthropic-api", "window": "credit", "used_pct": 20, "measured_at": now.Add(-48 * time.Hour).Format(time.RFC3339Nano), "expires_at": "2026-11-16T00:00:00Z",
				"credit": map[string]any{"remaining_usd": 160, "total_usd": 200, "expires_on": "2026-11-16T00:00:00Z"}},
			{"manual_observation_id": "a2", "event": "set", "pool": "anthropic-api", "window": "credit", "used_pct": 25, "measured_at": now.Add(-24 * time.Hour).Format(time.RFC3339Nano), "expires_at": "2026-11-16T00:00:00Z", "supersedes_ref": older,
				"credit": map[string]any{"remaining_usd": 150, "total_usd": 200, "expires_on": "2026-11-16T00:00:00Z"}},
			// A shared entry: no dollars, no lineage.
			{"manual_observation_id": "o1", "event": "set", "pool": "openai-api", "window": "credit", "used_pct": 5, "measured_at": now.Add(-time.Hour).Format(time.RFC3339Nano), "expires_at": "2026-11-18T00:00:00Z"},
			{"manual_observation_id": "c1", "event": "set", "pool": "claude", "window": "7d", "used_pct": 30, "measured_at": now.Format(time.RFC3339Nano), "expires_at": now.Add(time.Hour).Format(time.RFC3339Nano)},
		},
	})
	run := func(manualOK bool) map[string]PoolView {
		q := CLIQuota{Binary: "pacelever", Runner: &fakeRunner{Do: func(c Command) Result {
			if strings.Join(c.Args, " ") == "manual list" {
				if !manualOK {
					return Result{Code: 2, Err: "shared manual observations unavailable"}
				}
				return Result{Out: string(store)}
			}
			return Result{Out: string(status)}
		}}}
		pools, err := q.Pools(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return pools
	}
	pools := run(true)
	a := pools["anthropic-api"].Credit
	if a == nil || a.LeftPct != 75 || *a.BalanceUSD != 150 || a.HoursLeft != 14*24 || a.PacePctH == nil || math.Abs(*a.PacePctH-5.0/24) > 1e-9 {
		t.Fatalf("anthropic-api balance, expiry and pace: %+v", a)
	}
	// 5% a day for 14 days spends 70% of the 75% left: 5% expires unspent.
	if ok, _ := a.expiring(); !ok {
		t.Fatal("a balance the pace leaves partly unspent is expiring")
	}
	o := pools["openai-api"].Credit
	if o == nil || o.LeftPct != 95 || o.BalanceUSD != nil || o.HoursLeft != 16*24 || o.PacePctH != nil {
		t.Fatalf("shared openai-api entry: %+v", o)
	}
	if ok, u := o.expiring(); !ok || math.Abs(u-95.0/(16*24)) > 1e-9 {
		t.Fatalf("unknown pace counts as no spend: ok=%t urgency=%v", ok, u)
	}
	if pools["claude"].Credit != nil {
		t.Fatal("only credit-window entries carry a balance")
	}
	if pools := run(false); pools["anthropic-api"].Credit != nil || pools["anthropic-api"].Status != "ok" {
		t.Fatalf("an unreadable store leaves the status view without a balance: %+v", pools["anthropic-api"])
	}
}
