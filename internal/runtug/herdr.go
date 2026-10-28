package runtug

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

func (a *App) launchArgs(s Seat, l Launch) ([]string, []string, error) {
	m, e := l.ModelID, l.Effort
	var env []string
	switch s.Kind {
	case "claude":
		return []string{"--model", m, "--dangerously-skip-permissions", "--effort", e}, env, nil
	case "codex":
		return []string{"--yolo", "-m", m, "-c", "model_reasoning_effort=" + e}, env, nil
	case "devin":
		return []string{"--model", m, "--permission-mode", "dangerous", "--respect-workspace-trust", "false"}, env, nil
	case "grok":
		return []string{"--always-approve", "-m", m, "--effort", e}, env, nil
	case "pi":
		return []string{"--no-mcp", "--model", m + ":" + e}, env, nil
	case "cmd":
		return []string{"-m", m, "--effort", e, "--yolo", "--skip-onboarding", "--no-session"}, env, nil
	case "kimi":
		home := os.Getenv("KIMI_CODE_HOME")
		if home == "" {
			home = filepath.Join(a.Home, ".kimi-code")
		}
		if s.Pin {
			key := "KIMI_CODE_" + strings.ToUpper(e) + "_HOME"
			home = os.Getenv(key)
			if home == "" {
				home = filepath.Join(a.Home, ".local", "share", "kimi-code-"+e)
			}
		}
		c, err := readThinking(home)
		if err != nil || c != e {
			return nil, nil, fail(70, "Kimi clone must attest thinking effort %s", e)
		}
		return []string{"--auto", "-m", m}, []string{"KIMI_CODE_HOME=" + home}, nil
	case "kiro":
		return []string{"chat", "--v3", "--trust-all-tools", "--require-mcp-startup", "--model", m, "--effort", e}, env, nil
	case "opencode":
		return []string{"--auto", "--model", m}, env, nil
	default:
		return nil, nil, fail(70, "unsupported agent kind %s", s.Kind)
	}
}
func readThinking(home string) (string, error) {
	b, e := os.ReadFile(filepath.Join(home, "config.toml"))
	if e != nil {
		return "", e
	}
	var t struct {
		Thinking struct {
			Effort string `toml:"effort"`
		} `toml:"thinking"`
	}
	if e = unmarshalTOML(b, &t); e != nil {
		return "", e
	}
	return t.Thinking.Effort, nil
}

type Agent struct {
	Pane   string `json:"pane_id"`
	Tab    string `json:"tab_id"`
	Name   string `json:"name"`
	Kind   string `json:"agent"`
	Status string `json:"agent_status"`
	CWD    string `json:"cwd"`
}

func (a *App) agents(ctx context.Context) ([]Agent, error) {
	r := a.call(ctx, "herdr", "agent", "list")
	if e := check(r, "herdr agent list"); e != nil {
		return nil, e
	}
	var m map[string]json.RawMessage
	if e := json.Unmarshal([]byte(r.Out), &m); e != nil {
		return nil, e
	}
	if v, ok := m["result"]; ok {
		if e := json.Unmarshal(v, &m); e != nil {
			return nil, e
		}
	}
	v, ok := m["agents"]
	if !ok {
		return nil, fail(70, "agent list missing agents")
	}
	var agents []Agent
	e := json.Unmarshal(v, &agents)
	return agents, e
}
func (a *App) status(ctx context.Context, pane string) (string, error) {
	agents, e := a.agents(ctx)
	if e != nil {
		return "", e
	}
	for _, ag := range agents {
		if ag.Pane == pane {
			return ag.Status, nil
		}
	}
	return "", fail(70, "pane absent from agent list")
}

var ansi = regexp.MustCompile("\x1b\\[[0-?]*[ -/]*[@-~]")

func cleanScreen(s string) []string {
	lines := strings.Split(ansi.ReplaceAllString(s, ""), "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \r\t")
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}
func normalized(s string) string { return strings.Join(strings.Fields(s), " ") }
func (a *App) visible(ctx context.Context, pane string) (string, error) {
	r := a.call(ctx, "herdr", "agent", "read", pane, "--source", "visible", "--lines", "80")
	return r.Out, check(r, "herdr visible read")
}
func CmdProof(screen, model, effort, cwd, home, brief string) bool {
	lines := cleanScreen(screen)
	if _, name, ok := strings.Cut(model, "/"); ok {
		model = name
	}
	if !strings.HasSuffix(model, "-(latest)") {
		model += "-(latest)"
	}
	want := "# models: " + model + " with " + effort + " effort · "
	count := 0
	for i, line := range lines {
		if !strings.HasPrefix(line, "# models: ") {
			continue
		}
		if !strings.HasPrefix(line, want) || len(line) == len(want) || i == 0 || i+1 >= len(lines) || !regexp.MustCompile(`^# Command Code v\S+$`).MatchString(lines[i-1]) {
			return false
		}
		shown := []string{"# " + cwd}
		if cwd == home {
			shown = append(shown, "# ~")
		} else if strings.HasPrefix(cwd, strings.TrimRight(home, "/")+"/") {
			shown = append(shown, "# ~"+strings.TrimPrefix(cwd, home))
		}
		if !slices.Contains(shown, lines[i+1]) {
			return false
		}
		block := normalized(strings.Join(lines[i-1:i+2], "\n"))
		if strings.Contains(normalized(brief), block) {
			return false
		}
		count++
	}
	return count == 1
}
func PiProof(screen, model, effort, cwd, home, brief string) bool {
	lines := cleanScreen(screen)
	bottom := -1
	for i, line := range lines {
		if regexp.MustCompile(`^(─{3,}|─+ ↓ \d+ more ─+)$`).MatchString(line) {
			bottom = i
		}
	}
	if bottom < 2 || len(lines)-bottom < 3 || len(lines)-bottom > 4 {
		return false
	}
	top := -1
	for i := 0; i < bottom; i++ {
		if strings.HasPrefix(lines[i], "──") {
			top = i
		}
	}
	if top < 0 || bottom-top < 2 {
		return false
	}
	footer := lines[bottom+1:]
	paths := []string{cwd}
	if real, e := filepath.EvalSymlinks(cwd); e == nil {
		paths = append(paths, real)
	}
	for _, p := range slices.Clone(paths) {
		if p == home {
			paths = append(paths, "~")
		} else if strings.HasPrefix(p, home+"/") {
			paths = append(paths, "~"+p[len(home):])
		}
	}
	cwdOK := false
	for _, p := range paths {
		if regexp.MustCompile(`^` + regexp.QuoteMeta(p) + `(?: \([^\r\n]*\))?(?: • [^\r\n]+)?$`).MatchString(footer[0]) {
			cwdOK = true
		}
	}
	provider, name, ok := strings.Cut(model, "/")
	if !ok || !cwdOK {
		return false
	}
	pattern := `^(?s).*(?:\d+(?:\.\d+)?%|\?)/\S+(?: \(auto\))?\s+(?:\(` + regexp.QuoteMeta(provider) + `\) )?` + regexp.QuoteMeta(name) + ` • ` + regexp.QuoteMeta(effort) + `$`
	return regexp.MustCompile(pattern).MatchString(footer[1]) && !strings.Contains(normalized(brief), normalized(strings.Join(footer[:2], "\n")))
}
func (a *App) modelProof(ctx context.Context, s Seat, l Launch, cwd, pane, brief string) error {
	screen, e := a.visible(ctx, pane)
	if e != nil {
		return e
	}
	ok := true
	if s.Kind == "cmd" {
		ok = CmdProof(screen, l.ModelID, l.Effort, cwd, a.Home, brief)
	}
	if s.Kind == "pi" {
		ok = PiProof(screen, l.ModelID, l.Effort, cwd, a.Home, brief)
	}
	if !ok {
		return fail(70, "%s model/effort not proven by visible session", s.Kind)
	}
	return nil
}

type processInfo struct {
	ShellPID  int `json:"shell_pid"`
	Processes []struct {
		PID     int    `json:"pid"`
		Argv0   string `json:"argv0"`
		Cmdline string `json:"cmdline"`
	} `json:"foreground_processes"`
}

func (a *App) pinfo(ctx context.Context, pane string) (processInfo, error) {
	r := a.call(ctx, "herdr", "pane", "process-info", "--pane", pane)
	var p processInfo
	if e := check(r, "process-info"); e != nil {
		return p, e
	}
	var m map[string]json.RawMessage
	if e := json.Unmarshal([]byte(r.Out), &m); e != nil {
		return p, e
	}
	if v, ok := m["result"]; ok {
		if e := json.Unmarshal(v, &m); e != nil {
			return p, e
		}
	}
	v, ok := m["process_info"]
	if !ok {
		return p, fail(70, "missing process_info")
	}
	if e := json.Unmarshal(v, &p); e != nil {
		return p, e
	}
	if p.Processes == nil {
		return p, fail(70, "missing foreground_processes")
	}
	return p, nil
}
func (a *App) start(ctx context.Context, s Spawn, seat Seat, l Launch, pane string, argv []string) error {
	timeout := time.Duration(integer(a.Config.Local, "shell_ready_timeout_ms", 120000)) * time.Millisecond
	deadline := a.Now().Add(timeout)
	ready := false
	for n := 0; n < 120 && a.Now().Before(deadline); n++ {
		p, e := a.pinfo(ctx, pane)
		if e == nil && p.ShellPID != 0 && len(p.Processes) == 1 && p.Processes[0].PID == p.ShellPID {
			ready = true
			break
		}
		if e = a.Sleep(ctx, time.Second); e != nil {
			return e
		}
	}
	if !ready {
		return fail(70, "shell not ready")
	}
	// Exact output-line proof avoids typing launch argv into an rc-file read.
	var nonce [12]byte
	if _, e := rand.Read(nonce[:]); e != nil {
		return e
	}
	token := "runtug-ready-" + hex.EncodeToString(nonce[:])
	if e := a.must(ctx, "herdr", "pane", "run", pane, "printf", "%s\\n", token); e != nil {
		return e
	}
	ready = false
	for n := 0; n < 20 && a.Now().Before(deadline); n++ {
		r := a.call(ctx, "herdr", "agent", "read", pane, "--source", "visible", "--lines", "40")
		for _, line := range cleanScreen(r.Out) {
			if line == token {
				ready = true
			}
		}
		if ready {
			break
		}
		if e := a.Sleep(ctx, time.Second); e != nil {
			return e
		}
	}
	if !ready {
		return fail(70, "shell command execution not proven")
	}
	var args []string
	if seat.Kind == "devin" || seat.Kind == "cmd" {
		args = append([]string{"pane", "run", pane, seat.Kind}, argv...)
	} else {
		args = append([]string{"agent", "start", s.Label, "--kind", seat.Kind, "--pane", pane, "--timeout", fmt.Sprint(timeout.Milliseconds()), "--"}, argv...)
	}
	if e := a.must(ctx, "herdr", args...); e != nil {
		return e
	}
	if seat.Kind == "devin" || seat.Kind == "cmd" {
		if e := a.must(ctx, "herdr", "agent", "wait", pane, "--until", "idle", "--timeout", fmt.Sprint(timeout.Milliseconds())); e != nil {
			return e
		}
		if e := a.must(ctx, "herdr", "agent", "rename", pane, s.Label); e != nil {
			return e
		}
	}
	foreground := false
	for n := 0; n < 30; n++ {
		p, e := a.pinfo(ctx, pane)
		if e != nil {
			return e
		}
		for _, proc := range p.Processes {
			name := filepath.Base(proc.Argv0)
			if slices.Contains(executables(seat.Kind), name) {
				foreground = true
			}
		}
		if foreground {
			break
		}
		if e = a.Sleep(ctx, time.Second); e != nil {
			return e
		}
	}
	if !foreground {
		return fail(70, "agent not in foreground; brief withheld")
	}
	if seat.Kind == "devin" {
		r := a.call(ctx, "herdr", "agent", "explain", pane)
		if e := check(r, "Devin identity proof"); e != nil {
			return e
		}
		if !strings.Contains(r.Out, "welcome_prompt_footer") {
			return fail(70, "Devin identity not proven")
		}
	}
	if seat.Kind == "pi" || seat.Kind == "cmd" {
		if e := a.modelProof(ctx, seat, l, s.CWD, pane, ""); e != nil {
			return e
		}
	}
	return a.must(ctx, "herdr", "agent", "wait", pane, "--until", "idle", "--timeout", fmt.Sprint(timeout.Milliseconds()))
}
func executables(kind string) []string {
	switch kind {
	case "claude":
		return []string{"claude", "claude-code"}
	case "devin":
		return []string{"devin", "devin-cli"}
	case "grok":
		return []string{"grok", "grok-build"}
	case "kimi":
		return []string{"kimi-code", "kimi"}
	case "cmd":
		return []string{"cmd", "command-code"}
	case "kiro":
		return []string{"kiro", "kiro-cli"}
	case "opencode":
		return []string{"opencode", "open-code"}
	}
	return []string{kind}
}
func (a *App) deliver(ctx context.Context, kind, pane, brief string) error {
	before, e := a.status(ctx, pane)
	if e != nil {
		return e
	}
	if before != "idle" && before != "done" {
		return fail(76, "agent must be idle before initial brief")
	}
	if kind == "cmd" {
		if e = a.must(ctx, "herdr", "pane", "send-text", pane, brief); e != nil {
			return e
		}
		if e = a.must(ctx, "herdr", "pane", "send-keys", pane, "return"); e != nil {
			return e
		}
	} else {
		if e = a.must(ctx, "herdr", "agent", "prompt", pane, brief); e != nil {
			return e
		}
	}
	for attempt := 0; attempt < 2; attempt++ {
		for n := 0; n < 15; n++ {
			status, e := a.status(ctx, pane)
			if e != nil {
				return e
			}
			visible, e := a.visible(ctx, pane)
			if e != nil {
				return e
			}
			// A pasted chip or the brief in the composer is not proof. Require the
			// cold idle pane to start working, corroborated by no unsubmitted chip.
			if status == "working" && !strings.Contains(visible, "[Pasted text") {
				return nil
			}
			if e = a.Sleep(ctx, time.Second); e != nil {
				return e
			}
		}
		// Retry the submit key, never paste a second copy of an ambiguous payload.
		if attempt == 0 {
			scope := "agent"
			if kind == "cmd" {
				scope = "pane"
			}
			if e = a.must(ctx, "herdr", scope, "send-keys", pane, "return"); e != nil {
				return e
			}
		}
	}
	return fail(76, "brief submission unconfirmed after one submit retry")
}
func (a *App) registerLane(ctx context.Context, lane, parent, pane, role, machine string) error {
	if str(a.Config.Hub, "hub_url") == "" {
		return fail(70, "hub lane config missing")
	}
	args := []string{"lanes", "add", lane, "--machine", machine, "--pane", pane, "--parent", parent}
	args = append(args, a.Config.hubArgs(role)...)
	return a.must(ctx, "panewire", args...)
}
func (a *App) hubGate(ctx context.Context, cwd, gate string) error {
	if str(a.Config.Hub, "hub_token_env") == "" {
		return nil
	}
	pool := ""
	for _, word := range strings.Fields(strings.SplitN(gate, "\n", 2)[0]) {
		if strings.HasPrefix(word, "pool=") {
			pool = strings.TrimPrefix(word, "pool=")
		}
	}
	if !validID(pool) {
		return fail(70, "quota gate response lacks pool")
	}
	args := []string{"place", "--class", "worker", "--cwd", cwd, "--pool", pool, "--hub-url", str(a.Config.Hub, "hub_url"), "--hub-token-env", str(a.Config.Hub, "hub_token_env")}
	if v := str(a.Config.Hub, "hub_cf_env"); v != "" {
		args = append(args, "--hub-cf-env", v)
	}
	r := a.call(ctx, "panewire", args...)
	switch r.Code {
	case 0:
		return nil
	case 4:
		a.warn("hub quota unavailable; using local gate")
		return nil
	case 2, 5, 6, 70:
		return check(r, "hub quota policy")
	default:
		return fail(70, "hub quota policy failed closed")
	}
}
