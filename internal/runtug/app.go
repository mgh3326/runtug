package runtug

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

type App struct {
	EnforceTrust bool
	Runner       Runner
	Quota        QuotaGate
	Config       Config
	ConfigPath   string
	Policy       Policy
	Out, Err     io.Writer
	Jobs, Home   string
	Sleep        func(context.Context, time.Duration) error
	Now          func() time.Time
	Bins         map[string]string
	Source       PlacementSource
	Wake         Waker
	LoadRatio    func(context.Context) (float64, error)
}

func Main(ctx context.Context, args []string, out, errout io.Writer) int {
	a, e := New(out, errout)
	if e == nil {
		e = a.Run(ctx, args)
	}
	if e != nil {
		fmt.Fprintln(errout, "runtug:", e)
		var x *ExitError
		if errors.As(e, &x) {
			return x.Code
		}
		return 1
	}
	return 0
}
func New(out, errout io.Writer) (*App, error) {
	path := configPath(errout)
	c, e := LoadConfig(path)
	if e != nil {
		return nil, e
	}
	p, e := LoadPolicy(os.Getenv("RUNTUG_POLICY"))
	if e != nil {
		return nil, e
	}
	h, _ := os.UserHomeDir()
	a := &App{Runner: ExecRunner{}, Config: c, ConfigPath: path, Policy: p, Out: out, Err: errout, Home: h, Jobs: filepath.Join(h, "work", "herdr-inbox", "jobs"), Now: time.Now, Sleep: sleepContext, Bins: map[string]string{}}
	if v := os.Getenv("ARBITER_INBOX_ROOT"); v != "" {
		a.Jobs = v
	}
	for bin, env := range map[string]string{"herdr": "HERDR_BIN", "arbiter": "ARBITER_BIN", "handoffkeep": "HANDOFFKEEP_BIN", "panewire": "PANEWIRE_BIN"} {
		a.Bins[bin] = bin
		if v := os.Getenv(env); v != "" {
			a.Bins[bin] = v
		}
	}
	// The quota CLI is pacelever; RUNTUG_QUOTA_BIN is an explicit override.
	if os.Getenv("SCOPEFUEL_BIN") != "" && os.Getenv("RUNTUG_QUOTA_BIN") == "" {
		fmt.Fprintln(errout, "runtug: warning: SCOPEFUEL_BIN is retired and ignored; the quota CLI defaults to pacelever, override it with RUNTUG_QUOTA_BIN")
	}
	quota := "pacelever"
	if v := os.Getenv("RUNTUG_QUOTA_BIN"); v != "" {
		quota = v
	}
	a.Quota = CLIQuota{a.Runner, quota}
	a.Source = fleetSource{a}
	a.Wake = cliWaker{a}
	a.LoadRatio = a.loadRatio
	a.EnforceTrust = true
	return a, nil
}
func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
func (a *App) call(ctx context.Context, bin string, args ...string) Result {
	name := a.Bins[bin]
	if name == "" {
		name = bin
	}
	return a.Runner.Run(ctx, Command{Name: name, Args: args})
}
func (a *App) must(ctx context.Context, bin string, args ...string) error {
	return check(a.call(ctx, bin, args...), bin)
}

// envOr reads a renamed environment variable. For one release the retired
// name still applies, with a one-line warning to switch.
func (a *App) envOr(name, retired string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	if v := os.Getenv(retired); v != "" {
		a.warn("%s is retired; set %s instead", retired, name)
		return v
	}
	return ""
}
func (a *App) warn(format string, args ...any) {
	fmt.Fprintf(a.Err, "runtug: warning: "+format+"\n", args...)
}
func flags(name string) *flag.FlagSet {
	f := flag.NewFlagSet(name, flag.ContinueOnError)
	f.SetOutput(io.Discard)
	return f
}

var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func validID(s string) bool { return idPattern.MatchString(s) && s != "." && s != ".." }
func (a *App) Run(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(a.Out, "Usage: runtug <spawn|done|escalate|restore|reap|prune|lanes-missing|hosts|profiles|find|name-sync|artifacts> [options]")
		return nil
	}
	if len(args) > 1 && (args[1] == "--help" || args[1] == "-h") {
		return a.help(args[0])
	}
	if a.EnforceTrust && (args[0] == "spawn" || args[0] == "_spawn-stdin" || args[0] == "done" || args[0] == "escalate" || args[0] == "restore" || args[0] == "reap" || args[0] == "prune" || args[0] == "artifacts" || args[0] == "_watch") {
		if e := a.trust(); e != nil {
			return e
		}
	}
	switch args[0] {
	case "spawn":
		s, e := parseSpawn(args[1:])
		if e != nil {
			return e
		}
		return a.Spawn(ctx, s)
	case "done", "escalate", "restore":
		return a.lifecycle(ctx, args[0], args[1:])
	case "reap", "prune":
		return a.hygiene(ctx, args[0], args[1:])
	case "hosts", "profiles", "find", "name-sync", "lanes-missing":
		return a.diagnostics(ctx, args[0], args[1:])
	case "artifacts":
		return a.artifacts(ctx, args[1:])
	case "_artifact-inventory":
		return a.artifactInventory()
	case "_watch":
		return a.watch(ctx, args[1:])
	case "_spawn-stdin":
		return a.spawnStdin(ctx)
	default:
		return fail(exitUsage, "unknown command %q", args[0])
	}
}
func (a *App) help(cmd string) error {
	switch cmd {
	case "spawn":
		fmt.Fprintln(a.Out, "Usage: runtug spawn -c CWD [-m PROFILE|auto] -p BRIEF -w WORKSPACE -l LABEL --t T0..T3 [--task ID|--parent-job JOB] [--effort E] [--role worker|builder|tester|reviewer|counsel] [--lane L] [--parent L] [--owner L] [--job ID] [--host auto|local|NAME] [--tags a,b,c] [--report PATH] [-L live|mock] [--keep] [--job-dup-ok] [--task-hk-bypass] [--allow-escalation] [--override-reason TEXT] [--operator-request REF] [--requested-by NAME] [--purpose PURPOSE] [--lane-defer HOST] [--report-relative PATH] [--wait] [--wait-timeout DUR] [--dry-run]")
	case "done":
		fmt.Fprintln(a.Out, "Usage: runtug done JOB [--report PATH]")
	case "escalate":
		fmt.Fprintln(a.Out, "Usage: runtug escalate JOB --question TEXT [--report PATH]")
	case "restore":
		fmt.Fprintln(a.Out, "Usage: runtug restore JOB (rebind a released job to its verified existing pane)")
	case "reap", "prune":
		fmt.Fprintf(a.Out, "Usage: runtug %s [--job ID[,ID]] [--lane L] [--grace 10m] [--include-builders] [--worktrees] [--apply]\n", cmd)
	case "artifacts":
		fmt.Fprintln(a.Out, "Usage: runtug artifacts move SRC --dest DEST [--source-host HOST] [--apply] | sweep [--job JOB] [--age 7d] [--policy FILE] [--apply]")
	default:
		fmt.Fprintf(a.Out, "Usage: runtug %s [options]; see README and docs/config.md\n", cmd)
	}
	return nil
}
