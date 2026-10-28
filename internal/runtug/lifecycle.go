package runtug

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

func (a *App) lifecycle(ctx context.Context, kind string, args []string) error {
	if len(args) == 0 || !validID(args[0]) {
		return fail(2, "%s requires a job id", kind)
	}
	id := args[0]
	f := flags(kind)
	report := f.String("report", "", "")
	question := f.String("question", "", "")
	if e := f.Parse(args[1:]); e != nil {
		return fail(2, "%v", e)
	}
	if f.NArg() > 0 {
		return fail(2, "unexpected arguments")
	}
	j, e := a.job(id)
	if e != nil {
		return e
	}
	if kind == "restore" {
		if *report != "" || *question != "" {
			return fail(2, "restore accepts only JOB")
		}
		return a.restore(ctx, j)
	}
	if kind == "escalate" && (*question == "" || j.Parent == "") {
		return fail(2, "escalate requires --question and a recorded parent lane")
	}
	if *report == "" && kind == "done" {
		matches, _ := filepath.Glob(filepath.Join(a.Jobs, id, "report*.md"))
		sort.Slice(matches, func(i, j int) bool {
			x, _ := os.Stat(matches[i])
			y, _ := os.Stat(matches[j])
			return x.ModTime().After(y.ModTime())
		})
		if len(matches) > 0 {
			*report = matches[0]
		}
	}
	if kind == "done" && *report == "" {
		return fail(2, "done requires an existing report")
	}
	body := []byte{}
	if *report != "" {
		if e = noSymlinks(*report); e != nil {
			return e
		}
		body, e = os.ReadFile(*report)
		if e != nil {
			return e
		}
	}
	lock, e := os.OpenFile(filepath.Join(a.Jobs, id, ".runtug-operation.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	defer lock.Close()
	if e = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); e != nil {
		return e
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	j, e = a.job(id)
	if e != nil {
		return e
	}
	hash := sha256.Sum256(body)
	digest := hex.EncodeToString(hash[:])
	eventKind := "job.completed"
	if kind == "escalate" {
		eventKind = "job.escalate"
	}
	rounds := 0
	usedPath := false
	for _, ev := range j.Events {
		if ev.Kind == eventKind {
			rounds++
			if str(ev.Payload, "report_sha256") == digest && str(ev.Payload, "question") == *question {
				if kind == "done" {
					if e = a.releaseQuota(ctx, j); e != nil {
						return e
					}
				}
				fmt.Fprintf(a.Out, "OK job=%s report=%s duplicate=yes\n", id, *report)
				return nil
			}
			if str(ev.Payload, "report") == *report {
				usedPath = true
			}
		}
	}
	if usedPath && *report != "" {
		ext := filepath.Ext(*report)
		stem := strings.TrimSuffix(*report, ext)
		for n := rounds + 1; ; n++ {
			path := fmt.Sprintf("%s-r%d%s", stem, n, ext)
			file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if os.IsExist(err) {
				continue
			}
			if err != nil {
				return err
			}
			_, err = file.Write(body)
			closeErr := file.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return closeErr
			}
			*report = path
			break
		}
	}
	doc := ""
	if *report != "" {
		key := "reports/" + id + "/" + filepath.Base(*report)
		r := a.call(ctx, "handoffkeep", "doc", "put", "--key", key, "--kind", "report", "--job", id, "--file", *report)
		if r.Code == 0 {
			doc = key
		} else {
			a.warn("report upload failed; local report retained")
		}
	}
	p := map[string]any{"report": *report, "report_sha256": digest, "question": *question, "owner_lane": j.Owner, "parent_lane": j.Parent, "label": j.Label, "pane_id": j.Pane, "document": doc}
	if e = a.event(ctx, id, eventKind, p); e != nil {
		return e
	}
	owner := j.Owner
	if j.Parent != "" && (kind == "escalate" || j.Role == "builder" || j.Role == "captain") {
		owner = j.Parent
	}
	last := strings.TrimSpace(string(body))
	if lines := strings.Split(last, "\n"); len(lines) > 0 {
		last = lines[len(lines)-1]
	}
	if doc != "" {
		last += " doc:" + doc
	}
	argsEmit := []string{"emit", "--kind", eventKind, "--job", id, "--owner-lane", owner, "--epoch", "1", "--label", j.Label, "--host", a.machine(), "--pane", j.Pane, "--inbox-root", filepath.Dir(a.Jobs)}
	if *report != "" {
		argsEmit = append(argsEmit, "--report", *report, "--report-last-line", last)
	}
	if *question != "" {
		argsEmit = append(argsEmit, "--question", *question)
	}
	r := a.call(ctx, "panewire", argsEmit...)
	relay := ""
	if r.Code != 0 {
		relay = " relay=file-only"
		a.warn("relay failed; durable arbiter event retained")
		_ = a.event(ctx, id, "job.relay_failed", map[string]any{"kind": eventKind, "exit": r.Code})
	}
	if j.Task != "" {
		comment := "runtug " + eventKind + " job=" + id + " report=" + *report
		if e = a.must(ctx, "handoffkeep", "tasks", "comment", j.Task, "--body", comment); e != nil {
			a.warn("task comment failed: %v", e)
		}
	}
	if kind == "done" {
		if e = a.releaseQuota(ctx, j); e != nil {
			return e
		}
	}
	fmt.Fprintf(a.Out, "OK job=%s report=%s kind=%s%s\n", id, *report, eventKind, relay)
	return nil
}
func (a *App) releaseQuota(ctx context.Context, j Job) error {
	r := a.call(ctx, "arbiter", "status", "--job", j.ID, "--json")
	if e := check(r, "arbiter status"); e != nil {
		return e
	}
	var status struct {
		Records []struct {
			Pool string `json:"pool"`
		} `json:"quota_pool_records"`
	}
	if e := json.Unmarshal([]byte(r.Out), &status); e != nil {
		return e
	}
	for _, record := range status.Records {
		if record.Pool == "" {
			return fail(75, "invalid quota record")
		}
		if e := a.must(ctx, "arbiter", "release", "--job", j.ID, "--resource", record.Pool, "--kind", "quota_pool"); e != nil {
			return e
		}
	}
	return nil
}
func (a *App) restore(ctx context.Context, j Job) error {
	d, e := a.durable(ctx, j.ID)
	if e != nil {
		return e
	}
	if d.State != "released" {
		return fail(2, "restore requires released job")
	}
	agents, e := a.agents(ctx)
	if e != nil {
		return e
	}
	found := 0
	for _, ag := range agents {
		if ag.Pane == j.Pane {
			if ag.Tab != j.Tab || ag.Name != j.Label || !liveStatus(ag.Status) {
				return fail(2, "restore pane identity or liveness mismatch")
			}
			found++
		}
	}
	if found != 1 || j.Pane == "" || j.Tab == "" {
		return fail(2, "restore requires one verified existing pane")
	}
	role := "worker"
	if j.Role == "builder" || j.Role == "captain" {
		role = "builder"
	}
	args := []string{"claim", "--reclaim-released", "--job", j.ID, "--lane", j.Owner, "--agent-label", j.Label, "--t", j.T, "--role", role, "--task-id", j.Task}
	if role == "builder" {
		args = append(args, "--parent-lane", j.Parent)
	}
	if e = a.must(ctx, "arbiter", args...); e != nil {
		return e
	}
	if e = a.event(ctx, j.ID, "job.spawned", j.Receipt); e != nil {
		return fail(1, "job reclaimed; restoring receipt failed: %v", e)
	}
	if e = a.startWatch(j.ID); e != nil {
		return e
	}
	fmt.Fprintf(a.Out, "OK job=%s restored pane=%s tab=%s label=%s\n", j.ID, j.Pane, j.Tab, j.Label)
	return nil
}
func liveStatus(s string) bool { return s == "working" || s == "idle" || s == "done" }
