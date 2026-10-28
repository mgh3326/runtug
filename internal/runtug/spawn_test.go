package runtug

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeQuota struct {
	LaunchValue Launch
	GateError   error
	Calls       []QuotaRequest
}

func (q *fakeQuota) Launch(_ context.Context, r QuotaRequest) (Launch, error) {
	q.Calls = append(q.Calls, r)
	return q.LaunchValue, nil
}
func (q *fakeQuota) Gate(_ context.Context, r QuotaRequest) (string, error) {
	q.Calls = append(q.Calls, r)
	return "pool=example profile=cmd-dsflash41\n", q.GateError
}
func testApp(t *testing.T) *App {
	t.Helper()
	p, e := LoadPolicy("")
	if e != nil {
		t.Fatal(e)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	root := t.TempDir()
	return &App{Runner: &fakeRunner{}, Policy: p, Config: Config{}, Home: root, Jobs: filepath.Join(root, "work", "herdr-inbox", "jobs"), Bins: map[string]string{}, Out: &bytes.Buffer{}, Err: &bytes.Buffer{}, Now: func() time.Time { return now }, Sleep: func(_ context.Context, d time.Duration) error { now = now.Add(d); return nil }}
}
func putEvent(t *testing.T, a *App, job, kind string, p map[string]any) {
	t.Helper()
	dir := filepath.Join(a.Jobs, job, "events")
	if e := os.MkdirAll(dir, 0700); e != nil {
		t.Fatal(e)
	}
	files, _ := os.ReadDir(dir)
	ev := map[string]any{"kind": kind, "payload": p, "created_at": a.Now().Add(time.Duration(len(files)) * time.Nanosecond).Format(time.RFC3339Nano)}
	b, _ := json.Marshal(ev)
	if e := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%04d-%s.json", len(files), kind)), b, 0600); e != nil {
		t.Fatal(e)
	}
}
func cmdFixture(t *testing.T) (*App, Spawn, *fakeRunner, *int) {
	t.Helper()
	a := testApp(t)
	a.Config.Local = map[string]any{"cmd_dsflash41": true, "heavy_load_wait": false}
	cwd := filepath.Join(a.Home, "tree")
	if e := os.MkdirAll(cwd, 0700); e != nil {
		t.Fatal(e)
	}
	prompt := filepath.Join(a.Home, "brief")
	if e := os.WriteFile(prompt, []byte(strings.Repeat("Please implement and verify the requested change.\n", 200)), 0600); e != nil {
		t.Fatal(e)
	}
	s := Spawn{CWD: cwd, Profile: "cmd-dsflash41", Prompt: prompt, Workspace: "workspace", Label: "worker-a", Job: "job-a", T: "T1", Task: "42", Role: "worker", Host: "local"}
	a.Quota = &fakeQuota{LaunchValue: Launch{Profile: "cmd-dsflash41", ModelID: "provider/example-model", Effort: "high", Grade: "A+", Gate: "default"}}
	launched := false
	submitted := false
	returns := 0
	token := ""
	banner := "# Command Code v1.0\n# models: example-model-(latest) with high effort · tag\n# " + cwd + "\n"
	f := &fakeRunner{}
	f.Do = func(c Command) Result {
		args := strings.Join(c.Args, " ")
		switch {
		case c.Name == "handoffkeep" && strings.HasPrefix(args, "tasks show"):
			return Result{Out: `{"id":42,"title":"implement (grade B, T1)","state":"ready","refs":{}}`}
		case c.Name == "handoffkeep" && strings.HasPrefix(args, "tasks claim"):
			return Result{Out: `{"claimed_by":"worker-a","refs":{"job_id":"job-a"}}`}
		case c.Name == "arbiter" && c.Args[0] == "claim":
			putEvent(t, a, s.Job, "job.claim", map[string]any{"owner_lane": "default", "task_id": "42", "role": "worker", "t_level": "T1"})
			return Result{}
		case c.Name == "arbiter" && c.Args[0] == "event":
			var p map[string]any
			if e := json.Unmarshal([]byte(c.Args[6]), &p); e != nil {
				t.Fatal(e)
			}
			putEvent(t, a, c.Args[2], c.Args[4], p)
			return Result{}
		case c.Name == "arbiter" && c.Args[0] == "lease":
			return Result{Out: `{"pool":"example","profile":"cmd-dsflash41","started_at":"now"}`}
		case c.Name == "arbiter" && c.Args[0] == "status":
			return Result{Out: `{"quota_pool_records":[{"pool":"example"}]}`}
		case strings.HasPrefix(args, "tab create"):
			return Result{Out: `{"result":{"root_pane":{"pane_id":"w:p1"},"tab_id":"w:t1"}}`}
		case strings.HasPrefix(args, "pane process-info"):
			if !launched {
				return Result{Out: `{"result":{"process_info":{"shell_pid":1,"foreground_processes":[{"pid":1,"argv0":"zsh"}]}}}`}
			}
			return Result{Out: `{"result":{"process_info":{"shell_pid":1,"foreground_processes":[{"pid":2,"argv0":"cmd"}]}}}`}
		case strings.HasPrefix(args, "pane run") && len(c.Args) > 3 && c.Args[3] == "printf":
			token = c.Args[len(c.Args)-1]
			return Result{}
		case strings.HasPrefix(args, "pane run"):
			launched = true
			return Result{}
		case strings.HasPrefix(args, "pane send-keys"):
			returns++
			if returns == 2 {
				submitted = true
			}
			return Result{}
		case strings.HasPrefix(args, "agent list"):
			status := "idle"
			if submitted {
				status = "working"
			}
			return Result{Out: fmt.Sprintf(`{"result":{"agents":[{"pane_id":"w:p1","tab_id":"w:t1","name":"worker-a","agent":"cmd","agent_status":%q}]}}`, status)}
		case strings.HasPrefix(args, "agent read"):
			if !launched {
				return Result{Out: token + "\n"}
			}
			if !submitted {
				return Result{Out: banner + "[Pasted text #1 +200 lines]\n"}
			}
			return Result{Out: banner + "Working on the requested change\n"}
		}
		return Result{}
	}
	a.Runner = f
	return a, s, f, &returns
}
func TestCmdLongBriefSubmissionRequiresStatusAndRetriesOnce(t *testing.T) {
	a, s, f, returns := cmdFixture(t)
	if e := a.Spawn(context.Background(), s); e != nil {
		t.Fatal(e)
	}
	if *returns != 2 {
		t.Fatalf("submit keys=%d", *returns)
	}
	pastes := 0
	for _, c := range f.Calls {
		if len(c.Args) > 1 && c.Args[0] == "pane" && c.Args[1] == "send-text" {
			pastes++
			if !strings.Contains(c.Args[3], "Host safety (fleet-v1)") {
				t.Fatal("safety preamble omitted")
			}
		}
	}
	if pastes != 1 {
		t.Fatalf("pastes=%d", pastes)
	}
	if !strings.Contains(a.Out.(*bytes.Buffer).String(), "landed=yes") {
		t.Fatal("success absent")
	}
}
func TestSpawnSubmissionFailureCleansPaneAndLease(t *testing.T) {
	a, s, f, _ := cmdFixture(t)
	original := f.Do
	f.Do = func(c Command) Result {
		r := original(c)
		if strings.HasPrefix(strings.Join(c.Args, " "), "agent list") {
			r.Out = strings.ReplaceAll(r.Out, `"working"`, `"idle"`)
		}
		return r
	}
	e := a.Spawn(context.Background(), s)
	if code(e) != 76 {
		t.Fatalf("%v", e)
	}
	calls := ""
	for _, c := range f.Calls {
		calls += strings.Join(c.Args, " ") + "\n"
	}
	if !strings.Contains(calls, "pane close w:p1") || !strings.Contains(calls, "release --job job-a") {
		t.Fatal("cleanup missing")
	}
	if strings.Contains(calls, "tasks claim") || strings.Contains(calls, "job.spawned") {
		t.Fatal("failed spawn committed")
	}
}
func TestSpawnDryRunShowsPreambleWithoutSideEffects(t *testing.T) {
	a, s, f, _ := cmdFixture(t)
	s.DryRun = true
	if e := a.Spawn(context.Background(), s); e != nil {
		t.Fatal(e)
	}
	if len(f.Calls) != 0 {
		t.Fatalf("dry run executed %d commands", len(f.Calls))
	}
	if !strings.Contains(a.Out.(*bytes.Buffer).String(), "fleet-v1") {
		t.Fatal("preamble missing")
	}
}
func TestR12ArbiterIsSoleLedger(t *testing.T) {
	a, s, f, _ := cmdFixture(t)
	if e := a.Spawn(context.Background(), s); e != nil {
		t.Fatal(e)
	}
	seen := map[string]bool{}
	for _, c := range f.Calls {
		if c.Name == "arbiter" {
			seen[c.Args[0]] = true
		}
	}
	for _, name := range []string{"claim", "lease", "event"} {
		if !seen[name] {
			t.Fatal("missing arbiter " + name)
		}
	}
	if _, e := os.Stat(filepath.Join(a.Home, ".local", "share", "runtug", "state.db")); !os.IsNotExist(e) {
		t.Fatal("runtug must not create a ledger")
	}
}
func TestTaskClaimMustConfirmAtomicBinding(t *testing.T) {
	a, s, f, _ := cmdFixture(t)
	original := f.Do
	f.Do = func(c Command) Result {
		if c.Name == "handoffkeep" && c.Args[1] == "claim" {
			return Result{Out: `{"claimed_by":"worker-a","refs":{}}`}
		}
		return original(c)
	}
	if code(a.Spawn(context.Background(), s)) != 70 {
		t.Fatal("half-record admitted")
	}
}
