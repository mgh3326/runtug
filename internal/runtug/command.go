package runtug

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Runner is the process boundary. Arguments never pass through a local shell.
type Runner interface {
	Run(context.Context, Command) Result
}
type Command struct {
	Name  string
	Args  []string
	Input string
	Env   []string
	Dir   string
}
type Result struct {
	Out, Err string
	Code     int
}
type ExecRunner struct{ Timeout time.Duration }

func (r ExecRunner) Run(ctx context.Context, c Command) Result {
	timeout := r.Timeout
	if timeout == 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	cmd.Stdin = strings.NewReader(c.Input)
	cmd.Dir = c.Dir
	cmd.Env = append(os.Environ(), c.Env...)
	var out, err bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &err
	code := 0
	if e := cmd.Run(); e != nil {
		code = 1
		if x, ok := e.(*exec.ExitError); ok {
			code = x.ExitCode()
		} else {
			fmt.Fprintln(&err, e)
		}
		if ctx.Err() != nil {
			code = 124
		}
	}
	return Result{out.String(), err.String(), code}
}

type ExitError struct {
	Code    int
	Message string
}

func (e *ExitError) Error() string { return e.Message }
func fail(code int, format string, args ...any) error {
	return &ExitError{code, fmt.Sprintf(format, args...)}
}
func check(r Result, what string) error {
	if r.Code != 0 {
		return fail(r.Code, "%s: %s", what, strings.TrimSpace(r.Err+" "+r.Out))
	}
	return nil
}
func writeJSON(w io.Writer, v any) error {
	b, e := encode(v)
	if e == nil {
		_, e = fmt.Fprintln(w, string(b))
	}
	return e
}
