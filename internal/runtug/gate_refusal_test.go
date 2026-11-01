package runtug

import (
	"context"
	"encoding/json"
	"testing"
)

type refusalQuota struct {
	*fakeQuota
	gate CLIQuota
}

func (q refusalQuota) Gate(ctx context.Context, req QuotaRequest) (string, error) {
	return q.gate.Gate(ctx, req)
}

func TestRawGateExitThreeUsesCodeInsteadOfProse(t *testing.T) {
	for _, message := range []string{"window_exhausted", "weekly limit reached", "사용량 한도 초과"} {
		t.Run(message, func(t *testing.T) {
			a, s, q, src := accountLeadFixture(t)
			q.CatalogRows = q.CatalogRows[1:2]
			src.presence = src.presence[1:2]
			q.GateDo = func(QuotaRequest) (string, error) { return "", fail(3, "%s", message) }
			sel, err := a.selectProfile(context.Background(), s)
			if exitCode(err) != exitRoleWait || sel.outcome != outcomeQuotaExhausted {
				t.Fatalf("gate prose changed exhaustion: %v/%s", err, sel.outcome)
			}
		})
	}
}

func TestStructuredGateRefusalsAreIndependentOfMessages(t *testing.T) {
	for _, tc := range []struct {
		name, outcome, reasonCode, message, want string
		exit                                     int
		roleDenied                               bool
	}{
		{"typed exhaustion", outcomeQuotaExhausted, "", "weekly limit reached", outcomeQuotaExhausted, exitRoleWait, false},
		{"reason code", "", "window_exhausted", "사용량 한도 초과", outcomeQuotaExhausted, exitRoleWait, false},
		{"typed policy", outcomeNoRoleCandidate, "", "quota exhausted is not the reason", outcomeNoRoleCandidate, exitSeatRefused, false},
		{"role flag", "", "", "quota exhausted is not the reason", outcomeNoRoleCandidate, exitSeatRefused, true},
		{"ambiguous measured block", "", "", "weekly limit reached", outcomeQuotaUnknown, exitQuotaUnknown, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, s, q, src := accountLeadFixture(t)
			q.CatalogRows = q.CatalogRows[1:2]
			src.presence = src.presence[1:2]
			calls := 0
			gateRunner := &fakeRunner{Do: func(c Command) Result {
				calls++
				d := map[string]any{"schema": "scopefuel.gate.v1", "provider_id": "claude", "account_key": testClaudeB, "ok": false, "exit_code": 3, "reason": tc.message, "used_pct": 95, "role_denied": tc.roleDenied}
				if tc.outcome != "" {
					d["outcome_code"] = tc.outcome
				}
				if tc.reasonCode != "" {
					d["reason_code"] = tc.reasonCode
				}
				b, _ := json.Marshal(d)
				return Result{Out: string(b), Code: 3}
			}}
			a.Quota = refusalQuota{fakeQuota: q, gate: CLIQuota{Runner: gateRunner, Binary: "fake-quota"}}
			sel, err := a.selectProfile(context.Background(), s)
			if exitCode(err) != tc.exit || sel.outcome != tc.want {
				t.Fatalf("structured refusal misclassified: %v/%s, want %s/%d", err, sel.outcome, tc.want, tc.exit)
			}
			wantCalls := 1
			if tc.want == outcomeQuotaUnknown {
				wantCalls = 2
			}
			if calls != wantCalls {
				t.Fatalf("gate calls=%d want=%d", calls, wantCalls)
			}
		})
	}
}

func TestRefreshExitThreeIsNotGateExhaustion(t *testing.T) {
	a, s, q, src := accountLeadFixture(t)
	q.CatalogRows = q.CatalogRows[1:2]
	src.presence = src.presence[1:2]
	src.accounts[testClaudeB] = SharedAccount{Key: testClaudeB}
	a.Runner = wrapRunner(a.Runner, func(c Command) (Result, bool) {
		if c.Name == "ssh" {
			return Result{Code: 3, Err: "quota exhausted"}, true
		}
		return Result{}, false
	})
	sel, err := a.selectProfile(context.Background(), s)
	if exitCode(err) != exitConfig || sel.outcome != outcomePolicyUnavailable {
		t.Fatalf("refresh process exit became measured gate exhaustion: %v/%s", err, sel.outcome)
	}
}

func TestStructuredGateRefusalMustConfirmItsAccountAndCode(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(map[string]any)
		want   string
		status int
	}{
		{"different account", func(r map[string]any) { r["account_key"] = testClaudeA }, outcomePolicyUnavailable, exitConfig},
		{"different pool", func(r map[string]any) { r["provider_id"] = "codex" }, outcomePolicyUnavailable, exitConfig},
		{"different exit", func(r map[string]any) { r["exit_code"] = 0 }, outcomePolicyUnavailable, exitConfig},
		{"contradictory ok", func(r map[string]any) { r["ok"] = true }, outcomePolicyUnavailable, exitConfig},
		{"conflicting flags", func(r map[string]any) { r["role_denied"] = true }, outcomePolicyUnavailable, exitConfig},
		{"conflicting reason", func(r map[string]any) { r["reason_code"] = "role_denied" }, outcomePolicyUnavailable, exitConfig},
		{"unknown outcome", func(r map[string]any) { r["outcome_code"] = "future_outcome" }, outcomeQuotaUnknown, 3},
		{"ambiguous full usage", func(r map[string]any) { delete(r, "outcome_code"); r["used_pct"] = 100 }, outcomeQuotaUnknown, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := map[string]any{"schema": "scopefuel.gate.v1", "provider_id": "claude", "account_key": testClaudeB, "ok": false, "exit_code": 3, "outcome_code": outcomeQuotaExhausted, "reason": "quota exhausted"}
			tc.change(r)
			b, _ := json.Marshal(r)
			q := CLIQuota{Binary: "fake-quota", Runner: &fakeRunner{Do: func(Command) Result { return Result{Out: string(b), Code: 3} }}}
			_, err := q.Gate(context.Background(), QuotaRequest{AccountKey: testClaudeB, Profile: "opus"})
			if exitCode(err) != tc.status || gateOutcome(err) != tc.want {
				t.Fatalf("unverified refusal trusted: %v/%s", err, gateOutcome(err))
			}
		})
	}
}

func TestRefreshNeverAddsIneligibleLocalHolder(t *testing.T) {
	for _, mode := range []string{"host-pin", "excluded", "disabled", "capability", "tag", "eligible-local"} {
		t.Run(mode, func(t *testing.T) {
			a, s, _, src := remoteFreeFixture(t, freeReceipt)
			const key = "devin:fedcba9876543210"
			s.Host = "auto"
			src.accounts[key] = SharedAccount{Key: key}
			src.presence = append(src.presence, PresenceRecord{HostID: "local", AccountKey: key, LastSeen: a.Now()})
			switch mode {
			case "host-pin":
				s.Host = "host-b"
			case "excluded":
				s.ExcludeHosts = "local"
			case "disabled":
				a.Config.Local["spawn"] = false
			case "capability":
				for name, seat := range a.Policy.Profiles {
					if seat.Canon == "devin-swe2-medium" {
						seat.Capability = "fixture_capability"
						a.Policy.Profiles[name] = seat
					}
				}
				a.Config.Hosts["host-b"]["fixture_capability"] = true
			case "tag":
				s.Tags = "fixture-tag"
				a.Config.Hosts["host-b"]["tags"] = "fixture-tag"
			}
			local, remote := 0, 0
			a.Runner = wrapRunner(a.Runner, func(c Command) (Result, bool) {
				if c.Name == "pacelever" {
					local++
				} else if c.Name == "ssh" {
					remote++
				} else {
					return Result{}, false
				}
				src.accounts[key] = SharedAccount{Key: key, Observed: true, Fresh: true}
				return Result{}, true
			})
			if _, err := a.selectProfile(context.Background(), s); err != nil {
				t.Fatal(err)
			}
			if mode == "eligible-local" {
				if local != 1 || remote != 0 {
					t.Fatalf("eligible local preference: local=%d remote=%d", local, remote)
				}
			} else if local != 0 || remote != 1 {
				t.Fatalf("refresh escaped eligible holders: local=%d remote=%d", local, remote)
			}
		})
	}
}
