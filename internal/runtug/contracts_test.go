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

func ledgerRunner(t *testing.T, a *App) *fakeRunner {
	t.Helper()
	f := &fakeRunner{}
	f.Do = func(c Command) Result {
		if c.Name == "arbiter" && len(c.Args) > 0 {
			switch c.Args[0] {
			case "event":
				var p map[string]any
				if e := json.Unmarshal([]byte(c.Args[6]), &p); e != nil {
					t.Fatal(e)
				}
				putEvent(t, a, c.Args[2], c.Args[4], p)
			case "status":
				return Result{Out: `{"quota_pool_records":[{"pool":"example"}]}`}
			}
		}
		if c.Name == "herdr" && strings.Join(c.Args, " ") == "agent list" {
			return Result{Out: `{"result":{"agents":[]}}`}
		}
		return Result{}
	}
	a.Runner = f
	return f
}
func seedJob(t *testing.T, a *App, id, role string) {
	putEvent(t, a, id, "job.claim", map[string]any{"owner_lane": "owner", "parent_lane": "parent", "task_id": "42", "t_level": "T1", "role": role})
	putEvent(t, a, id, "job.spawned", map[string]any{"pane_id": "w:p1", "tab_id": "w:t1", "label": "agent-a", "profile": "cmd-dsflash41", "family": "deepseek", "keep": false})
}
func TestR07ReapProtectsBuildersKeepAndRevivedJobs(t *testing.T) {
	for _, tt := range []struct {
		name, role            string
		keep, revive, include bool
		wantClose             bool
	}{{"worker", "worker", false, false, false, true}, {"builder default", "builder", false, false, false, false}, {"builder opt in", "builder", false, false, true, true}, {"keep", "worker", true, false, true, false}, {"revived", "worker", false, true, true, false}} {
		t.Run(tt.name, func(t *testing.T) {
			a := testApp(t)
			f := ledgerRunner(t, a)
			seedJob(t, a, "j", tt.role)
			putEvent(t, a, "j", "job.completed", map[string]any{})
			if tt.keep {
				putEvent(t, a, "j", "job.spawned", map[string]any{"pane_id": "w:p1", "keep": true})
			}
			if tt.revive {
				putEvent(t, a, "j", "job.reclaim", map[string]any{"owner_lane": "owner", "task_id": "42"})
			}
			args := []string{"--job", "j", "--grace", "0s", "--apply"}
			if tt.include {
				args = append(args, "--include-builders")
			}
			a.Now = func() time.Time { return time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC) }
			if e := a.hygiene(context.Background(), "reap", args); e != nil {
				t.Fatal(e)
			}
			closed := false
			for _, c := range f.Calls {
				if strings.HasPrefix(strings.Join(c.Args, " "), "pane close") {
					closed = true
				}
			}
			if closed {
				t.Fatal("absent pane should not receive close")
			}
			reaped := false
			for _, c := range f.Calls {
				if strings.Contains(strings.Join(c.Args, " "), "--kind job.reaped") {
					reaped = true
				}
			}
			if reaped != tt.wantClose {
				t.Fatalf("reaped=%t want %t", reaped, tt.wantClose)
			}
		})
	}
}
func TestR07KernelLockAndSecureFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "slot")
	a, e := secureOpen(path, true)
	if e != nil {
		t.Fatal(e)
	}
	if !tryLock(a) {
		t.Fatal("first lock failed")
	}
	b, e := secureOpen(path, false)
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	if tryLock(b) {
		t.Fatal("concurrent holder admitted")
	}
	a.Close()
	if !tryLock(b) {
		t.Fatal("dead holder did not release")
	}
	link := path + ".link"
	if e = os.Symlink(path, link); e != nil {
		t.Fatal(e)
	}
	if _, e = secureOpen(link, false); e == nil {
		t.Fatal("symlink lock accepted")
	}
}
func TestR08RestoreVerifiesExistingReleasedPane(t *testing.T) {
	a := testApp(t)
	f := ledgerRunner(t, a)
	seedJob(t, a, "j", "worker")
	original := f.Do
	f.Do = func(c Command) Result {
		switch strings.Join(c.Args, " ") {
		case "job-get --job j --json":
			return Result{Out: `{"state":"released"}`}
		case "agent list":
			return Result{Out: `{"result":{"agents":[{"pane_id":"w:p1","tab_id":"w:t1","name":"agent-a","agent_status":"working"}]}}`}
		}
		return original(c)
	}
	if e := a.lifecycle(context.Background(), "restore", []string{"j"}); e != nil {
		t.Fatal(e)
	}
	found := false
	for _, c := range f.Calls {
		s := strings.Join(c.Args, " ")
		if strings.Contains(s, "--reclaim-released") {
			found = true
		}
		if strings.HasPrefix(s, "tab create") {
			t.Fatal("restore created a duplicate pane")
		}
	}
	if !found {
		t.Fatal("no arbiter reclaim")
	}
}
func TestDoneDeduplicatesAndSnapshotsChangedReport(t *testing.T) {
	a := testApp(t)
	f := ledgerRunner(t, a)
	seedJob(t, a, "j", "worker")
	path := filepath.Join(a.Jobs, "j", "report.md")
	if e := os.WriteFile(path, []byte("first\n"), 0600); e != nil {
		t.Fatal(e)
	}
	for n := 0; n < 2; n++ {
		if e := a.lifecycle(context.Background(), "done", []string{"j", "--report", path}); e != nil {
			t.Fatal(e)
		}
	}
	if e := os.WriteFile(path, []byte("second\n"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := a.lifecycle(context.Background(), "done", []string{"j", "--report", path}); e != nil {
		t.Fatal(e)
	}
	emits := 0
	for _, c := range f.Calls {
		if c.Name == "panewire" {
			emits++
		}
	}
	if emits != 2 {
		t.Fatalf("emits=%d", emits)
	}
	if b, e := os.ReadFile(filepath.Join(a.Jobs, "j", "report-r2.md")); e != nil || string(b) != "second\n" {
		t.Fatalf("snapshot %q %v", b, e)
	}
}
func TestR01R09R10R11ExternalServicesRemainExternal(t *testing.T) {
	for _, cmd := range []string{"assistant", "session-reap", "attach", "hk-desk", "joined", "round", "o3", "tester-eligible"} {
		t.Run(cmd, func(t *testing.T) {
			a := testApp(t)
			f := a.Runner.(*fakeRunner)
			if code(a.Run(context.Background(), []string{cmd})) != 2 {
				t.Fatal("unexpected replacement of external command")
			}
			if len(f.Calls) != 0 {
				t.Fatal("external mutation")
			}
		})
	}
}
func TestR03PlacementReadsCanonicalMachineAndFiltersDisconnected(t *testing.T) {
	raw := `{"decision":"host-b","candidates":[{"machine":"host-a","connected":false},{"machine":"host-b","connected":true},{"host":"host-c","reason":"not_accepting"},"host-d"]}`
	views, e := parseHubHosts(raw)
	if e != nil {
		t.Fatal(e)
	}
	if len(views) != 4 {
		t.Fatalf("expected 4 candidate views, got %v", views)
	}
	if views[0].Connected || !views[1].Connected || views[2].Reason != "not_accepting" {
		t.Fatalf("connectivity and acceptance must be preserved: %+v", views)
	}
	awake := []string{}
	for _, v := range views {
		if v.Connected && !strings.Contains(v.Reason, "not_accepting") {
			awake = append(awake, v.Name)
		}
	}
	if strings.Join(awake, ",") != "host-b,host-d" {
		t.Fatalf("%v", views)
	}
}
func TestR04TaskStateAndBindingUseCLIResponse(t *testing.T) {
	for _, state := range []string{"merged", "dropped", "hold", "needs_decision"} {
		t.Run(state, func(t *testing.T) {
			a := testApp(t)
			a.Runner = &fakeRunner{Do: func(Command) Result {
				return Result{Out: fmt.Sprintf(`{"state":%q,"title":"task (grade B, T1)","refs":{}}`, state)}
			}}
			_, _, e := a.taskPrecheck(context.Background(), Spawn{Task: "42", Job: "j", Label: "agent"})
			if e == nil {
				t.Fatal("unavailable task accepted")
			}
		})
	}
}
func TestR05MergewakePolicyShadowAndNoLegacyPins(t *testing.T) {
	b, e := os.ReadFile("../../.mergewake/policy.yaml")
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Contains(b, []byte("enabled: false")) {
		t.Fatal("mergewake is not shadow")
	}
	for _, name := range []string{"gate_policy.json", "verdict.json"} {
		if _, e := os.Stat("../../" + name); !os.IsNotExist(e) {
			t.Fatal("legacy review state should not be created")
		}
	}
}
func TestR06R13QuotaPoolAndManualFallbackStayInQuotaTool(t *testing.T) {
	f := &fakeRunner{Do: func(Command) Result { return Result{Out: "pool=operator-manual-provider source=manual\n"} }}
	q := CLIQuota{f, "quota"}
	out, e := q.Gate(context.Background(), QuotaRequest{Profile: "external-profile", Effort: "high", Role: "worker"})
	if e != nil || !strings.Contains(out, "operator-manual-provider") {
		t.Fatalf("%s %v", out, e)
	}
	if len(f.Calls) != 1 {
		t.Fatal("runtug must not replace manual fallback")
	}
}
func TestR14MockIsolationRequiresExclusiveDescriptor(t *testing.T) {
	a := testApp(t)
	dir := t.TempDir()
	config := filepath.Join(dir, "mcp.json")
	t.Setenv("MOCK_MCP_PROFILE", "reviewed")
	t.Setenv("RUNTUG_MOCK_MCP_CONFIG", config)
	for _, body := range []string{`{"mcpServers":{"x":{"url":"https://example.invalid"}}}`, `{"mcpServers":{"x":{"command":"relative"}}}`, `{"mcpServers":{}}`} {
		if e := os.WriteFile(config, []byte(body), 0600); e != nil {
			t.Fatal(e)
		}
		if _, e := a.mock(context.Background(), Spawn{Job: "j", CWD: dir, ExecutionLane: "mock"}, Seat{Kind: "claude"}); code(e) != 79 {
			t.Fatalf("unsafe mock accepted: %v", e)
		}
	}
}
func TestTrustGuardRejectsWritableBinary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tool")
	if e := os.WriteFile(path, []byte("fixture"), 0777); e != nil {
		t.Fatal(e)
	}
	if e := os.Chmod(path, 0777); e != nil {
		t.Fatal(e)
	}
	if code(TrustBinary(path, uint32(os.Getuid()))) != 70 {
		t.Fatal("writable binary accepted")
	}
	if e := os.Chmod(path, 0700); e != nil {
		t.Fatal(e)
	}
	if e := TrustBinary(path, uint32(os.Getuid())); e != nil {
		t.Fatal(e)
	}
}

func TestLockOwnerRule(t *testing.T) {
	const me = 1000
	for _, c := range []struct {
		uid, gid uint32
		perm     os.FileMode
		ok       bool
	}{
		{me, me, 0600, true},
		{me, me, 0660, false},
		{me, me, 0606, false},
		{0, 0, 0660, true}, // PA desktop shared lock (tmpfiles + ACL)
		{0, 0, 0600, true},
		{0, 0, 0666, false}, // other-writable
		{0, 5, 0660, false}, // root-owned but foreign group
		{2000, 2000, 0600, false},
	} {
		if got := lockOwnerOK(c.uid, c.gid, c.perm, me); got != c.ok {
			t.Fatalf("uid=%d gid=%d perm=%o: got %v want %v", c.uid, c.gid, c.perm, got, c.ok)
		}
	}
}
