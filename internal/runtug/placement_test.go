package runtug

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

type fakePlacementSource struct {
	hosts       []HostView
	hostsErr    error
	presence    []PresenceRecord
	presenceErr error
	accounts    map[string]SharedAccount
	accountErr  error
}

func (f *fakePlacementSource) Hosts(context.Context, string) ([]HostView, error) {
	return f.hosts, f.hostsErr
}
func (f *fakePlacementSource) Presence(context.Context) ([]PresenceRecord, error) {
	return f.presence, f.presenceErr
}
func (f *fakePlacementSource) SharedAccount(_ context.Context, key string) (SharedAccount, error) {
	if f.accountErr != nil {
		return SharedAccount{Key: key, Degraded: true}, f.accountErr
	}
	if a, ok := f.accounts[key]; ok {
		return a, nil
	}
	return SharedAccount{Key: key}, nil
}

type fakeWaker struct {
	calls  []string
	err    error
	onWake func(string)
}

func (w *fakeWaker) Wake(_ context.Context, host string, _ WakeRequest) error {
	w.calls = append(w.calls, host)
	if w.onWake != nil {
		w.onWake(host)
	}
	return w.err
}

func ptrF(v float64) *float64 { return &v }

func placementApp(t *testing.T, source *fakePlacementSource) *App {
	t.Helper()
	a := testApp(t)
	a.Source = source
	a.Wake = &fakeWaker{}
	a.LoadRatio = func(context.Context) (float64, error) { return 0.2, nil }
	return a
}

func orderNames(p *placePlan) []string {
	var out []string
	for _, c := range p.ordered {
		out = append(out, c.name)
	}
	return out
}

func TestPlacementTagFiltering(t *testing.T) {
	a := placementApp(t, &fakePlacementSource{})
	a.Config.Local = map[string]any{"spawn": false}
	a.Config.Hosts = map[string]map[string]any{
		"unity-host": {"tags": "macos,unity"},
		"gpu-host":   {"tags": "linux,gpu,unity"},
		"light-host": {"tags": "linux,arm,light"},
		"plain-host": {},
	}
	a.Config.HostOrder = []string{"unity-host", "gpu-host", "light-host", "plain-host"}
	p, e := a.plan(context.Background(), Spawn{Tags: "unity"}, "", 0)
	if e != nil {
		t.Fatal(e)
	}
	if got := strings.Join(orderNames(p), ","); got != "unity-host,gpu-host" {
		t.Fatalf("tagged job must only match hosts carrying the tag: %v", got)
	}
	p, e = a.plan(context.Background(), Spawn{Tags: "linux"}, "", 0)
	if e != nil {
		t.Fatal(e)
	}
	if got := strings.Join(orderNames(p), ","); got != "gpu-host,light-host" {
		t.Fatalf("%v", got)
	}
	p, e = a.plan(context.Background(), Spawn{}, "", 0)
	if e != nil {
		t.Fatal(e)
	}
	if got := strings.Join(orderNames(p), ","); got != "unity-host,gpu-host,light-host,plain-host" {
		t.Fatalf("a job without tags may go anywhere: %v", got)
	}
}

func TestPlacementLoadOrderingAndThreshold(t *testing.T) {
	a := placementApp(t, &fakePlacementSource{hosts: []HostView{
		{Name: "busy", Connected: true, LoadRatio: ptrF(2.4)},
		{Name: "warm", Connected: true, LoadRatio: ptrF(0.9)},
		{Name: "idle", Connected: true, LoadRatio: ptrF(0.3)},
		{Name: "odd", Connected: true},
	}})
	a.Config.Local = map[string]any{"spawn": false}
	a.Config.Hosts = map[string]map[string]any{"busy": {}, "warm": {}, "idle": {}, "odd": {}}
	a.Config.HostOrder = []string{"busy", "warm", "idle", "odd"}
	p, e := a.plan(context.Background(), Spawn{}, "", 0)
	if e != nil {
		t.Fatal(e)
	}
	if got := strings.Join(orderNames(p), ","); got != "idle,warm,odd,busy" {
		t.Fatalf("load5/ncpu must order candidates; over-threshold last: %v", got)
	}
}

func TestPlacementLoginRanksBeforeUnknown(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	key := "pool-a:0123456789abcdef"
	src := &fakePlacementSource{
		hosts: []HostView{{Name: "fresh", Connected: true, LoadRatio: ptrF(0.5)}, {Name: "idle", Connected: true, LoadRatio: ptrF(0.1)}},
		presence: []PresenceRecord{
			{HostID: "fresh", AccountKey: key, LastSeen: now.Add(-time.Hour)},
			{HostID: "idle", AccountKey: "other:0123456789abcdef", LastSeen: now.Add(-time.Hour)},
		},
		accounts: map[string]SharedAccount{key: {Key: key, Observed: true, Fresh: true, MeasuredAt: now.Add(-time.Hour)}},
	}
	a := placementApp(t, src)
	a.Config.Local = map[string]any{"spawn": false}
	a.Config.Hosts = map[string]map[string]any{"fresh": {}, "idle": {}}
	a.Config.HostOrder = []string{"idle", "fresh"}
	p, e := a.plan(context.Background(), Spawn{}, "pool-a", 0)
	if e != nil {
		t.Fatal(e)
	}
	if got := strings.Join(orderNames(p), ","); got != "fresh,idle" {
		t.Fatalf("a host with fresh presence for the needed account must rank first: %v", got)
	}
}

func TestPlacementDegradedReadsAreUnknownNotAbsent(t *testing.T) {
	a := placementApp(t, &fakePlacementSource{
		hosts:       []HostView{{Name: "h1", Connected: true}},
		presenceErr: errString("down"),
		accountErr:  errString("down"),
	})
	a.Config.Local = map[string]any{"spawn": false}
	a.Config.Hosts = map[string]map[string]any{"h1": {}}
	a.Config.HostOrder = []string{"h1"}
	p, e := a.plan(context.Background(), Spawn{}, "pool-a", 0)
	if e != nil {
		t.Fatal(e)
	}
	if got := strings.Join(orderNames(p), ","); got != "h1" {
		t.Fatalf("degraded presence must not remove candidates: %v", got)
	}
	if p.ordered[0].login != loginUnknown {
		t.Fatalf("degraded readings class as unknown: %q", p.ordered[0].login)
	}
	if !slices.Contains(p.degraded, "presence_degraded") {
		t.Fatal("degraded reads must be noted in the decision record")
	}
}

type errString string

func (e errString) Error() string { return string(e) }

// remoteFixture prepares an app whose Runner answers the SSH and ledger calls
// a remote placement round makes; no real spawn ever happens.
func remoteFixture(t *testing.T, src *fakePlacementSource, w *fakeWaker, taskCreated string, ssh func(args string) Result) (*App, Spawn, *fakeRunner) {
	t.Helper()
	a := placementApp(t, src)
	a.Wake = w
	a.Config.Local = map[string]any{"spawn": false}
	cwd := a.Home + "/work/repo-a"
	f := &fakeRunner{}
	f.Do = func(c Command) Result {
		args := strings.Join(c.Args, " ")
		switch {
		case c.Name == "handoffkeep" && strings.HasPrefix(args, "tasks show"):
			return Result{Out: `{"id":42,"title":"work (grade B, T1)","state":"ready","refs":{},"created_at":` + strconv.Quote(taskCreated) + `}`}
		case c.Name == "arbiter" && c.Args[0] == "event":
			var p map[string]any
			if e := json.Unmarshal([]byte(c.Args[6]), &p); e != nil {
				t.Fatal(e)
			}
			putEvent(t, a, c.Args[2], c.Args[4], p)
			return Result{}
		case c.Name == "herdr" && strings.HasPrefix(args, "agent list"):
			return Result{Out: `{"agents":[]}`}
		case c.Name == "git" && strings.Contains(args, "rev-parse"):
			return Result{Out: a.Home + "/work/repo-a\n"}
		case c.Name == "git" && strings.Contains(args, "get-url"):
			return Result{Out: "git@git.invalid:org/repo-a.git\n"}
		case c.Name == "ssh":
			return ssh(args)
		}
		return Result{}
	}
	a.Runner = f
	s := Spawn{CWD: cwd, Profile: "cmd-dsflash41", Job: "job-a", Task: "42", Role: "worker", Host: "auto"}
	return a, s, f
}

func TestPlacementRoundRefusalMovesToNextCandidate(t *testing.T) {
	src := &fakePlacementSource{hosts: []HostView{
		{Name: "first", Connected: true, LoadRatio: ptrF(0.1)},
		{Name: "second", Connected: true, LoadRatio: ptrF(0.9)},
	}}
	var spawns []string
	a, s, _ := remoteFixture(t, src, &fakeWaker{}, "2026-01-01T00:00:00Z", func(args string) Result {
		if strings.Contains(args, "_spawn-stdin") {
			if strings.Contains(args, "first") {
				return Result{Code: 4, Err: "quota refused"}
			}
			spawns = append(spawns, args)
			return Result{Out: "OK pane=w:p1 job=job-a\n"}
		}
		return Result{}
	})
	a.Config.Hosts = map[string]map[string]any{"first": {}, "second": {}}
	a.Config.HostOrder = []string{"first", "second"}
	handled, rec, e := a.placeAuto(context.Background(), s, "brief")
	if e != nil || !handled {
		t.Fatalf("%v", e)
	}
	if rec["chose"] != "second" {
		t.Fatalf("a refused candidate must move to the next: %v", rec["chose"])
	}
	if len(spawns) != 1 {
		t.Fatalf("second candidate should have been spawned once: %v", spawns)
	}
}

func TestPlacementDecisionRecordedInJobEvents(t *testing.T) {
	src := &fakePlacementSource{hosts: []HostView{{Name: "h1", Connected: true, LoadRatio: ptrF(0.1)}}}
	a, s, _ := remoteFixture(t, src, &fakeWaker{}, "2026-01-01T00:00:00Z", func(args string) Result {
		if strings.Contains(args, "_spawn-stdin") {
			return Result{Out: "OK pane=w:p1 job=job-a\n"}
		}
		return Result{Code: 0}
	})
	a.Config.Hosts = map[string]map[string]any{"h1": {"tags": "unity"}}
	a.Config.HostOrder = []string{"h1"}
	s.Tags = "unity"
	handled, _, e := a.placeAuto(context.Background(), s, "brief")
	if e != nil || !handled {
		t.Fatalf("%v", e)
	}
	events, e := a.events("job-a")
	if e != nil {
		t.Fatal(e)
	}
	var rec map[string]any
	for _, ev := range events {
		if ev.Kind == "job.placement" {
			rec = ev.Payload
		}
	}
	if rec == nil || rec["chose"] != "h1" || rec["source"] != "policy" {
		t.Fatalf("placement decision missing from job record: %v", rec)
	}
	if c, ok := rec["candidates"].([]any); !ok || len(c) == 0 {
		t.Fatal("candidate inputs must be recorded")
	}
}

func TestPlacementRepoConventionAndCloneOnMissing(t *testing.T) {
	src := &fakePlacementSource{hosts: []HostView{{Name: "h1", Connected: true}}}
	var cloned bool
	a, s, f := remoteFixture(t, src, &fakeWaker{}, "2026-01-01T00:00:00Z", func(args string) Result {
		switch {
		case strings.Contains(args, "test -d"):
			return Result{Code: 1}
		case strings.Contains(args, "git clone"):
			cloned = true
			return Result{}
		case strings.Contains(args, "_spawn-stdin"):
			return Result{Out: "OK pane=w:p1\n"}
		}
		return Result{}
	})
	a.Config.Hosts = map[string]map[string]any{"h1": {}}
	a.Config.HostOrder = []string{"h1"}
	handled, rec, e := a.placeAuto(context.Background(), s, "brief")
	if e != nil || !handled {
		t.Fatalf("%v", e)
	}
	if !cloned {
		t.Fatal("missing repo must be cloned before the spawn leg")
	}
	if rec["repo"] != "repo-a" {
		t.Fatalf("repo decision input must be recorded: %v", rec)
	}
	var stdinCmd *Command
	for i, c := range f.Calls {
		if c.Name == "ssh" && strings.Contains(strings.Join(c.Args, " "), "_spawn-stdin") {
			stdinCmd = &f.Calls[i]
		}
	}
	if stdinCmd == nil || !strings.Contains(stdinCmd.Input, `"~/work/repo-a"`) {
		t.Fatal("remote cwd must resolve to the repo convention")
	}
}

func TestPlacementExplicitCwdMapOverridePreserved(t *testing.T) {
	src := &fakePlacementSource{hosts: []HostView{{Name: "h1", Connected: true}}}
	var checked bool
	a, s, _ := remoteFixture(t, src, &fakeWaker{}, "2026-01-01T00:00:00Z", func(args string) Result {
		if strings.Contains(args, "test -d") {
			checked = true
		}
		if strings.Contains(args, "_spawn-stdin") {
			return Result{Out: "OK pane=w:p1\n"}
		}
		return Result{}
	})
	a.Config.Hosts = map[string]map[string]any{"h1": {"cwd_map": map[string]any{a.Home: "/srv/remote"}}}
	a.Config.HostOrder = []string{"h1"}
	handled, _, e := a.placeAuto(context.Background(), s, "brief")
	if e != nil || !handled {
		t.Fatalf("%v", e)
	}
	if checked {
		t.Fatal("cwd_map override must not trigger a repo clone probe")
	}
}

func TestPlacementAwakeHostsPreferredNoWake(t *testing.T) {
	src := &fakePlacementSource{hosts: []HostView{
		{Name: "awake-1", Connected: true},
		{Name: "desk-1", Connected: false},
	}}
	w := &fakeWaker{}
	a, s, _ := remoteFixture(t, src, w, "2026-01-01T00:00:00Z", func(args string) Result {
		if strings.Contains(args, "_spawn-stdin") {
			return Result{Out: "OK pane=w:p1\n"}
		}
		return Result{}
	})
	a.Config.Hosts = map[string]map[string]any{"awake-1": {}, "desk-1": {"wake": "panewire"}}
	a.Config.HostOrder = []string{"awake-1", "desk-1"}
	handled, rec, e := a.placeAuto(context.Background(), s, "brief")
	if e != nil || !handled {
		t.Fatalf("%v", e)
	}
	if rec["chose"] != "awake-1" {
		t.Fatalf("awake hosts come first: %v", rec)
	}
	if len(w.calls) != 0 {
		t.Fatal("no wake when an awake host accepts")
	}
}

func TestPlacementCapabilityGapWakesDesktop(t *testing.T) {
	src := &fakePlacementSource{hosts: []HostView{
		{Name: "awake-1", Connected: true},
		{Name: "desk-1", Connected: false},
	}}
	w := &fakeWaker{}
	w.onWake = func(host string) {
		for i := range src.hosts {
			if src.hosts[i].Name == host {
				src.hosts[i].Connected = true
			}
		}
	}
	a, s, _ := remoteFixture(t, src, w, "2026-01-01T00:00:00Z", func(args string) Result {
		if strings.Contains(args, "_spawn-stdin") {
			return Result{Out: "OK pane=w:p9\n"}
		}
		return Result{}
	})
	a.Config.Hosts = map[string]map[string]any{"awake-1": {"tags": "macos"}, "desk-1": {"tags": "gpu", "wake": "panewire"}}
	a.Config.HostOrder = []string{"awake-1", "desk-1"}
	s.Tags = "gpu"
	handled, rec, e := a.placeAuto(context.Background(), s, "brief")
	if e != nil || !handled {
		t.Fatalf("%v", e)
	}
	if rec["chose"] != "desk-1" {
		t.Fatalf("the woken host should take the job: %v", rec["chose"])
	}
	if len(w.calls) != 1 || w.calls[0] != "desk-1" {
		t.Fatalf("wake target wrong: %v", w.calls)
	}
	wake, ok := rec["wake"].(map[string]any)
	if !ok || wake["ok"] != true {
		t.Fatalf("wake must be recorded in the decision: %v", rec["wake"])
	}
}

func TestPlacementWakeWaitTimeoutFails(t *testing.T) {
	src := &fakePlacementSource{hosts: []HostView{
		{Name: "awake-1", Connected: true},
		{Name: "desk-1", Connected: false},
	}}
	w := &fakeWaker{}
	a, s, _ := remoteFixture(t, src, w, "2026-01-01T00:00:00Z", func(args string) Result {
		if strings.Contains(args, " true") {
			return Result{Code: 255}
		}
		return Result{}
	})
	a.Config.Hosts = map[string]map[string]any{"awake-1": {"tags": "macos"}, "desk-1": {"tags": "gpu", "wake": "panewire", "wake_wait": int64(8)}}
	a.Config.HostOrder = []string{"awake-1", "desk-1"}
	s.Tags = "gpu"
	_, _, e := a.placeAuto(context.Background(), s, "brief")
	if code(e) != 81 {
		t.Fatalf("wake that never connects must fail 81: %v", e)
	}
	if len(w.calls) != 1 {
		t.Fatalf("exactly one wake attempt expected: %v", w.calls)
	}
}

func TestPlacementAllAwakeOverThresholdWaitedWakes(t *testing.T) {
	src := &fakePlacementSource{hosts: []HostView{
		{Name: "hot-1", Connected: true, LoadRatio: ptrF(1.6)},
		{Name: "desk-1", Connected: false},
	}}
	w := &fakeWaker{}
	w.onWake = func(host string) {
		for i := range src.hosts {
			if src.hosts[i].Name == host {
				src.hosts[i].Connected = true
			}
		}
	}
	// The task just submitted: the over-threshold awake host takes it.
	a, s, _ := remoteFixture(t, src, w, "2026-01-01T00:00:00Z", func(args string) Result {
		if strings.Contains(args, "_spawn-stdin") {
			return Result{Out: "OK pane=w:p1\n"}
		}
		return Result{}
	})
	a.Config.Hosts = map[string]map[string]any{"hot-1": {}, "desk-1": {"wake": "panewire"}}
	a.Config.HostOrder = []string{"hot-1", "desk-1"}
	handled, rec, e := a.placeAuto(context.Background(), s, "brief")
	if e != nil || !handled {
		t.Fatalf("%v", e)
	}
	if rec["chose"] != "hot-1" || len(w.calls) != 0 {
		t.Fatalf("a just-submitted job goes to the awake host: %v %v", rec["chose"], w.calls)
	}
	// A task that waited past the wake_after window with every awake candidate
	// over the threshold wakes the sleeper instead.
	src.hosts[0].Connected = true
	src.hosts[1].Connected = false
	w2 := &fakeWaker{}
	w2.onWake = func(host string) {
		for i := range src.hosts {
			if src.hosts[i].Name == host {
				src.hosts[i].Connected = true
			}
		}
	}
	a2, s2, _ := remoteFixture(t, src, w2, "2025-12-31T23:30:00Z", func(args string) Result {
		if strings.Contains(args, "_spawn-stdin") {
			if strings.Contains(args, "hot-1") {
				return Result{Code: 3, Err: "full"}
			}
			return Result{Out: "OK pane=w:p2\n"}
		}
		return Result{}
	})
	a2.Config.Hosts = a.Config.Hosts
	a2.Config.HostOrder = a.Config.HostOrder
	handled, rec, e = a2.placeAuto(context.Background(), s2, "brief")
	if e != nil || !handled {
		t.Fatalf("%v", e)
	}
	if rec["chose"] != "desk-1" || len(w2.calls) != 1 {
		t.Fatalf("a long-waited job with only over-threshold hosts must wake the sleeper: %v %v", rec["chose"], w2.calls)
	}
}

func TestPlacementForcedHostUnreachableWakesOnce(t *testing.T) {
	src := &fakePlacementSource{}
	w := &fakeWaker{}
	a, s, _ := remoteFixture(t, src, w, "2026-01-01T00:00:00Z", func(args string) Result {
		if strings.Contains(args, "_spawn-stdin") {
			if strings.Contains(args, "desk-1") && len(w.calls) == 0 {
				return Result{Code: 255}
			}
			return Result{Out: "OK pane=w:p1\n"}
		}
		return Result{}
	})
	a.Config.Hosts = map[string]map[string]any{"desk-1": {"wake": "panewire"}}
	a.Config.HostOrder = []string{"desk-1"}
	s.Host = "desk-1"
	handled, _, e := a.place(context.Background(), s, "brief")
	if e != nil || !handled {
		t.Fatalf("%v", e)
	}
	if len(w.calls) != 1 || w.calls[0] != "desk-1" {
		t.Fatalf("forced host unreachable must wake once: %v", w.calls)
	}
}

func TestPlacementDegradedHostListDoesNotClassifyAsleep(t *testing.T) {
	// A failed host-list read must not mark wakeable hosts asleep: they are
	// tried like awake candidates and never woken off a missing reading.
	src := &fakePlacementSource{hostsErr: errString("hub down")}
	w := &fakeWaker{}
	a, s, _ := remoteFixture(t, src, w, "2026-01-01T00:00:00Z", func(args string) Result {
		if strings.Contains(args, "_spawn-stdin") {
			return Result{Out: "OK pane=w:p5\n"}
		}
		return Result{}
	})
	a.Config.Hosts = map[string]map[string]any{"desk-1": {"wake": "panewire"}}
	a.Config.HostOrder = []string{"desk-1"}
	handled, rec, e := a.placeAuto(context.Background(), s, "brief")
	if e != nil || !handled {
		t.Fatalf("%v", e)
	}
	if rec["chose"] != "desk-1" {
		t.Fatalf("a wakeable host unlisted under a degraded read is still a candidate: %v", rec["chose"])
	}
	if len(w.calls) != 0 {
		t.Fatalf("no wake off a missing host-list read: %v", w.calls)
	}
	degraded, _ := rec["degraded"].([]string)
	if !slices.Contains(degraded, "hosts_degraded") {
		t.Fatalf("the outage must be recorded as degraded input: %v", rec["degraded"])
	}
}

func TestPlacementDegradedHostListUnreachableNoWake(t *testing.T) {
	src := &fakePlacementSource{hostsErr: errString("hub down")}
	w := &fakeWaker{}
	a, s, _ := remoteFixture(t, src, w, "2026-01-01T00:00:00Z", func(args string) Result {
		return Result{Code: 255, Err: "no route"}
	})
	a.Config.Hosts = map[string]map[string]any{"desk-1": {"wake": "panewire"}}
	a.Config.HostOrder = []string{"desk-1"}
	_, _, e := a.placeAuto(context.Background(), s, "brief")
	if e == nil {
		t.Fatal("unreachable candidates must fail placement")
	}
	if len(w.calls) != 0 {
		t.Fatalf("an unreachable reading under a degraded host list must not wake: %v", w.calls)
	}
}

func TestPlacementOverThresholdLocalDefersToWake(t *testing.T) {
	src := &fakePlacementSource{hosts: []HostView{{Name: "desk-1", Connected: false}}}
	w := &fakeWaker{}
	w.onWake = func(host string) {
		for i := range src.hosts {
			if src.hosts[i].Name == host {
				src.hosts[i].Connected = true
			}
		}
	}
	a, s, _ := remoteFixture(t, src, w, "2025-12-31T23:30:00Z", func(args string) Result {
		if strings.Contains(args, "_spawn-stdin") {
			return Result{Out: "OK pane=w:p7\n"}
		}
		return Result{}
	})
	a.Config.Local = map[string]any{"spawn": true, "max_active": int64(0)}
	a.Config.Hosts = map[string]map[string]any{"desk-1": {"wake": "panewire"}}
	a.Config.HostOrder = []string{"desk-1"}
	handled, rec, e := a.placeAuto(context.Background(), s, "brief")
	if e != nil || !handled {
		t.Fatalf("%v", e)
	}
	if rec["chose"] != "desk-1" || len(w.calls) != 1 {
		t.Fatalf("an over-threshold local defers to the woken sleeper: %v %v", rec["chose"], w.calls)
	}
}

func TestPlacementOverThresholdLocalFallbackAfterWakeFails(t *testing.T) {
	src := &fakePlacementSource{hosts: []HostView{{Name: "desk-1", Connected: false}}}
	w := &fakeWaker{err: errString("wake refused")}
	a, s, _ := remoteFixture(t, src, w, "2025-12-31T23:30:00Z", func(args string) Result {
		return Result{}
	})
	a.Config.Local = map[string]any{"spawn": true, "max_active": int64(0)}
	a.Config.Hosts = map[string]map[string]any{"desk-1": {"wake": "panewire"}}
	a.Config.HostOrder = []string{"desk-1"}
	handled, rec, e := a.placeAuto(context.Background(), s, "brief")
	if e != nil || handled {
		t.Fatalf("a failed wake must fall back to the saturated local: handled=%v %v", handled, e)
	}
	if rec["chose"] != a.machine() {
		t.Fatalf("local is the last resort when no other candidate qualifies: %v", rec["chose"])
	}
}

func TestPlacementLocalAdmissionRefusalMovesOn(t *testing.T) {
	src := &fakePlacementSource{hosts: []HostView{{Name: "remote-1", Connected: true}}}
	a, s, _ := remoteFixture(t, src, &fakeWaker{}, "2026-01-01T00:00:00Z", func(args string) Result {
		if strings.Contains(args, "_spawn-stdin") {
			return Result{Out: "OK pane=w:p3\n"}
		}
		return Result{}
	})
	a.Config.Local = map[string]any{"spawn": true}
	a.Config.Hosts = map[string]map[string]any{"remote-1": {}}
	a.Config.HostOrder = []string{"remote-1"}
	a.Quota = &fakeQuota{LaunchErr: errString("quota refused")}
	handled, rec, e := a.placeAuto(context.Background(), s, "brief")
	if e != nil || !handled {
		t.Fatalf("%v", e)
	}
	if rec["chose"] != "remote-1" {
		t.Fatalf("a refused local must move to the next candidate: %v", rec["chose"])
	}
}
