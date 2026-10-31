package runtug

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/mgh3326/runtug/internal/arbiter"
)

type Spawn struct {
	CWD, Profile, Prompt, Workspace, Label, T, Job, Owner, Effort, Role, Lane, Parent, Task, ParentJob, Host, Report, ExecutionLane, OperatorRequest, RequestedBy, Purpose, LaneDefer, ReportRelative string
	OverrideReason, WaitTimeout, Urgency                                                                                                                                                              string
	Keep, DupOK, Bypass, DryRun, Wait, AllowEscalation                                                                                                                                                bool
	Inherited, ParentFamily                                                                                                                                                                           string
	// Tags lists the capability tags a host must carry and ExcludeHosts the
	// hosts placement must not pick. Delegated marks a spawn delivered by
	// another initiator. Sel carries the selection decision record; all four
	// are initiator-local and never cross the delegation wire.
	Tags         string `json:"-"`
	ExcludeHosts string `json:"-"`
	// BuilderFamily and BuilderJob name the author's family and job for a
	// reviewer or tester whose builder job ran on another host, so its
	// metadata is not readable here (--parent-job needs local metadata).
	BuilderFamily string         `json:"-"`
	BuilderJob    string         `json:"-"`
	Delegated     bool           `json:"-"`
	Sel           map[string]any `json:"-"`
	// Grade is the task grade a caller states itself (`runtug delegate`),
	// used instead of the "(grade X, Tn" marker in the bound task's title.
	Grade string `json:"-"`
	// ExcludeFamilies and OnlyFamily narrow automatic selection to seats of
	// other families, or of one family (`runtug delegate --panel`).
	ExcludeFamilies []string `json:"-"`
	OnlyFamily      string   `json:"-"`
}

func parseSpawn(args []string) (Spawn, error) {
	s := Spawn{Role: "worker", Host: "auto"}
	f := flags("spawn")
	for _, p := range []struct {
		k string
		v *string
	}{{"c", &s.CWD}, {"m", &s.Profile}, {"profile", &s.Profile}, {"p", &s.Prompt}, {"w", &s.Workspace}, {"l", &s.Label}, {"t", &s.T}, {"job", &s.Job}, {"owner", &s.Owner}, {"effort", &s.Effort}, {"role", &s.Role}, {"lane", &s.Lane}, {"parent", &s.Parent}, {"task", &s.Task}, {"parent-job", &s.ParentJob}, {"host", &s.Host}, {"report", &s.Report}, {"L", &s.ExecutionLane}, {"operator-request", &s.OperatorRequest}, {"requested-by", &s.RequestedBy}, {"purpose", &s.Purpose}, {"lane-defer", &s.LaneDefer}, {"report-relative", &s.ReportRelative}, {"tags", &s.Tags}, {"exclude-host", &s.ExcludeHosts}, {"override-reason", &s.OverrideReason}, {"wait-timeout", &s.WaitTimeout}, {"urgency", &s.Urgency}, {"builder-family", &s.BuilderFamily}, {"builder-job", &s.BuilderJob}} {
		f.StringVar(p.v, p.k, *p.v, "")
	}
	f.BoolVar(&s.Keep, "keep", false, "")
	f.BoolVar(&s.DupOK, "job-dup-ok", false, "")
	f.BoolVar(&s.Bypass, "task-hk-bypass", false, "")
	f.BoolVar(&s.DryRun, "dry-run", false, "")
	f.BoolVar(&s.Wait, "wait", false, "")
	f.BoolVar(&s.AllowEscalation, "allow-escalation", false, "")
	if e := f.Parse(args); e != nil {
		return s, fail(exitUsage, "%v", e)
	}
	if f.NArg() != 0 {
		return s, fail(exitUsage, "unexpected spawn arguments")
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
	CreatedAt string         `json:"created_at"`
	Priority  any            `json:"priority"`
}

func (a *App) taskPrecheck(ctx context.Context, s Spawn) (Task, bool, error) {
	r := a.call(ctx, "handoffkeep", "tasks", "show", s.Task)
	var t Task
	if r.Code != 0 {
		if strings.Contains(r.Out+r.Err, "not_found") {
			return t, false, fail(exitUsage, "task does not exist")
		}
		if s.Bypass {
			return t, true, nil
		}
		return t, false, fail(exitConfig, "task precheck unavailable: %s", r.Err)
	}
	if e := json.Unmarshal([]byte(r.Out), &t); e != nil || t.State == "" {
		return t, false, fail(exitConfig, "unreadable task precheck")
	}
	if t.ID != nil && scalar(t.ID) != s.Task {
		return t, false, fail(exitConfig, "task response ID mismatch")
	}
	if s.Inherited != "" {
		return t, false, nil
	}
	if slices.Contains([]string{"merged", "dropped", "hold", "needs_decision"}, t.State) {
		return t, false, fail(exitUsage, "task unavailable: %s", t.State)
	}
	if j := str(t.Refs, "job_id"); j != "" && j != s.Job {
		return t, false, fail(exitUsage, "task bound to another job")
	}
	if str(t.Refs, "job_id") == "" && t.ClaimedBy != "" && t.ClaimedBy != s.Label {
		return t, false, fail(exitUsage, "task has another claimant without a job binding")
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
				return fail(exitConfig, "task claim refused: %s", r.Err)
			}
			status = "bypassed"
		} else {
			if e := json.Unmarshal([]byte(r.Out), &t); e != nil || t.ClaimedBy != s.Label || str(t.Refs, "job_id") != s.Job {
				return fail(exitConfig, "task claim did not confirm claimed_by and refs.job_id")
			}
			status = "linked"
		}
	}
	return a.event(ctx, s.Job, "job.task_link", map[string]any{"task_id": s.Task, "job_id": s.Job, "claimed_by": s.Label, "status": status, "inherited_from": s.Inherited})
}
func (a *App) Spawn(ctx context.Context, s Spawn) (err error) {
	if s.CWD == "" || s.Prompt == "" || s.Workspace == "" || s.Label == "" || !slices.Contains([]string{"T0", "T1", "T2", "T3"}, s.T) {
		return fail(exitUsage, "spawn requires -c -p -w -l --t T0..T3")
	}
	if !validID(s.Job) || !validID(s.Label) || !slices.Contains([]string{"worker", "builder", "tester", "reviewer", "counsel"}, s.Role) {
		return fail(exitUsage, "invalid job, label or role")
	}
	if s.Role == "builder" && (s.Lane == "" || s.Parent == "" || s.Owner != "" || s.Task == "") {
		return fail(exitUsage, "builder requires --lane --parent --task; --owner is not accepted")
	}
	if s.LaneDefer != "" && (s.Role != "builder" || !validID(s.LaneDefer)) {
		return fail(exitUsage, "--lane-defer requires builder and a host name")
	}
	if s.Task != "" && s.ParentJob != "" {
		return fail(exitUsage, "--task and --parent-job are exclusive")
	}
	autoSel := s.Profile == "" || s.Profile == "auto"
	if !s.Delegated && (!autoSel || s.Host != "auto") && strings.TrimSpace(s.OverrideReason) == "" {
		return fail(exitUsage, "explicit -m or --host requires --override-reason")
	}
	if s.Urgency != "" && !slices.Contains([]string{"normal", "urgent"}, s.Urgency) {
		return fail(exitUsage, "--urgency must be normal or urgent")
	}
	if s.Wait && s.DryRun {
		return fail(exitUsage, "--wait needs a real spawn")
	}
	if s.WaitTimeout != "" && !s.Wait {
		return fail(exitUsage, "--wait-timeout requires --wait")
	}
	if s.Wait {
		if _, e := a.waitDeadline(s); e != nil {
			return e
		}
	}
	if s.BuilderFamily != "" || s.BuilderJob != "" {
		if s.BuilderFamily == "" || !validID(s.BuilderJob) || s.ParentJob != "" || s.Task == "" || (s.Role != "reviewer" && s.Role != "tester") {
			return fail(exitUsage, "--builder-family and --builder-job go together, with --task and --role reviewer|tester, not --parent-job")
		}
		if s.Report == "" && s.ReportRelative == "" {
			return fail(exitUsage, "--builder-job requires --report")
		}
		s.ParentFamily = s.BuilderFamily
		s.Inherited = s.BuilderJob
	}
	if s.ParentJob == "" && s.Task == "" {
		s.ParentJob = os.Getenv("ARBITER_JOB")
		if s.ParentJob == "" {
			s.Task = os.Getenv("HK_TASK_ID")
		}
	}
	builderFamily := s.ParentFamily
	if s.ParentJob != "" {
		j, e := a.resolveParent(ctx, s.ParentJob)
		if e != nil {
			return fail(exitStateConflict, "parent metadata unreadable: %v", e)
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
			return fail(exitUsage, "--parent-job requires --report")
		}
	}
	if !regexp.MustCompile(`^[0-9]+$`).MatchString(s.Task) {
		return fail(exitUsage, "spawn requires a numeric task binding")
	}
	if _, e := parseTags(s.Tags); e != nil {
		return e
	}
	excluded, e := parseHosts(s.ExcludeHosts)
	if e != nil {
		return e
	}
	if forced := s.Host; forced != "auto" {
		if forced == "local" {
			forced = a.machine()
		}
		if slices.Contains(excluded, forced) {
			return fail(exitUsage, "--host %s is excluded by --exclude-host", s.Host)
		}
	}
	brief, e := os.ReadFile(expand(s.Prompt))
	if e != nil {
		return e
	}
	if len(strings.TrimSpace(string(brief))) == 0 {
		return fail(exitUsage, "brief is empty")
	}
	selEffort := ""
	if autoSel {
		sel, e := a.selectProfile(ctx, s)
		if sel != nil {
			s.Sel = sel.record()
		}
		if s.DryRun && s.Sel != nil {
			b, _ := encode(s.Sel)
			fmt.Fprintf(a.Out, "urgency=%s source=%s tiers=%s\n", sel.req.urgency, sel.req.urgencySource, strings.Join(sel.order, ">"))
			fmt.Fprintf(a.Out, "selection=%s\n", b)
		}
		if e != nil {
			return e
		}
		s.Profile = sel.chosen.seatName
		selEffort = sel.chosen.row.Effort
		if selEffort != "" {
			s.Effort = selEffort
		}
	} else if !s.Delegated {
		s.Sel = map[string]any{"mode": "override", "profile": s.Profile}
	}
	// An unknown -m seat can never admit anywhere: refuse before placement
	// burns a remote round on hosts that must all answer rc70.
	if _, ok := a.Policy.Profiles[s.Profile]; !ok {
		return fail(exitConfig, "missing seat policy for %s", s.Profile)
	}
	var placementRec map[string]any
	if s.Host != "local" {
		handled, rec, e := a.place(ctx, s, string(brief))
		if e != nil {
			return e
		}
		if handled {
			return a.afterSpawn(ctx, s, true)
		}
		placementRec = rec
	}
	payload, e := a.prepareBrief(s, string(brief))
	if e != nil {
		return e
	}
	if s.DryRun {
		// The local preview must still refuse what Seat.Check would refuse:
		// a capability-bound seat on a host that lacks the flag.
		if seat := a.Policy.Profiles[s.Profile]; seat.Capability != "standard" && !boolean(a.Config.Local, seat.Capability, false) {
			return fail(exitConfig, "host %s lacks capability %s", a.machine(), seat.Capability)
		}
		fmt.Fprintf(a.Out, "dry-run host=%s profile=%s role=%s job=%s\n%s\n", a.machine(), s.Profile, s.Role, s.Job, payload)
		return nil
	}
	if !boolean(a.Config.Local, "spawn", true) {
		return fail(exitLocalDisabled, "local spawns disabled")
	}
	cwd, e := filepath.Abs(expand(s.CWD))
	if e != nil {
		return e
	}
	s.CWD = cwd
	if s.ExecutionLane != "" {
		if s.ExecutionLane != "live" && s.ExecutionLane != "mock" {
			return fail(exitUsage, "-L must be live or mock")
		}
		if info, e := os.Stat(filepath.Join(cwd, s.ExecutionLane)); e == nil && info.IsDir() {
			s.CWD = filepath.Join(cwd, s.ExecutionLane)
		}
	}
	if info, e := os.Stat(s.CWD); e != nil || !info.IsDir() {
		return fail(exitUsage, "cwd is not a directory")
	}
	if s.ReportRelative != "" {
		if filepath.IsAbs(s.ReportRelative) || strings.Contains(s.ReportRelative, "..") {
			return fail(exitUsage, "unsafe report-relative")
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
		return fail(exitConfig, "missing seat policy for %s", s.Profile)
	}
	effort := s.Effort
	if effort == "" {
		effort = seat.Effort
	}
	if seat.Pin && effort != seat.Effort {
		return fail(exitSeatRefused, "profile pins effort %s", seat.Effort)
	}
	task, bypassed, e := a.taskPrecheck(ctx, s)
	if e != nil {
		return e
	}
	grade := s.Grade
	if s.Grade == "" {
		if m := taskMarker.FindStringSubmatch(task.Title); m != nil {
			grade = strings.ToUpper(m[1])
			if strings.ToUpper(m[2]) != s.T {
				return fail(exitSeatRefused, "task scale does not match --t")
			}
		} else if !bypassed {
			return fail(exitSeatRefused, "task title lacks (grade X, Tn marker")
		}
	}
	launch, gate, e := a.admitLaunch(ctx, s, seat, effort, selEffort, grade, builderFamily, autoSel)
	if e != nil {
		return e
	}
	owner := s.Owner
	if owner == "" {
		owner = "default"
	}
	if s.Role == "builder" {
		owner = s.Lane
	}
	claim := arbiter.Claim{Job: s.Job, AgentLabel: s.Label, Lane: owner, T: s.T, Role: "worker", TaskID: s.Task, TaskInheritedFrom: s.Inherited}
	if s.Role == "builder" {
		claim.Role, claim.ParentLane = "builder", s.Parent
	}
	if s.Bypass {
		claim.TaskLinkOverride = "declared"
	}
	// The hk job claim runs on the executing host before the local claim:
	// 409 job_id_taken refuses with the same exit 74, and hk down fails
	// closed like the task claim.
	if e = a.claimJobRecord(ctx, s); e != nil {
		return e
	}
	recordSpawned := false
	defer func() {
		// A duplicate loses to a live spawn of the same id (an identical hk
		// claim replays as claimed); its record belongs to that spawn, so a
		// duplicate never marks it lost.
		var ee *ExitError
		if err != nil && !recordSpawned && !(errors.As(err, &ee) && ee.Code == exitDuplicate) {
			a.abandonJobRecord(context.WithoutCancel(ctx), s.Job)
		}
	}()
	// The local ledger arbitrates concurrent duplicate claims on this host;
	// never infer a free job from pane status.
	_, e = a.ledger().Claim(ctx, claim)
	if e != nil && arbiter.Code(e) == arbiter.ExitDuplicate {
		d, de := a.durable(ctx, s.Job)
		if de != nil {
			return fail(exitStateConflict, "duplicate job state unreadable")
		}
		if d.State == "released" {
			claim.ReclaimReleased = true
			_, e = a.ledger().Claim(ctx, claim)
		} else if !s.DupOK {
			return fail(exitDuplicate, "active duplicate job %s", s.Job)
		} else {
			a.warn("--job-dup-ok used for %s", s.Job)
			if e = a.event(ctx, s.Job, "job.duplicate_override", map[string]any{"label": s.Label}); e != nil {
				return e
			}
			e = nil
		}
	}
	if e != nil {
		return ledgerFail(e, "claim")
	}
	if !s.Delegated {
		if placementRec == nil {
			tags, _ := parseTags(s.Tags)
			placementRec = map[string]any{"source": "forced", "tags": tags, "profile": s.Profile, "host": a.machine()}
		}
		a.attachSelection(placementRec, s)
		if e = a.event(ctx, s.Job, "job.placement", placementRec); e != nil {
			a.warn("placement decision record failed: %v", e)
		}
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
				_ = a.ledger().ReleaseQuotaPool(cleanup, s.Job, pool, false)
			}
		}
	}()
	granted, e := arbiter.ParseGate(seat.gateName(), gate)
	if e != nil {
		return ledgerFail(e, "lease")
	}
	rec, e := a.ledger().RecordQuotaPool(ctx, arbiter.QuotaRun{Job: s.Job, Profile: seat.gateName(), LaunchProfile: launchProfile(seat.Canon, launch.Effort), Gate: granted})
	if e != nil {
		return ledgerFail(e, "lease")
	}
	pool = rec.Pool
	mockEnv, e := a.mock(ctx, s, seat)
	if e != nil {
		return e
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
	env = append(env, "ARBITER_JOB="+s.Job, "ARBITER_RESOURCE="+pool, "ARBITER_KIND=quota_pool", "ARBITER_PROFILE="+seat.gateName(), "HK_TASK_ID="+s.Task)
	if seat.Kind == "claude" && s.Purpose != "director" {
		env = append(env, "CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION=false")
	}
	if e = a.workspaceTrust(ctx, s, seat, env); e != nil {
		return fail(exitConfig, "workspace trust: %v", e)
	}
	tabArgs := []string{"tab", "create", "--workspace", s.Workspace, "--cwd", s.CWD, "--label", s.Label, "--no-focus"}
	for _, v := range env {
		tabArgs = append(tabArgs, "--env", v)
	}
	r := a.call(ctx, "herdr", tabArgs...)
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
		return fail(exitConfig, "tab create omitted pane_id")
	}
	if e = a.start(ctx, s, seat, launch, pane, argv); e != nil {
		return e
	}
	pointer, e := a.writeDeliveredBrief(s.Job, payload)
	if e != nil {
		return e
	}
	if e = a.deliver(ctx, seat.Kind, pane, pointer); e != nil {
		return e
	}
	if seat.Kind == "pi" || seat.Kind == "cmd" {
		if e = a.modelProof(ctx, seat, launch, s.CWD, pane, pointer); e != nil {
			return e
		}
	}
	if e = a.taskLink(ctx, s, bypassed); e != nil {
		return e
	}
	recordSpawned = true
	a.spawnedJobRecord(ctx, s, seat, launch, pane, pool, owner)
	lane := s.Label
	parent := owner
	if s.Role == "builder" {
		lane = s.Lane
		parent = s.Parent
	}
	machine := a.machine()
	laneOK := s.LaneDefer == "" && a.registerLane(ctx, lane, parent, pane, s.Role, machine) == nil
	receipt := map[string]any{"pane_id": pane, "tab_id": tab, "label": s.Label, "profile": s.Profile, "launch_profile": launchProfile(seat.Canon, launch.Effort), "model_id": launch.ModelID, "effort": launch.Effort, "family": seat.Family, "role": s.Role, "lane_role": s.Role, "owner_lane": owner, "parent_lane": s.Parent, "task_id": s.Task, "report": s.Report, "cwd": s.CWD, "keep": s.Keep, "pool": pool, "host": machine}
	if laneOK {
		receipt["hub_lane"] = lane
		receipt["hub_lane_machine"] = machine
	}
	if e = a.event(ctx, s.Job, "job.spawned", receipt); e != nil {
		return e
	}
	committed = true
	if e = a.startWatch(s.Job); e != nil {
		a.warn("completion watcher failed: %v", e)
	}
	laneStatus := ""
	if laneOK {
		laneStatus = " lane=" + lane + "@" + machine
	} else if s.Role == "builder" {
		laneStatus = " lane=UNREGISTERED"
	}
	fmt.Fprintf(a.Out, "OK pane=%s model=%s label=%s status=working landed=yes job=%s quota_record=%s/quota_pool%s\n", pane, s.Profile, s.Label, s.Job, pool, laneStatus)
	if s.Role == "builder" && !laneOK && a.envOr("RUNTUG_LANE_STRICT", "WRK_LANE_STRICT") == "1" {
		return fail(exitLaneUnregistered, "builder pane exists but lane is UNREGISTERED")
	}
	return a.afterSpawn(ctx, s, false)
}

// admitLaunch resolves the launch rung for a selected seat and asks the quota
// gate and the hub for the spawn. It is the admission half shared by spawn and
// `runtug delegate --headless`; it claims and writes nothing.
func (a *App) admitLaunch(ctx context.Context, s Spawn, seat Seat, effort, selEffort, grade, builderFamily string, autoSel bool) (Launch, string, error) {
	qr := QuotaRequest{Profile: seat.Canon, GateProfile: seat.gateName(), Effort: effort, Role: s.Role, OperatorRequest: s.OperatorRequest, RequestedBy: s.RequestedBy, Purpose: s.Purpose}
	launch, e := a.Quota.Launch(ctx, qr)
	if e != nil {
		// Preserve the gate's role/quota refusal even when the catalog also
		// refuses this launch; no fallback launch data is ever invented.
		if _, gateErr := a.Quota.Gate(ctx, qr); gateErr != nil {
			return Launch{}, "", gateErr
		}
		return Launch{}, "", e
	}
	if effort != "" && launch.Effort != effort {
		return Launch{}, "", fail(exitConfig, "catalog effort does not match request")
	}
	if autoSel && selEffort != "" && launch.Effort != selEffort {
		return Launch{}, "", fail(exitConfig, "launch rung does not match selected catalog rung")
	}
	if launch.Profile != seat.Canon {
		return Launch{}, "", fail(exitConfig, "catalog profile mismatch")
	}
	if e = seat.Check(launch, s.Role, grade, builderFamily, a.envOr("RUNTUG_E6_ARM", "SCOPEFUEL_E6_ARM"), a.Config.Local); e != nil {
		return Launch{}, "", e
	}
	// A gate alias is the pool gate for a whole family of spellings, so it
	// cannot tell an escalation rung from a default one; the launch row can.
	if seat.Gate != "" && launch.Gate != "default" && !s.AllowEscalation {
		return Launch{}, "", fail(exitSeatRefused, "%s is a %s rung; pass --allow-escalation", seat.Canon, launch.Gate)
	}
	qr.Effort = launch.Effort
	gate, e := a.Quota.Gate(ctx, qr)
	if e != nil {
		return Launch{}, "", e
	}
	if e = a.hubGate(ctx, s.CWD, gate); e != nil {
		return Launch{}, "", e
	}
	return launch, gate, nil
}
func (a *App) attachSelection(rec map[string]any, s Spawn) {
	if s.Sel != nil {
		rec["selection"] = s.Sel
	}
	if s.OverrideReason != "" {
		rec["override_reason"] = s.OverrideReason
	}
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
	if mapping, ok := a.Config.Hub["session_machine_ids"].(map[string]any); ok {
		session := firstNonempty(os.Getenv("HERDR_SESSION"), "default")
		if id := str(mapping, session); id != "" {
			return id
		}
	}
	for _, m := range []map[string]any{a.Config.Local, a.Config.Hub} {
		if v := str(m, "machine_id"); v != "" {
			return v
		}
	}
	return "local"
}
func (a *App) captureFailure(ctx context.Context, job, pane string) {
	r := a.call(ctx, "herdr", "pane", "read", pane, "--source", "visible", "--lines", "80")
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
		return fail(exitUsage, "report must be absolute")
	}
	for _, id := range []string{job, parent} {
		if id != "" && within(filepath.Join(root, id), path) {
			return noSymlinks(path)
		}
	}
	return fail(exitUsage, "report must be under the job or parent job directory")
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
			return fail(exitUsage, "symlink path refused: %s", p)
		}
	}
	return nil
}

// writeDeliveredBrief stores the full brief in the job dir on this host and
// returns the one-line pointer that is typed into the pane instead. Typing the
// whole brief lost everything past about 1 KB on a remote host, and the pane
// still reported a landed brief; a file has no such limit.
func (a *App) writeDeliveredBrief(job, payload string) (string, error) {
	dir := filepath.Join(a.Jobs, job)
	if e := os.MkdirAll(dir, 0700); e != nil {
		return "", e
	}
	path := filepath.Join(dir, "brief-delivered.md")
	if e := os.WriteFile(path, []byte(payload), 0600); e != nil {
		return "", e
	}
	sum := sha256.Sum256([]byte(payload))
	return fmt.Sprintf("Your brief is the file %s (%d bytes, sha256 %x). Read the whole file now, then follow it exactly; it is your only instruction.", path, len(payload), sum[:6]), nil
}
