package runtug

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type Placement struct {
	Decision   string            `json:"decision"`
	Candidates []json.RawMessage `json:"candidates"`
}

func placementHosts(raw string) ([]string, error) {
	var p Placement
	if e := json.Unmarshal([]byte(raw), &p); e != nil {
		return nil, fail(70, "invalid placement response")
	}
	var out []string
	for _, v := range p.Candidates {
		var name string
		if json.Unmarshal(v, &name) != nil {
			var c struct {
				Machine, Host, Target, Reason string
				Connected                     *bool
			}
			if json.Unmarshal(v, &c) != nil {
				return nil, fail(70, "invalid placement candidate")
			}
			if c.Connected != nil && !*c.Connected || strings.Contains(c.Reason, "not_accepting") {
				continue
			}
			name = c.Machine
			if name == "" {
				name = c.Host
			}
			if name == "" {
				name = c.Target
			}
		}
		if validID(name) && !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	if i := slices.Index(out, p.Decision); i >= 0 {
		out = append([]string{p.Decision}, append(out[:i], out[i+1:]...)...)
	}
	return out, nil
}
func (a *App) place(ctx context.Context, s Spawn, brief string) (bool, error) {
	hosts := []string{s.Host}
	if s.Host == "auto" {
		if str(a.Config.Hub, "hub_url") == "" || str(a.Config.Hub, "hub_token_env") == "" {
			var e error
			hosts, e = a.fallbackHosts(ctx)
			if e != nil {
				return true, e
			}
			if len(hosts) == 1 && hosts[0] == "local" {
				return false, nil
			}
		} else {
			args := []string{"place", "--class", "worker", "--cwd", s.CWD}
			args = append(args, a.controlHubArgs(false)...)
			r := a.call(ctx, "panewire", args...)
			if r.Code != 0 {
				if r.Code == 4 {
					var e error
					hosts, e = a.fallbackHosts(ctx)
					if e != nil {
						return true, e
					}
				} else {
					return true, check(r, "placement")
				}
			} else {
				var e error
				hosts, e = placementHosts(r.Out)
				if e != nil {
					return true, e
				}
				if len(hosts) == 0 {
					return true, fail(81, "placement has no eligible host")
				}
			}
		}
	}
	for _, host := range hosts {
		if host == "local" || host == a.machine() {
			if boolean(a.Config.Local, "spawn", true) {
				return false, nil
			}
			continue
		}
		h, ok := a.Config.Hosts[host]
		if !ok {
			return true, fail(70, "placement selected unconfigured host %s", host)
		}
		if s.ExecutionLane == "mock" {
			return true, fail(79, "remote mock spawns refused")
		}
		if s.DryRun {
			payload, e := a.prepareBriefForHost(s, host, brief)
			if e != nil {
				return true, e
			}
			fmt.Fprintf(a.Out, "dry-run host=%s profile=%s role=%s job=%s\n%s\n", host, s.Profile, s.Role, s.Job, payload)
			return true, nil
		}
		if str(h, "wake") == "panewire" {
			args := []string{"burst", "request", "--target", host, "--hold", fmt.Sprintf("%dm", integer(h, "wake_hold", 10)), "--timeout", fmt.Sprintf("%ds", integer(h, "wake_wait", 90))}
			args = append(args, a.controlHubArgs(true)...)
			r := a.call(ctx, "panewire", args...)
			if r.Code != 0 {
				if s.Host != "auto" {
					return true, fail(81, "host wake failed")
				}
				continue
			}
		}
		if str(h, "via") == "hub" {
			return true, fail(70, "hub spawn transport still targets wrk; use an SSH target running runtug")
		}
		cwd := mappedCWD(h["cwd_map"], s.CWD)
		if cwd == "" {
			return true, fail(70, "host %s lacks cwd_map for requested directory", host)
		}
		target := str(h, "ssh")
		if target == "" {
			target = host
		}
		if strings.HasPrefix(target, "-") || strings.ContainsAny(target, "\r\n") {
			return true, fail(70, "invalid SSH target")
		}
		remote := s
		remote.ParentJob = ""
		remote.CWD = cwd
		remote.Host = "local"
		remote.Workspace = str(h, "workspace")
		if remote.Workspace == "" {
			remote.Workspace = s.Workspace
		}
		remote.Prompt = ""
		// Parent task/family is read on the initiator. Carry inheritance explicitly;
		// the remote must not claim the owning job's task a second time.
		if s.Report != "" {
			if !within(a.Jobs, s.Report) {
				return true, fail(2, "remote report outside jobs root")
			}
			rel, e := filepath.Rel(a.Jobs, s.Report)
			if e != nil {
				return true, e
			}
			remote.Report = ""
			remote.ReportRelative = rel
		}
		if _, _, e := a.taskPrecheck(ctx, s); e != nil {
			return true, e
		}
		envelope := struct {
			Spawn Spawn
			Brief string
		}{remote, brief}
		b, e := encode(envelope)
		if e != nil {
			return true, e
		}
		session := str(h, "herdr_session")
		if session == "" {
			session = "worker"
		}
		r := a.Runner.Run(ctx, Command{Name: "ssh", Args: []string{"-o", "BatchMode=yes", "--", target, "env HERDR_SESSION=" + shellQuote(session) + " runtug _spawn-stdin"}, Input: string(b)})
		fmt.Fprint(a.Err, r.Err)
		if strings.Contains(r.Out, "OK pane=") {
			fmt.Fprint(a.Out, strings.TrimRight(r.Out, "\n")+" host="+host+"\n")
			return true, check(r, "remote spawn")
		}
		if r.Code == 0 {
			return true, fail(75, "remote spawn returned no receipt")
		}
		if slices.Contains([]int{2, 70, 74, 75, 76, 77, 79, 82, 3, 4, 5, 6}, r.Code) || s.Host != "auto" {
			return true, check(r, "remote spawn")
		}
	}
	return true, fail(81, "no placement candidate accepted spawn; no local fallback")
}
func (a *App) fallbackHosts(ctx context.Context) ([]string, error) {
	pressured := !boolean(a.Config.Local, "spawn", true)
	if max, ok := a.Config.Local["max_load_ratio"]; ok {
		ratio, e := a.loadRatio(ctx)
		if e != nil {
			return nil, e
		}
		threshold := 0.0
		switch n := max.(type) {
		case int64:
			threshold = float64(n)
		case float64:
			threshold = n
		}
		pressured = pressured || ratio >= threshold
	}
	if _, ok := a.Config.Local["max_active"]; ok {
		agents, e := a.agents(ctx)
		if e != nil {
			return nil, e
		}
		count := int64(0)
		for _, agent := range agents {
			if liveStatus(agent.Status) {
				count++
			}
		}
		pressured = pressured || count >= integer(a.Config.Local, "max_active", 0)
	}
	if !pressured {
		return []string{"local"}, nil
	}
	hosts := slices.Clone(a.Config.HostOrder)
	if len(hosts) == 0 {
		for host := range a.Config.Hosts {
			hosts = append(hosts, host)
		}
		slices.Sort(hosts)
	}
	if len(hosts) == 0 {
		return nil, fail(80, "local spawn disabled or pressured; no remote configured")
	}
	return hosts, nil
}
func mappedCWD(raw any, cwd string) string {
	mapping, ok := raw.(map[string]any)
	if !ok {
		return ""
	}
	best, mapped := "", ""
	for from, v := range mapping {
		to, ok := v.(string)
		if !ok {
			continue
		}
		if cwd == from || strings.HasPrefix(cwd, from+"/") || strings.HasPrefix(cwd, from+".") {
			if len(from) > len(best) {
				best = from
				mapped = to + strings.TrimPrefix(cwd, from)
			}
		}
	}
	return mapped
}
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func (a *App) spawnStdin(ctx context.Context) error {
	b, e := io.ReadAll(io.LimitReader(os.Stdin, 4<<20))
	if e != nil {
		return e
	}
	var env struct {
		Spawn Spawn
		Brief string
	}
	if e = decode(b, &env); e != nil {
		return e
	}
	if env.Spawn.Host != "local" {
		return fail(2, "delegation must target local")
	}
	f, e := os.CreateTemp("", "runtug-brief-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if _, e = f.WriteString(env.Brief); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	env.Spawn.Prompt = f.Name()
	return a.Spawn(ctx, env.Spawn)
}
func (a *App) prepareBriefForHost(s Spawn, host, body string) (string, error) {
	copy := *a
	copy.Config = a.Config
	copy.Config.Local = map[string]any{}
	for k, v := range a.Config.Hosts[host] {
		copy.Config.Local[k] = v
	}
	copy.Config.Local["machine_id"] = host
	return copy.prepareBrief(s, body)
}
