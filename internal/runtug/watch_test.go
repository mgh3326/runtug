package runtug

import (
	"context"
	"strings"
	"testing"
)

func TestWatcherExpiryKeepsLiveLease(t *testing.T) {
	a := testApp(t)
	f := ledgerRunner(t, a)
	seedJob(t, a, "j", "worker")
	original := f.Do
	f.Do = func(c Command) Result {
		if c.Name == "herdr" && strings.Join(c.Args, " ") == "agent list" {
			return Result{Out: `{"result":{"agents":[{"pane_id":"w:p1","agent_status":"working"}]}}`}
		}
		return original(c)
	}
	t.Setenv("RUNTUG_COMPLETION_TIMEOUT_S", "1")
	if e := a.watch(context.Background(), []string{"j"}); e != nil {
		t.Fatal(e)
	}
	for _, c := range f.Calls {
		if c.Name == "arbiter" && c.Args[0] == "release" {
			t.Fatal("watch expiry released live job")
		}
		if strings.Contains(strings.Join(c.Args, " "), "job.lost") {
			t.Fatal("watch expiry recorded loss")
		}
	}
}
func TestWatcherMissingPaneReleasesThroughArbiter(t *testing.T) {
	a := testApp(t)
	f := ledgerRunner(t, a)
	seedJob(t, a, "j", "worker")
	if e := a.watch(context.Background(), []string{"j"}); e != nil {
		t.Fatal(e)
	}
	released, lost := false, false
	for _, c := range f.Calls {
		if c.Name == "arbiter" && c.Args[0] == "release" {
			released = true
		}
		if strings.Contains(strings.Join(c.Args, " "), "job.lost") {
			lost = true
		}
	}
	if !released || !lost {
		t.Fatal("lost run not settled")
	}
}
func TestMalformedCatalogNeverUsesFallbackModel(t *testing.T) {
	for _, body := range []string{`{"profile":"p","profile":"q"}`, `{} {}`, `{"catalog":{"stale":true},"profile":"p"}`} {
		q := CLIQuota{&fakeRunner{Do: func(Command) Result { return Result{Out: body} }}, "quota"}
		if _, e := q.Launch(context.Background(), QuotaRequest{Profile: "p"}); code(e) != 70 {
			t.Fatalf("malformed/stale catalog accepted: %v", e)
		}
	}
}
func TestFallbackHostOrderAndLocalClosure(t *testing.T) {
	a := testApp(t)
	a.Source = &fakePlacementSource{}
	a.LoadRatio = func(context.Context) (float64, error) { return 0.1, nil }
	a.Config.Local = map[string]any{"spawn": false}
	a.Config.Hosts = map[string]map[string]any{"host-a": {}, "host-b": {}}
	a.Config.HostOrder = []string{"host-b", "host-a"}
	p, e := a.plan(context.Background(), Spawn{Tags: ""}, "", 0)
	if e != nil {
		t.Fatal(e)
	}
	var names []string
	for _, c := range p.ordered {
		names = append(names, c.name)
	}
	if strings.Join(names, ",") != "host-b,host-a" {
		t.Fatalf("%v", names)
	}
}
func TestCodexFooterRequiresLastContextRow(t *testing.T) {
	screen := "GPT-6.1 Example high /home/example/tree Context 99%"
	if !CodexFooterProof(screen, "gpt-6.1-example", "high", "/home/example/tree", "/home/example") {
		t.Fatal("valid footer refused")
	}
	if CodexFooterProof(screen+"\nOther model low /home/example/tree Context 50%", "gpt-6.1-example", "high", "/home/example/tree", "/home/example") {
		t.Fatal("stale footer accepted")
	}
}
