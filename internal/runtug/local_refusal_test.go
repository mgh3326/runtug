package runtug

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// localRefusalPlan runs `runtug plan` on a host whose local spawns are enabled,
// with the hub reporting localReason for it, and returns the chosen host and
// the local candidate's row.
func localRefusalPlan(t *testing.T, localReason string) (string, map[string]any) {
	t.Helper()
	a, _, _, _, out := planFixture(t)
	a.Config.Local["spawn"] = true
	src := a.Source.(*fakePlacementSource)
	high, low := 90.0, 10.0
	// The local host outranks host-b, so only a refusal can move the job.
	src.hosts = []HostView{
		{Name: a.machine(), Connected: true, Score: &high, Reason: localReason},
		{Name: "host-b", Connected: true, Score: &low},
	}
	if e := a.planCmd(context.Background(), []string{"--grade", "A", "--t", "T1", "--json"}); e != nil {
		t.Fatalf("%v\n%s", e, out.String())
	}
	var doc map[string]any
	if e := json.Unmarshal(out.Bytes(), &doc); e != nil {
		t.Fatal(e)
	}
	chosen, _ := doc["chosen"].(map[string]any)["host"].(string)
	for _, c := range doc["placement"].(map[string]any)["candidates"].([]any) {
		if row := c.(map[string]any); row["local"] == true {
			return chosen, row
		}
	}
	t.Fatal("placement record lacks the local candidate")
	return "", nil
}

// A local host the hub refuses is refused like a remote one, and the record
// keeps the hub's reason so a refused spawn can say why.
func TestPlanRefusesLocalHostHubDoesNotAccept(t *testing.T) {
	for _, reason := range []string{"not_accepting", "not_accepting(memory_pressure)", "not_accepting(on_battery)", "not_accepting(power_unknown),memory_unknown"} {
		t.Run(reason, func(t *testing.T) {
			chosen, row := localRefusalPlan(t, reason)
			if chosen != "host-b" {
				t.Fatalf("chose %q, want host-b", chosen)
			}
			if row["outcome"] != "not_accepting" || row["hub_reason"] != reason || row["chosen"] == true {
				t.Fatalf("local row: %v", row)
			}
		})
	}
}

// Any other hub reason leaves the local host eligible, and it wins on score.
func TestPlanKeepsLocalHostHubAccepts(t *testing.T) {
	for _, reason := range []string{"", "available", "memory_unknown"} {
		t.Run(reason, func(t *testing.T) {
			chosen, row := localRefusalPlan(t, reason)
			if row["outcome"] == "not_accepting" || row["hub_reason"] != nil {
				t.Fatalf("local row: %v", row)
			}
			if !strings.EqualFold(chosen, row["host"].(string)) {
				t.Fatalf("chose %q, want the local host %v", chosen, row["host"])
			}
		})
	}
}

// Model selection reads the same hub view through its own host list, so it
// must exclude the refused local host too.
func TestSelectionExcludesLocalHostHubDoesNotAccept(t *testing.T) {
	for reason, want := range map[string]bool{"not_accepting(on_battery)": true, "available": false} {
		t.Run(reason, func(t *testing.T) {
			a, _, _, _, _ := planFixture(t)
			a.Config.Local["spawn"] = true
			a.Source.(*fakePlacementSource).hosts = []HostView{{Name: a.machine(), Connected: true, Reason: reason}}
			sp := a.newSelPresence(context.Background(), Spawn{}, selReq{})
			for _, h := range sp.hosts {
				if h.local {
					if h.excluded != want {
						t.Fatalf("excluded=%v, want %v", h.excluded, want)
					}
					if want && h.fit != reason {
						t.Fatalf("fit=%q, want the hub's reason %q", h.fit, reason)
					}
					return
				}
			}
			t.Fatal("selection lacks the local host")
		})
	}
}

// A forced local spawn never reads the hub's refusal: it stays a placement the
// caller chose (operator decision 2026-11-02, hk 2649).
func TestForcedLocalSpawnIgnoresHubRefusal(t *testing.T) {
	a, _, _, _, _ := planFixture(t)
	a.Config.Local["spawn"] = true
	a.Source.(*fakePlacementSource).hosts = []HostView{{Name: a.machine(), Connected: true, Reason: "not_accepting(on_battery)"}}
	for _, host := range []string{"local", a.machine()} {
		remote, rec, e := a.placeForced(context.Background(), Spawn{Host: host, Profile: "sonnet"}, "")
		if e != nil || remote || rec["source"] != "forced" {
			t.Fatalf("host %s: remote=%v rec=%v err=%v", host, remote, rec, e)
		}
	}
}
