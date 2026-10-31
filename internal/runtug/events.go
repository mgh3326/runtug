package runtug

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

type Event struct {
	Kind      string         `json:"kind"`
	CreatedAt string         `json:"created_at"`
	TS        string         `json:"ts"`
	Payload   map[string]any `json:"payload"`
	when      time.Time
	mtime     time.Time
	name      string
}
type Job struct {
	ID                                                                                                string
	Owner, Parent, Role, Label, Pane, Tab, Task, Family, Profile, Effort, Report, HubLane, Machine, T string
	Keep, Terminal, Reaped                                                                            bool
	At                                                                                                time.Time
	Receipt                                                                                           map[string]any
	Events                                                                                            []Event
}

func (a *App) events(id string) ([]Event, error) {
	if !validID(id) {
		return nil, fail(exitUsage, "invalid job id")
	}
	dir := filepath.Join(a.Jobs, id, "events")
	if e := noSymlinks(dir); e != nil {
		return nil, e
	}
	entries, e := os.ReadDir(dir)
	if e != nil {
		return nil, e
	}
	var out []Event
	for _, ent := range entries {
		if ent.Type()&os.ModeSymlink != 0 {
			return nil, fail(exitStateConflict, "symlink event refused")
		}
		if ent.IsDir() || filepath.Ext(ent.Name()) != ".json" {
			continue
		}
		info, e := ent.Info()
		if e != nil {
			return nil, e
		}
		b, e := os.ReadFile(filepath.Join(dir, ent.Name()))
		if e != nil {
			return nil, e
		}
		var ev Event
		if e = json.Unmarshal(b, &ev); e != nil {
			return nil, fail(exitStateConflict, "unreadable job event %s", ent.Name())
		}
		if ev.Kind == "" {
			return nil, fail(exitStateConflict, "event kind missing")
		}
		ev.name = ent.Name()
		ev.when = info.ModTime()
		ev.mtime = info.ModTime()
		for _, s := range []string{ev.CreatedAt, ev.TS} {
			if t, e := time.Parse(time.RFC3339Nano, s); e == nil {
				ev.when = t
				break
			}
		}
		out = append(out, ev)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].when.Equal(out[j].when) {
			if !out[i].mtime.Equal(out[j].mtime) {
				return out[i].mtime.Before(out[j].mtime)
			}
			return out[i].name < out[j].name
		}
		return out[i].when.Before(out[j].when)
	})
	return out, nil
}
func (a *App) job(id string) (Job, error) {
	evs, e := a.events(id)
	j := Job{ID: id, Events: evs}
	if e != nil {
		return j, e
	}
	for _, ev := range evs {
		p := ev.Payload
		switch ev.Kind {
		case "job.claim", "job.claimed", "job.reclaim":
			j.Owner = str(p, "owner_lane")
			j.Parent = str(p, "parent_lane")
			if r := str(p, "role"); r != "" {
				if r == "builder" || r == "captain" || j.Role == "" {
					j.Role = r
				}
			}
			j.Task = scalar(p["task_id"])
			j.T = str(p, "t_level")
			j.Terminal = false
			j.Reaped = false
		case "job.spawned":
			j.Receipt = p
			j.Pane = str(p, "pane_id")
			j.Tab = str(p, "tab_id")
			j.Label = str(p, "label")
			if j.Label == "" {
				j.Label = str(p, "agent_label")
			}
			j.Family = str(p, "family")
			j.Profile = str(p, "profile")
			j.Effort = str(p, "effort")
			j.Report = str(p, "report")
			j.HubLane = str(p, "hub_lane")
			j.Machine = str(p, "hub_lane_machine")
			j.Terminal = false
			j.Reaped = false
			if j.Task == "" {
				j.Task = scalar(p["task_id"])
			}
		case "job.reprompted":
			j.Terminal = false
		case "job.completed", "job.joined", "job.revoked", "job.handed_off":
			j.Terminal = true
			j.At = ev.when
		case "job.reaped":
			j.Reaped = true
		}
		if boolean(p, "keep", false) {
			j.Keep = true
		}
	}
	if j.Receipt == nil && j.Task == "" {
		return j, fail(exitStateConflict, "no claim or receipt for job %s", id)
	}
	return j, nil
}
func scalar(v any) string {
	switch n := v.(type) {
	case string:
		return n
	case float64:
		b, _ := json.Marshal(n)
		return string(b)
	}
	return ""
}
func (a *App) event(ctx context.Context, id, kind string, p map[string]any) error {
	b, e := encode(p)
	if e != nil {
		return e
	}
	return a.must(ctx, "arbiter", "event", "--job", id, "--kind", kind, "--payload-json", string(b))
}

type DurableJob struct {
	State   string         `json:"state"`
	Owner   string         `json:"owner_lane"`
	Label   string         `json:"agent_label"`
	Receipt DurableReceipt `json:"receipt"`
}

// DurableReceipt is the `receipt` field of `arbiter job-get`. Accepted shapes:
//   - an object: the spawn receipt, decoded as a map;
//   - the string sentinel "absent", JSON null, or a missing field: no receipt
//     (nil). The arbiter reports a released job this way, and it stays
//     reclaimable.
//
// Any other shape (another string, a number, a bool, an array) is an error, so
// durable() fails closed with exit 75.
type DurableReceipt map[string]any

func (r *DurableReceipt) UnmarshalJSON(b []byte) error {
	var v any
	if e := json.Unmarshal(b, &v); e != nil {
		return e
	}
	switch t := v.(type) {
	case nil:
		*r = nil
		return nil
	case string:
		if t == "absent" {
			*r = nil
			return nil
		}
	case map[string]any:
		*r = t
		return nil
	}
	return fmt.Errorf("unexpected receipt shape %.40s", b)
}

func (a *App) durable(ctx context.Context, id string) (DurableJob, error) {
	r := a.call(ctx, "arbiter", "job-get", "--job", id, "--json")
	var j DurableJob
	if e := check(r, "arbiter job-get"); e != nil {
		return j, e
	}
	if e := json.Unmarshal([]byte(r.Out), &j); e != nil || j.State == "" {
		return j, fail(exitStateConflict, "unreadable durable job %s", id)
	}
	return j, nil
}
