package runtug

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// runSimulate invokes the simulate command on a fixture path and returns the
// printed decision record. The fake runner proves nothing external ran.
func runSimulate(t *testing.T, args ...string) (map[string]any, *fakeRunner, error) {
	t.Helper()
	a := testApp(t)
	fr := &fakeRunner{}
	a.Runner = fr
	a.Quota = CLIQuota{Runner: fr, Binary: "pacelever"}
	a.Source = &fakePlacementSource{}
	out := &bytes.Buffer{}
	a.Out = out
	err := a.simulate(context.Background(), args)
	var rec map[string]any
	if out.Len() > 0 {
		if e := json.Unmarshal(out.Bytes(), &rec); e != nil {
			t.Fatalf("simulate output is not JSON: %v\n%s", e, out.String())
		}
	}
	return rec, fr, err
}

func simulateFixturePath(t *testing.T) string {
	t.Helper()
	return filepath.Join("testdata", "simulate-claude-codex-2026-10-31.json")
}

func TestSimulateRanksWasteUrgency(t *testing.T) {
	rec, fr, err := runSimulate(t, "--selection", simulateFixturePath(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(fr.Calls) != 0 {
		t.Fatalf("simulate must run no external command: %v", fr.Calls)
	}
	chosen := rec["chosen"].(map[string]any)
	if chosen["seat"] != "sonnet" || chosen["tier"] != "waste" {
		t.Fatalf("the 2026-10-31 inputs choose the claude rung: %v", chosen)
	}
	if rec["mode"] != "simulate" {
		t.Fatalf("mode: %v", rec["mode"])
	}
	if got := candOutcome(rec, "devin-swe2", "high")["outcome"]; got != "dropped:free_limit" {
		t.Fatalf("the held free rung is refused by its observed free limit: %v", got)
	}
	if got := candOutcome(rec, "sonnet", "high")["waste_urgency"]; got == nil {
		t.Fatalf("ranked candidates carry waste_urgency: %v", got)
	}
}

func TestSimulateSetChangesChoice(t *testing.T) {
	// After claude's reset it reads 0% used, projected 0 with +168 h to the
	// next reset (a fresh window cannot be paced, so the projection is given).
	rec, fr, err := runSimulate(t, "--selection", simulateFixturePath(t), "--set", "claude:used=0,reset=+168h,projected=0")
	if err != nil {
		t.Fatal(err)
	}
	if len(fr.Calls) != 0 {
		t.Fatalf("simulate must run no external command: %v", fr.Calls)
	}
	if got := rec["chosen"].(map[string]any)["seat"]; got != "codex-luna" {
		t.Fatalf("--set must change the choice: %v", got)
	}
}

func TestSimulateNowAndJobEnvelope(t *testing.T) {
	// A job event envelope wraps the same record under payload.selection.
	b, err := os.ReadFile(simulateFixturePath(t))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	env := map[string]any{"kind": "job.placement", "created_at": "2026-10-31T07:24:32Z", "job_id": "p4-pw6", "payload": map[string]any{"selection": doc}}
	eb, _ := json.Marshal(env)
	path := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(path, eb, 0600); err != nil {
		t.Fatal(err)
	}
	rec, _, err := runSimulate(t, "--selection", path, "--now", "2026-10-31T07:24:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if got := rec["chosen"].(map[string]any)["seat"]; got != "sonnet" {
		t.Fatalf("job envelope selects the claude rung: %v", got)
	}
}

func TestSimulateRejectsMissingSelection(t *testing.T) {
	if _, _, err := runSimulate(t); err == nil {
		t.Fatal("simulate without --selection must fail")
	}
	if _, _, err := runSimulate(t, "--selection", simulateFixturePath(t), "--set", "claude:used=oops"); err == nil {
		t.Fatal("a malformed --set must fail")
	}
}
