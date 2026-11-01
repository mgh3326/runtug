package runtug

import (
	"fmt"
	"slices"
	"strings"
)

// Host fit decides which work a host can take before any account or quota
// is read. Three host keys decide it, all
// data in hosts.toml ([local] or [hosts.NAME]):
//
//   - tags (alias caps): the capabilities the host offers. A task's --tags
//     must be a subset. A capability the host does not declare is unknown and
//     fails closed for that host only; other hosts stay eligible.
//   - roles: the spawn roles the host accepts. Unset accepts every role.
//   - grades: the task grades the host accepts. Unset accepts every grade. A
//     lead or consult has no grade, so a host with grades set refuses it.

var spawnRoles = []string{"worker", "builder", "tester", "reviewer", "counsel"}

// hostCaps returns the declared capabilities of a host: tags and caps merged.
func hostCaps(cfg map[string]any) []string {
	var out []string
	for _, t := range append(stringsValue(cfg, "tags"), stringsValue(cfg, "caps")...) {
		if t = strings.TrimSpace(t); t != "" && !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	return out
}

// missingCaps lists the required tags a host does not declare.
func missingCaps(cfg map[string]any, required []string) []string {
	caps := hostCaps(cfg)
	var out []string
	for _, t := range required {
		if !slices.Contains(caps, t) {
			out = append(out, t)
		}
	}
	return out
}

// roleRefusal names why a host does not take this role and grade, or "".
// An empty grade is a lead or consult, which a grade-limited host refuses.
func roleRefusal(cfg map[string]any, role, grade string) string {
	if roles := stringsValue(cfg, "roles"); len(roles) > 0 && !slices.Contains(roles, role) {
		return "role_not_accepted:" + role
	}
	if grades := stringsValue(cfg, "grades"); len(grades) > 0 && !slices.Contains(grades, grade) {
		return "grade_not_accepted:" + firstNonempty(grade, "none")
	}
	return ""
}

// validHostFit checks the roles and grades lists of one host table.
func validHostFit(h map[string]any, key string) error {
	if e := validTags(h["caps"], key+".caps"); e != nil {
		return e
	}
	for _, f := range []struct {
		name  string
		valid func(string) bool
	}{
		{"roles", func(v string) bool { return slices.Contains(spawnRoles, v) }},
		{"grades", func(v string) bool { _, ok := gradeOrder[v]; return ok }},
	} {
		v, ok := h[f.name]
		if !ok {
			continue
		}
		list, isList := v.([]any)
		if !isList || len(list) == 0 {
			return fail(exitConfig, "%s.%s must be a nonempty list", key, f.name)
		}
		for _, x := range list {
			if s, isStr := x.(string); !isStr || !f.valid(s) {
				return fail(exitConfig, "%s.%s contains an invalid value %v", key, f.name, x)
			}
		}
	}
	return nil
}

// capsGap names the required tags no usable host declares, for a refusal.
func capsGap(hosts []selHost, required []string) string {
	if len(required) == 0 {
		return ""
	}
	var missing []string
	all := false
	for _, t := range required {
		if !slices.ContainsFunc(hosts, func(h selHost) bool { return !h.excluded && slices.Contains(hostCaps(h.cfg), t) }) {
			missing = append(missing, t)
		}
	}
	for _, h := range hosts {
		if !h.excluded && len(missingCaps(h.cfg, required)) == 0 {
			all = true
		}
	}
	switch {
	case len(missing) > 0:
		return fmt.Sprintf("; no host declares cap %s", strings.Join(missing, ","))
	case !all:
		return fmt.Sprintf("; no single host declares all caps %s", strings.Join(required, ","))
	}
	return ""
}
