package runtug

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeSecrets are credential-shaped fixtures that must never reach the
// fleet-events file, whatever error text carries them.
var fakeSecrets = []string{
	"ghp_FAKEfakeFAKEfakeFAKEfake0123456789",
	"hunter2pass",
	"tok3nVALUEfake99",
}

const fakeSecretText = "token=tok3nVALUEfake99 ghp_FAKEfakeFAKEfakeFAKEfake0123456789 https://ops:hunter2pass@quota.invalid/v1"

func readFleetEvents(t *testing.T, a *App) (string, []map[string]string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(a.stateDir(), "events", "fleet-events.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		t.Fatal(err)
	}
	var events []map[string]string
	for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		var fields map[string]any
		if err := json.Unmarshal([]byte(line), &fields); err != nil {
			t.Fatalf("event line is not JSON: %q", line)
		}
		ev := map[string]string{}
		for _, k := range []string{"ts", "host", "tool", "kind", "job", "outcome", "reason", "detail"} {
			v, ok := fields[k].(string)
			if !ok {
				t.Fatalf("event field %q missing or not a string: %q", k, line)
			}
			ev[k] = v
		}
		if len(fields) != 8 {
			t.Fatalf("event has fields beyond the v1 shape: %q", line)
		}
		events = append(events, ev)
	}
	return string(raw), events
}

func TestSpawnRefusalWritesOneRedactedFleetEvent(t *testing.T) {
	a, s, _, _ := cmdFixture(t)
	a.Quota.(*fakeQuota).GateDo = func(QuotaRequest) (string, error) {
		return "", fail(exitConfig, "quota gate refused: %s", fakeSecretText)
	}
	err := a.Spawn(context.Background(), s)
	if code(err) != exitConfig || !strings.Contains(err.Error(), "hunter2pass") {
		t.Fatalf("fixture must refuse with the secret in the error: %v", err)
	}
	raw, events := readFleetEvents(t, a)
	if len(events) != 1 {
		t.Fatalf("want exactly one event, got %d: %s", len(events), raw)
	}
	ev := events[0]
	if ev["tool"] != "runtug" || ev["kind"] != "spawn" || ev["job"] != "job-a" || ev["outcome"] != "refused" || ev["reason"] != "config" {
		t.Fatalf("event: %+v", ev)
	}
	if _, err := time.Parse(time.RFC3339Nano, ev["ts"]); err != nil || ev["host"] == "" {
		t.Fatalf("ts/host: %+v", ev)
	}
	if !strings.HasPrefix(ev["detail"], "exit=70 ") || !strings.Contains(ev["detail"], "[redacted]") {
		t.Fatalf("detail must keep the status and mark the redaction: %q", ev["detail"])
	}
	for _, secret := range fakeSecrets {
		if strings.Contains(raw, secret) {
			t.Fatalf("secret %q reached the events file: %s", secret, raw)
		}
	}
}

func TestPlacementDecisionEventNamesHostAndRanking(t *testing.T) {
	src := &fakePlacementSource{hosts: []HostView{
		{Name: "first", Connected: true, LoadRatio: ptrF(0.1)},
		{Name: "second", Connected: true, LoadRatio: ptrF(0.9)},
	}}
	a, s, _ := remoteFixture(t, src, &fakeWaker{}, "2026-01-01T00:00:00Z", func(args string) Result {
		if strings.Contains(args, "_spawn-stdin") {
			if strings.Contains(args, "first") {
				return Result{Code: 77, Err: "quota role-denied " + fakeSecretText}
			}
			return Result{Out: "OK pane=w:p1 job=job-a\n"}
		}
		return Result{}
	})
	a.Config.Hosts = map[string]map[string]any{"first": {}, "second": {}}
	a.Config.HostOrder = []string{"first", "second"}
	if _, _, err := a.place(context.Background(), s, "brief"); err != nil {
		t.Fatal(err)
	}
	raw, events := readFleetEvents(t, a)
	if len(events) != 1 {
		t.Fatalf("want one placement event, got %d: %s", len(events), raw)
	}
	ev := events[0]
	// Neither host carries a hub score, so ranking fell back to raw load.
	if ev["kind"] != "placement" || ev["outcome"] != "placed" || ev["reason"] != "raw_load" {
		t.Fatalf("event: %+v", ev)
	}
	for _, want := range []string{"chosen=second", "ranking=raw_load", "candidates=3", "refused=local:spawn_disabled,first:refused_rc_77"} {
		if !strings.Contains(ev["detail"], want) {
			t.Fatalf("detail %q lacks %q", ev["detail"], want)
		}
	}
	for _, secret := range fakeSecrets {
		if strings.Contains(raw, secret) {
			t.Fatalf("secret %q reached the events file", secret)
		}
	}
}

func TestPlacementRankingNamesScoreOnlyWhenEveryHostHasOne(t *testing.T) {
	p := &placePlan{ordered: []*placeCandidate{{name: "a", score: ptrF(2)}, {name: "b", score: ptrF(1)}}}
	if got := p.ranking(); got != "score" {
		t.Fatalf("all scored: %s", got)
	}
	p.ordered[1].score = nil
	if got := p.ranking(); got != "raw_load" {
		t.Fatalf("one unscored host falls back to raw load: %s", got)
	}
	p.ordered = p.ordered[:1]
	if got := p.ranking(); got != "single_candidate" {
		t.Fatalf("one candidate: %s", got)
	}
}

func TestDryRunPlacementWritesNoEvent(t *testing.T) {
	a := testApp(t)
	a.placementEvent(Spawn{DryRun: true, Job: "job-a"}, map[string]any{"chose": "h1"}, nil)
	if raw, events := readFleetEvents(t, a); len(events) != 0 {
		t.Fatalf("dry run wrote %s", raw)
	}
}

func TestStaleQuotaObservationEventCarriesAge(t *testing.T) {
	a := testApp(t)
	key := "claude:0123456789abcdef"
	acc := SharedAccount{Key: key, Observed: true, Fresh: false, MeasuredAt: a.Now().Add(-90 * time.Minute)}
	_, err := a.judgeHost(context.Background(), QuotaRequest{}, "claude", "host-b", false, key, acc)
	if err == nil {
		t.Fatal("a stale observation must refuse")
	}
	raw, events := readFleetEvents(t, a)
	if len(events) != 1 {
		t.Fatalf("want one event, got %s", raw)
	}
	ev := events[0]
	if ev["kind"] != "quota_observation" || ev["outcome"] != "stale" || ev["reason"] != "stale" || ev["detail"] != "target=host-b pool=claude age_min=90" {
		t.Fatalf("event: %+v", ev)
	}
	if strings.Contains(raw, "0123456789abcdef") {
		t.Fatal("the account fingerprint must not reach the events file")
	}
}
