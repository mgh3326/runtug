package runtug

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

type Spawn struct {
	CWD, Profile, Prompt, Workspace, Label, T, Job, Owner, Effort, Role, Lane, Parent, Task, ParentJob, Host, Report, ExecutionLane, OperatorRequest, RequestedBy, Purpose, LaneDefer, ReportRelative string
	Keep, DupOK, Bypass, DryRun                                                                                                                                                                       bool
	Inherited, ParentFamily                                                                                                                                                                           string
}

func parseSpawn(args []string) (Spawn, error) {
	s := Spawn{Role: "worker", Host: "auto"}
	f := flags("spawn")
	for _, p := range []struct {
		k string
		v *string
	}{{"c", &s.CWD}, {"m", &s.Profile}, {"profile", &s.Profile}, {"p", &s.Prompt}, {"w", &s.Workspace}, {"l", &s.Label}, {"t", &s.T}, {"job", &s.Job}, {"owner", &s.Owner}, {"effort", &s.Effort}, {"role", &s.Role}, {"lane", &s.Lane}, {"parent", &s.Parent}, {"task", &s.Task}, {"parent-job", &s.ParentJob}, {"host", &s.Host}, {"report", &s.Report}, {"L", &s.ExecutionLane}, {"operator-request", &s.OperatorRequest}, {"requested-by", &s.RequestedBy}, {"purpose", &s.Purpose}, {"lane-defer", &s.LaneDefer}, {"report-relative", &s.ReportRelative}} {
		f.StringVar(p.v, p.k, *p.v, "")
	}
	f.BoolVar(&s.Keep, "keep", false, "")
	f.BoolVar(&s.DupOK, "job-dup-ok", false, "")
	f.BoolVar(&s.Bypass, "task-hk-bypass", false, "")
	f.BoolVar(&s.DryRun, "dry-run", false, "")
	if e := f.Parse(args); e != nil {
		return s, fail(2, "%v", e)
	}
	if f.NArg() != 0 {
		return s, fail(2, "unexpected spawn arguments")
	}
	if s.Job == "" {
		s.Job = s.Label
	}
	if s.Role == "captain" {
		s.Role = "builder"
	}
	return s, nil
}

type Task struct {
	ID        any            `json:"id"`
	Title     string         `json:"title"`
	State     string         `json:"state"`
	ClaimedBy string         `json:"claimed_by"`
	Refs      map[string]any `json:"refs"`
}

func (a *App) taskPrecheck(ctx context.Context, s Spawn) (Task, bool, error) {
	r := a.call(ctx, "handoffkeep", "tasks", "show", s.Task)
	var t Task
	if r.Code != 0 {
		if strings.Contains(r.Out+r.Err, "not_found") {
			return t, false, fail(2, "task does not exist")
		}
		if s.Bypass {
			return t, true, nil
		}
		return t, false, fail(70, "task precheck unavailable: %s", r.Err)
	}
	if e := json.Unmarshal([]byte(r.Out), &t); e != nil || t.State == "" {
		return t, false, fail(70, "unreadable task precheck")
	}
	if t.ID != nil && scalar(t.ID) != s.Task {
		return t, false, fail(70, "task response ID mismatch")
	}
	if s.Inherited != "" {
		return t, false, nil
	}
	if slices.Contains([]string{"merged", "dropped", "hold", "needs_decision"}, t.State) {
		return t, false, fail(2, "task unavailable: %s", t.State)
	}
	if j := str(t.Refs, "job_id"); j != "" && j != s.Job {
		return t, false, fail(2, "task bound to another job")
	}
	if str(t.Refs, "job_id") == "" && t.ClaimedBy != "" && t.ClaimedBy != s.Label {
		return t, false, fail(2, "task has another claimant without a job binding")
	}
	return t, false, nil
}
func (a *App) taskLink(ctx context.Context, s Spawn, bypassed bool) error {
	status := "inherited"
	if s.Inherited == "" {
		r := a.call(ctx, "handoffkeep", "tasks", "claim", s.Task, "--by", s.Label, "--job-id", s.Job)
		var t Task
		if r.Code != 0 {
			if !bypassed || !s.Bypass || strings.Contains(r.Out+r.Err, "conflict") || strings.Contains(r.Out+r.Err, "refused") {
				return fail(70, "task claim refused: %s", r.Err)
			}
			status = "bypassed"
		} else {
			if e := json.Unmarshal([]byte(r.Out), &t); e != nil || t.ClaimedBy != s.Label || str(t.Refs, "job_id") != s.Job {
				return fail(70, "task claim did not confirm claimed_by and refs.job_id")
			}
			status = "linked"
		}
	}
	return a.event(ctx, s.Job, "job.task_link", map[string]any{"task_id": s.Task, "job_id": s.Job, "claimed_by": s.Label, "status": status, "inherited_from": s.Inherited})
}
func (a *App) Spawn(ctx context.Context, s Spawn) (err error) {
	if s.CWD == "" || s.Profile == "" || s.Prompt == "" || s.Workspace == "" || s.Label == "" || !slices.Contains([]string{"T0", "T1", "T2", "T3"}, s.T) {
		return fail(2, "spawn requires -c -m -p -w -l --t T0..T3")
	}
	if !validID(s.Job) || !validID(s.Label) || !slices.Contains([]string{"worker", "builder", "tester", "reviewer", "counsel"}, s.Role) {
		return fail(2, "invalid job, label or role")
	}
	if s.Role == "builder" && (s.Lane == "" || s.Parent == "" || s.Owner != "" || s.Task == "") {
		return fail(2, "builder requires --lane --parent --task; --owner is not accepted")
	}
	if s.LaneDefer != "" && (s.Role != "builder" || !validID(s.LaneDefer)) {
		return fail(2, "--lane-defer requires builder and a host name")
	}
	if s.Task != "" && s.ParentJob != "" {
		return fail(2, "--task and --parent-job are exclusive")
	}
	if s.ParentJob == "" && s.Task == "" {
		s.ParentJob = os.Getenv("ARBITER_JOB")
		if s.ParentJob == "" {
			s.Task = os.Getenv("HK_TASK_ID")
		}
	}
	builderFamily := s.ParentFamily
	if s.ParentJob != "" {
		j, e := a.job(s.ParentJob)
		if e != nil {
			return fail(75, "parent metadata unreadable: %v", e)
		}
		s.Task = j.Task
		s.Inherited = s.ParentJob
		builderFamily = j.Family
		if builderFamily == "" {
			if seat, ok := a.Policy.Profiles[j.Profile]; ok {
				builderFamily = seat.Family
			}
		}
		if s.Owner == "" {
			s.Owner = j.Owner
		}
		s.ParentFamily = builderFamily
		if s.Report == "" && s.ReportRelative == "" {
			return fail(2, "--parent-job requires --report")
		}
	}
	if !regexp.MustCompile(`^[0-9]+$`).MatchString(s.Task) {
		return fail(2, "spawn requires a numeric task binding")
	}
	brief, e := os.ReadFile(expand(s.Prompt))
	if e != nil {
		return e
	}
	if len(strings.TrimSpace(string(brief))) == 0 {
		return fail(2, "brief is empty")
	}
	if s.Host != "local" {
		handled, e := a.place(ctx, s, string(brief))
		if handled || e != nil {
			return e
		}
	}
	payload, e := a.prepareBrief(s, string(brief))
	if e != nil {
		return e
	}
	if s.DryRun {
		fmt.Fprintf(a.Out, "dry-run host=%s profile=%s role=%s job=%s\n%s\n", a.machine(), s.Profile, s.Role, s.Job, payload)
		return nil
	}
	if !boolean(a.Config.Local, "spawn", true) {
		return fail(80, "local spawns disabled")
	}
	cwd, e := filepath.Abs(expand(s.CWD))
	if e != nil {
		return e
	}
	s.CWD = cwd
	if s.ExecutionLane != "" {
		if s.ExecutionLane != "live" && s.ExecutionLane != "mock" {
			return fail(2, "-L must be live or mock")
		}
		if info, e := os.Stat(filepath.Join(cwd, s.ExecutionLane)); e == nil && info.IsDir() {
			s.CWD = filepath.Join(cwd, s.ExecutionLane)
		}
	}
	if info, e := os.Stat(s.CWD); e != nil || !info.IsDir() {
		return fail(2, "cwd is not a directory")
	}
	if s.ReportRelative != "" {
		if filepath.IsAbs(s.ReportRelative) || strings.Contains(s.ReportRelative, "..") {
			return fail(2, "unsafe report-relative")
		}
		s.Report = filepath.Join(a.Jobs, s.ReportRelative)
	}
	if s.Report != "" {
		if e := safeReport(a.Jobs, s.Job, firstNonempty(s.ParentJob, s.Inherited), s.Report); e != nil {
			return e
		}
	}
	seat, ok := a.Policy.Profiles[s.Profile]
	if !ok {
		return fail(70, "missing seat policy for %s", s.Profile)
	}
	effort := s.Effort
	if effort == "" {
		effort = seat.Effort
	}
	if seat.Pin && effort != seat.Effort {
		return fail(77, "profile pins effort %s", seat.Effort)
	}
	task, bypassed, e := a.taskPrecheck(ctx, s)
	if e != nil {
		return e
	}
	grade := ""
	if m := taskMarker.FindStringSubmatch(task.Title); m != nil {
		grade = strings.ToUpper(m[1])
		if strings.ToUpper(m[2]) != s.T {
			return fail(77, "task scale does not match --t")
		}
	} else if !bypassed {
		return fail(77, "task title lacks (grade X, Tn marker")
	}
	qr := QuotaRequest{Profile: seat.Canon, Effort: effort, Role: s.Role, OperatorRequest: s.OperatorRequest, RequestedBy: s.RequestedBy, Purpose: s.Purpose}
	launch, e := a.Quota.Launch(ctx, qr)
	if e != nil {
		return e
	}
	if effort != "" && launch.Effort != effort {
		return fail(70, "catalog effort does not match request")
	}
	if launch.Profile != seat.Canon {
		return fail(70, "catalog profile mismatch")
	}
	if e = seat.Check(launch, s.Role, grade, builderFamily, os.Getenv("SCOPEFUEL_E6_ARM"), a.Config.Local); e != nil {
		return e
	}
	qr.Effort = launch.Effort
	mockEnv, e := a.mock(ctx, s, seat)
	if e != nil {
		return e
	}
	gate, e := a.Quota.Gate(ctx, qr)
	if e != nil {
		return e
	}
	if e = a.hubGate(ctx, s.CWD, gate); e != nil {
		return e
	}
	owner := s.Owner
	if owner == "" {
		owner = "default"
	}
	if s.Role == "builder" {
		owner = s.Lane
	}
	claim := []string{"claim", "--job", s.Job, "--agent-label", s.Label, "--lane", owner, "--t", s.T, "--role", "worker", "--task-id", s.Task}
	if s.Role == "builder" {
		claim[10] = "builder"
		claim = append(claim, "--parent-lane", s.Parent)
	}
	if s.Inherited != "" {
		claim = append(claim, "--task-inherited-from", s.Inherited)
	}
	if s.Bypass {
		claim = append(claim, "--task-link-override", "declared")
	}
	// The arbiter arbitrates concurrent duplicate claims; never infer a free job from pane status.
	r := a.call(ctx, "arbiter", append(claim, "--json")...)
	if r.Code == 4 {
		d, de := a.durable(ctx, s.Job)
		if de != nil {
			return fail(75, "duplicate job state unreadable")
		}
		if d.State == "released" {
			r = a.call(ctx, "arbiter", append(claim, "--reclaim-released", "--json")...)
		} else if !s.DupOK {
			return fail(74, "active duplicate job %s", s.Job)
		} else {
			a.warn("--job-dup-ok used for %s", s.Job)
			if e = a.event(ctx, s.Job, "job.duplicate_override", map[string]any{"label": s.Label}); e != nil {
				return e
			}
			r = Result{}
		}
	}
	if e = check(r, "arbiter claim"); e != nil {
		return e
	}
	committed := false
	pane := ""
	pool := ""
	defer func() {
		if !committed {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			if pane != "" {
				a.captureFailure(cleanup, s.Job, pane)
				_ = a.must(cleanup, "herdr", "pane", "close", pane)
			}
			if pool != "" {
				_ = a.must(cleanup, "arbiter", "release", "--job", s.Job, "--resource", pool, "--kind", "quota_pool")
			}
		}
	}()
	gateFile, e := os.CreateTemp("", "runtug-gate-*")
	if e != nil {
		return e
	}
	defer os.Remove(gateFile.Name())
	if _, e = gateFile.WriteString(gate); e != nil {
		gateFile.Close()
		return e
	}
	if e = gateFile.Close(); e != nil {
		return e
	}
	r = a.call(ctx, "arbiter", "lease", "--job", s.Job, "--kind", "quota_pool", "--profile", seat.Canon, "--launch-profile", seat.Canon+"@"+launch.Effort, "--gate-output", gateFile.Name(), "--json")
	if e = check(r, "arbiter lease"); e != nil {
		return e
	}
	var lease map[string]any
	if e = json.Unmarshal([]byte(r.Out), &lease); e != nil {
		_ = a.releaseQuota(ctx, Job{ID: s.Job})
		return e
	}
	pool = str(lease, "pool")
	if pool == "" {
		_ = a.releaseQuota(ctx, Job{ID: s.Job})
		return fail(70, "lease missing pool")
	}
	argv, env, e := a.launchArgs(seat, launch)
	if e != nil {
		return e
	}
	for _, v := range mockEnv {
		if path, ok := strings.CutPrefix(v, "RUNTUG_MOCK_CONFIG="); ok {
			argv = append(argv, "--strict-mcp-config", "--mcp-config", path)
		}
	}
	env = append(env, mockEnv...)
	env = append(env, "ARBITER_JOB="+s.Job, "ARBITER_RESOURCE="+pool, "ARBITER_KIND=quota_pool", "ARBITER_PROFILE="+seat.Canon, "HK_TASK_ID="+s.Task)
	if seat.Kind == "claude" && s.Purpose != "director" {
		env = append(env, "CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION=false")
	}
	tabArgs := []string{"tab", "create", "--workspace", s.Workspace, "--cwd", s.CWD, "--label", s.Label, "--no-focus"}
	for _, v := range env {
		tabArgs = append(tabArgs, "--env", v)
	}
	r = a.call(ctx, "herdr", tabArgs...)
	if e = check(r, "herdr tab create"); e != nil {
		return e
	}
	var created map[string]any
	if e = json.Unmarshal([]byte(r.Out), &created); e != nil {
		return e
	}
	created = unwrap(created)
	root, _ := created["root_pane"].(map[string]any)
	pane = str(root, "pane_id")
	tab := str(created, "tab_id")
	if pane == "" {
		return fail(70, "tab create omitted pane_id")
	}
	if e = a.start(ctx, s, seat, launch, pane, argv); e != nil {
		return e
	}
	if e = a.deliver(ctx, seat.Kind, pane, payload); e != nil {
		return e
	}
	if seat.Kind == "pi" || seat.Kind == "cmd" {
		if e = a.modelProof(ctx, seat, launch, s.CWD, pane, payload); e != nil {
			return e
		}
	}
	if e = a.taskLink(ctx, s, bypassed); e != nil {
		return e
	}
	lane := s.Label
	parent := owner
	if s.Role == "builder" {
		lane = s.Lane
		parent = s.Parent
	}
	machine := a.machine()
	laneOK := s.LaneDefer == "" && a.registerLane(ctx, lane, parent, pane, s.Role, machine) == nil
	receipt := map[string]any{"pane_id": pane, "tab_id": tab, "label": s.Label, "profile": s.Profile, "launch_profile": seat.Canon + "@" + launch.Effort, "model_id": launch.ModelID, "effort": launch.Effort, "family": seat.Family, "role": s.Role, "lane_role": s.Role, "owner_lane": owner, "parent_lane": s.Parent, "task_id": s.Task, "report": s.Report, "cwd": s.CWD, "keep": s.Keep, "pool": pool, "host": machine}
	if laneOK {
		receipt["hub_lane"] = lane
		receipt["hub_lane_machine"] = machine
	}
	if e = a.event(ctx, s.Job, "job.spawned", receipt); e != nil {
		return e
	}
	committed = true
	if s.Report != "" {
		if e = a.startWatch(s.Job); e != nil {
			a.warn("completion watcher failed: %v", e)
		}
	}
	laneStatus := ""
	if laneOK {
		laneStatus = " lane=" + lane + "@" + machine
	} else if s.Role == "builder" {
		laneStatus = " lane=UNREGISTERED"
	}
	fmt.Fprintf(a.Out, "OK pane=%s model=%s label=%s status=working landed=yes job=%s quota_record=%s/quota_pool%s\n", pane, s.Profile, s.Label, s.Job, pool, laneStatus)
	if s.Role == "builder" && !laneOK && os.Getenv("WRK_LANE_STRICT") == "1" {
		return fail(82, "builder pane exists but lane is UNREGISTERED")
	}
	return nil
}
func firstNonempty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
func unwrap(m map[string]any) map[string]any {
	if r, ok := m["result"].(map[string]any); ok {
		return r
	}
	return m
}
func (a *App) machine() string {
	for _, m := range []map[string]any{a.Config.Local, a.Config.Hub} {
		if v := str(m, "machine_id"); v != "" {
			return v
		}
	}
	return "local"
}
func (a *App) captureFailure(ctx context.Context, job, pane string) {
	r := a.call(ctx, "herdr", "agent", "read", pane, "--source", "visible", "--lines", "80")
	if r.Code != 0 {
		return
	}
	dir := filepath.Join(a.Jobs, job)
	if os.MkdirAll(dir, 0700) != nil {
		return
	}
	f, e := os.CreateTemp(dir, "spawn-failure-*.txt")
	if e == nil {
		_, _ = f.WriteString(r.Out)
		_ = f.Close()
	}
}
func safeReport(root, job, parent, path string) error {
	if !filepath.IsAbs(path) {
		return fail(2, "report must be absolute")
	}
	for _, id := range []string{job, parent} {
		if id != "" && within(filepath.Join(root, id), path) {
			return noSymlinks(path)
		}
	}
	return fail(2, "report must be under the job or parent job directory")
}
func within(root, path string) bool {
	rel, e := filepath.Rel(root, path)
	return e == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}
func noSymlinks(path string) error {
	for p := filepath.Clean(path); p != filepath.Dir(p); p = filepath.Dir(p) {
		s, e := os.Lstat(p)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return e
		}
		if s.Mode()&os.ModeSymlink != 0 {
			return fail(2, "symlink path refused: %s", p)
		}
	}
	return nil
}
