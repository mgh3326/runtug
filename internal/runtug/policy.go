package runtug

import (
	_ "embed"
	"encoding/json"
	"os"
	"regexp"
	"slices"
	"strings"
)

//go:embed policy.json
var defaultPolicy []byte

type Policy struct {
	Version  int             `json:"version"`
	Profiles map[string]Seat `json:"profiles"`
}
type Seat struct {
	Canon      string          `json:"canon"`
	Kind       string          `json:"kind"`
	Family     string          `json:"family"`
	Effort     string          `json:"effort,omitempty"`
	Pin        bool            `json:"pin,omitempty"`
	Capability string          `json:"capability"`
	E6         bool            `json:"e6,omitempty"`
	Roles      []string        `json:"roles"`
	Rungs      map[string]Rung `json:"rungs,omitempty"`
}
type Rung struct {
	Roles []string `json:"roles"`
	E6    *bool    `json:"e6,omitempty"`
}

func LoadPolicy(path string) (Policy, error) {
	b := defaultPolicy
	var e error
	if path != "" {
		b, e = os.ReadFile(path)
		if e != nil {
			return Policy{}, e
		}
	}
	var p Policy
	if e = decode(b, &p); e != nil {
		return p, e
	}
	if p.Version != 1 || len(p.Profiles) == 0 {
		return p, fail(70, "invalid seat policy version or empty profiles")
	}
	for name, s := range p.Profiles {
		if s.Canon == "" || s.Kind == "" || s.Family == "" || s.Capability == "" || len(s.Roles) == 0 {
			return p, fail(70, "missing seat data for %s", name)
		}
	}
	return p, nil
}

var gradeOrder = map[string]int{"C": 0, "B": 1, "A": 2, "A+": 3, "S": 4, "S+": 5}
var taskMarker = regexp.MustCompile(`(?i)\(grade (S\+|S|A\+|A|B|C),\s*(T[0-3])\b`)

func (s Seat) Check(l Launch, role, grade, builderFamily, arm string, host map[string]any) error {
	if _, ok := gradeOrder[l.Grade]; !ok || l.ModelID == "" || l.Effort == "" || l.Gate == "" {
		return fail(70, "canon launch data incomplete")
	}
	if !boolean(host, s.Capability, false) { // Standard harnesses predate capability flags; only their explicit policy sentinel is implicit.
		if s.Capability != "standard" {
			return fail(70, "host lacks capability %s", s.Capability)
		}
	}
	roles := s.Roles
	e6 := s.E6
	if len(s.Rungs) > 0 {
		r, ok := s.Rungs[l.Effort]
		if !ok {
			return fail(70, "missing policy rung %s@%s", s.Canon, l.Effort)
		}
		roles = r.Roles
		if r.E6 != nil {
			e6 = *r.E6
		}
	}
	if !slices.Contains(roles, role) {
		return fail(77, "seat does not allow role %s", role)
	}
	if role == "builder" && l.Gate != "default" {
		return fail(77, "%s rung is worker/tester only", l.Gate)
	}
	if e6 && arm != s.Canon+"@"+l.Effort {
		return fail(77, "requires SCOPEFUEL_E6_ARM=%s@%s", s.Canon, l.Effort)
	}
	if grade != "" {
		n, ok := gradeOrder[grade]
		if !ok || gradeOrder[l.Grade] < n {
			return fail(77, "canon grade %s does not cover task grade %s", l.Grade, grade)
		}
	}
	if role == "tester" || role == "reviewer" {
		if builderFamily == "" {
			return fail(77, "tester requires readable builder family")
		}
		if s.Family == builderFamily {
			return fail(77, "tester and builder share family %s", s.Family)
		}
	}
	return nil
}
func encode(v any) ([]byte, error) { return json.Marshal(v) }
func decode(b []byte, v any) error {
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.DisallowUnknownFields()
	return d.Decode(v)
}
