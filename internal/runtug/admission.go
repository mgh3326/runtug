package runtug

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/mgh3326/runtug/internal/arbiter"
)

// admitPinnedProfile applies the same role and retirement boundary to -m and
// to an explicit profile delivered by another host. A placement override is
// never authority to bypass this boundary.
func (a *App) admitPinnedProfile(ctx context.Context, s *Spawn) error {
	seat, ok := a.Policy.Profiles[s.Profile]
	if !ok {
		return fail(exitConfig, "missing seat policy for %s", s.Profile)
	}
	if s.Host == "local" && seat.Capability != "standard" && !boolean(a.Config.Local, seat.Capability, false) {
		return fail(exitConfig, "host lacks capability %s", seat.Capability)
	}
	route, err := a.routeFor(ctx, s.Role, s.RoutingRole)
	if err != nil {
		return err
	}
	if !route.admitsSeat(seat) {
		return fail(exitSeatRefused, "NO_ROLE_CANDIDATE: profile %s is outside routing role %s; --override-reason overrides placement only", s.Profile, route.role)
	}
	if a.Quota == nil {
		return fail(exitConfig, "POLICY_UNAVAILABLE: quota catalog unavailable")
	}
	rows, err := a.Quota.Catalog(ctx)
	if err != nil {
		return fail(exitConfig, "POLICY_UNAVAILABLE: quota catalog unavailable: %v", err)
	}
	active := false
	for _, row := range rows {
		if row.Profile == seat.Canon && row.RetiredAt == nil {
			active = true
			break
		}
	}
	if !active {
		return fail(exitSeatRefused, "NO_ROLE_CANDIDATE: profile %s is retired or absent from the active catalog", s.Profile)
	}
	s.RoutingRole = route.role
	s.Sel = map[string]any{"mode": "override", "profile": s.Profile, "routing_role": route.role, "routing": route.record(""), "outcome_code": outcomeAdmitted, "account_key": s.AccountKey}
	return nil
}

// admissionGate keeps the account-bound JSON receipt for quota admission and
// extracts only the fields the existing local lease records.
func admissionGate(profile, receipt string) (arbiter.Gate, error) {
	if !strings.HasPrefix(strings.TrimSpace(receipt), "{") {
		return arbiter.ParseGate(profile, receipt)
	}
	g := arbiter.Gate{Audit: map[string]string{}}
	var r struct {
		Schema  string `json:"schema"`
		Pool    string `json:"provider_id"`
		Profile string `json:"profile"`
		OK      bool   `json:"ok"`
		Exit    *int   `json:"exit_code"`
	}
	if uniqueJSON([]byte(receipt)) != nil || json.Unmarshal([]byte(receipt), &r) != nil || r.Schema != "scopefuel.gate.v1" || !r.OK || r.Exit == nil || *r.Exit != 0 || !validID(r.Pool) || r.Profile != "" && r.Profile != profile {
		return g, fail(exitConfig, "quota receipt does not admit the selected profile")
	}
	g.Pool = r.Pool
	var fields map[string]any
	_ = json.Unmarshal([]byte(receipt), &fields)
	for _, key := range []string{"escalation_override", "operator_request_ref", "requested_by", "ref_resolution"} {
		if value := fields[key]; value != nil {
			switch v := value.(type) {
			case string:
				g.Audit[key] = v
			case bool:
				g.Audit[key] = fmt.Sprint(v)
			default:
				return g, fail(exitConfig, "invalid quota receipt audit field %s", key)
			}
		}
	}
	return g, nil
}

func (a *App) stampSelection(s *Spawn, host string) {
	if s.Sel == nil {
		s.Sel = map[string]any{}
	}
	s.Sel["routing_role"], s.Sel["account_key"], s.Sel["host"], s.Sel["outcome_code"] = s.RoutingRole, s.AccountKey, host, outcomeAdmitted
	if chosen, ok := s.Sel["chosen"].(map[string]any); ok {
		chosen["account_key"], chosen["host"], chosen["outcome_code"] = s.AccountKey, host, outcomeAdmitted
	}
}

func outcomeForError(err error) string {
	if err == nil {
		return outcomeAdmitted
	}
	var refusal *gateRefusal
	if errors.As(err, &refusal) {
		return refusal.outcome
	}
	switch exitCode(err) {
	case exitRoleWait, exitRoleEscalate:
		return outcomeQuotaExhausted
	case exitQuotaUnknown:
		return outcomeQuotaUnknown
	case exitAuthUnavailable:
		return outcomeAuthUnavailable
	case exitPlacement, exitLocalDisabled:
		return outcomeNoEligibleHost
	case exitConfig:
		return outcomePolicyUnavailable
	default:
		return outcomeNoRoleCandidate
	}
}
