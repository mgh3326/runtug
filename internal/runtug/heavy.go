package runtug

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func tryLock(f *os.File) bool {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil
}
func slotPath(base string, slot int) string {
	if slot == 0 {
		return base
	}
	return fmt.Sprintf("%s.slot%d", base, slot)
}
func secureOpen(path string, create bool) (*os.File, error) {
	flags := os.O_RDWR | syscall.O_NOFOLLOW
	if create {
		flags |= os.O_CREATE
	}
	f, e := os.OpenFile(path, flags, 0600)
	if e != nil {
		return nil, e
	}
	info, e := f.Stat()
	if e != nil {
		f.Close()
		return nil, e
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	// Our own lock must be 0600-style (no group/other write). A root:root lock
	// is also trusted: a host-wide shared lock under /tmp must be root-owned to
	// be opened by other users under Linux protected_regular, and is shared via
	// group/ACL write. Other-write and symlinks/hard links are always refused.
	if !ok || !info.Mode().IsRegular() || st.Nlink != 1 || !lockOwnerOK(st.Uid, st.Gid, info.Mode().Perm(), uint32(os.Getuid())) {
		f.Close()
		return nil, fail(70, "insecure lock file %s", path)
	}
	return f, nil
}

// heavyLockPath is the host-wide heavy lock. It is /tmp, never os.TempDir():
// on macOS TMPDIR is a per-user /var/folders path, so lanes and CI runners
// on one host would hold different locks and never exclude each other. The
// path matches wrk heavy's default so both tools share one lock.
func heavyLockPath(host string) string {
	if v := os.Getenv("WRK_HEAVY_LOCK"); v != "" {
		return v
	}
	return filepath.Join("/tmp", "wrk-heavy-"+host+".lock")
}

// lockOwnerOK accepts our own lock without group/other write, or a root:root
// shared lock without other write.
func lockOwnerOK(uid, gid uint32, perm os.FileMode, self uint32) bool {
	if uid == self && perm&0022 == 0 {
		return true
	}
	return uid == 0 && gid == 0 && perm&0002 == 0
}
func envInt(key string, def int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	n, e := strconv.Atoi(v)
	if e != nil || n < 0 {
		return 0, fail(78, "%s must be a nonnegative integer", key)
	}
	return n, nil
}
func (a *App) loadRatio(ctx context.Context) (float64, error) {
	var text string
	if runtime.GOOS == "linux" {
		b, e := os.ReadFile("/proc/loadavg")
		if e != nil {
			return 0, e
		}
		text = string(b)
	} else {
		r := a.call(ctx, "sysctl", "-n", "vm.loadavg")
		if e := check(r, "load measurement"); e != nil {
			return 0, e
		}
		text = r.Out
	}
	fields := strings.Fields(strings.Trim(text, "{} \n"))
	if len(fields) < 2 {
		return 0, fail(70, "host load unavailable")
	}
	load, e := strconv.ParseFloat(fields[1], 64)
	return load / float64(runtime.NumCPU()), e
}
func (a *App) heavy(ctx context.Context, args []string) error {
	host, _ := os.Hostname()
	base := heavyLockPath(host)
	limit, e := envInt("WRK_HEAVY_MAX", int(integer(a.Config.Local, "heavy_max", 1)))
	if e != nil {
		return e
	}
	if limit > 1024 {
		return fail(78, "heavy limit exceeds 1024")
	}
	if len(args) == 1 && args[0] == "status" {
		fmt.Fprintf(a.Out, "lock=%s limit=%d load_wait=%t\n", base, limit, boolean(a.Config.Local, "heavy_load_wait", true))
		for n := 0; n < limit; n++ {
			f, e := secureOpen(slotPath(base, n), false)
			if os.IsNotExist(e) {
				continue
			}
			if e != nil {
				return e
			}
			if !tryLock(f) {
				b, _ := os.ReadFile(f.Name())
				fmt.Fprintf(a.Out, "holder slot=%d %s\n", n, strings.TrimSpace(string(b)))
			}
			f.Close()
		}
		return nil
	}
	if limit == 0 {
		return fail(78, "heavy_max=0 refuses builds on this host")
	}
	queueKey := ""
	if len(args) >= 2 && args[0] == "--queue-key" {
		queueKey = args[1]
		args = args[2:]
		if !validID(queueKey) {
			return fail(2, "invalid queue key")
		}
	}
	if len(args) < 2 || args[0] != "--" {
		return fail(2, "heavy requires -- COMMAND [ARGS]")
	}
	args = args[1:]
	class := os.Getenv("WRK_HEAVY_CLASS")
	if class == "" {
		class = "lane"
	}
	if class != "ci" && class != "lane" {
		return fail(78, "heavy class must be ci or lane")
	}
	capSeconds, e := envInt("WRK_HEAVY_WAIT_CAP", 1200)
	if e != nil {
		return e
	}
	start := a.Now()
	deadline := start.Add(time.Duration(capSeconds) * time.Second)
	var held *os.File
	slot := -1
	// Inherited flock descriptors prevent deadlock on nested heavy invocations.
	if spec := os.Getenv("WRK_HEAVY_HELD_FD"); spec != "" {
		fd, path, ok := strings.Cut(spec, ":")
		n, err := strconv.Atoi(fd)
		if ok && err == nil && n >= 3 {
			for i := 0; i < limit; i++ {
				if path != slotPath(base, i) {
					continue
				}
				f := os.NewFile(uintptr(n), path)
				info, err := f.Stat()
				disk, derr := os.Stat(path)
				if err == nil && derr == nil && os.SameFile(info, disk) && tryLock(f) {
					held = f
					slot = i
				}
			}
		}
	}
	nested := held != nil
	for held == nil {
		for n := 0; n < limit; n++ {
			f, e := secureOpen(slotPath(base, n), true)
			if e != nil {
				return e
			}
			if tryLock(f) {
				held = f
				slot = n
				break
			}
			f.Close()
		}
		if held != nil {
			break
		}
		if !a.Now().Before(deadline) {
			return fail(75, "heavy wait cap expired; command not run")
		}
		if e = a.Sleep(ctx, 250*time.Millisecond); e != nil {
			return e
		}
	}
	defer held.Close()
	// The command inherits the descriptor, so orphaned descendants retain the
	// host slot until the kernel closes their last copy.
	if !nested {
		for boolean(a.Config.Local, "heavy_load_wait", true) {
			r := a.call(ctx, "sysctl", "-n", "vm.loadavg")
			if runtime.GOOS == "linux" {
				b, err := os.ReadFile("/proc/loadavg")
				if err != nil {
					return err
				}
				r = Result{Out: string(b)}
			}
			fields := strings.Fields(strings.Trim(r.Out, "{} \n"))
			if len(fields) < 2 {
				return fail(70, "host load unavailable")
			}
			load, err := strconv.ParseFloat(fields[1], 64)
			if err != nil {
				return err
			}
			if load/float64(runtime.NumCPU()) < 1 {
				break
			}
			if !a.Now().Before(deadline) {
				return fail(75, "heavy load wait cap expired")
			}
			if e = a.Sleep(ctx, time.Second); e != nil {
				return e
			}
		}
	}
	meta := fmt.Sprintf("pid=%d class=%s slot=%d job=%s queue_key=%s", os.Getpid(), class, slot, os.Getenv("ARBITER_JOB"), queueKey)
	if !nested {
		if e = held.Truncate(0); e != nil {
			return e
		}
		if _, e = held.WriteAt([]byte(meta+"\n"), 0); e != nil {
			return e
		}
	}
	cmd := exec.CommandContext(ctx, "nice", append([]string{"-n", "10"}, args...)...)
	holdStart := a.Now()
	cmd.Stdout = a.Out
	cmd.Stderr = a.Err
	cmd.ExtraFiles = []*os.File{held}
	cmd.Env = append(os.Environ(), "WRK_HEAVY_HELD_FD=3:"+slotPath(base, slot))
	rc := 0
	if e = cmd.Run(); e != nil {
		rc = 1
		if x, ok := e.(*exec.ExitError); ok {
			rc = x.ExitCode()
		}
	}
	record := map[string]any{"ts": a.Now().UTC().Format(time.RFC3339Nano), "kind": "run", "pid": os.Getpid(), "class": class, "slot": slot, "limit": limit, "wait_s": holdStart.Sub(start).Seconds(), "hold_s": a.Now().Sub(holdStart).Seconds(), "rc": rc}
	if nested {
		record["kind"] = "nested"
	}
	log := os.Getenv("WRK_HEAVY_LOG")
	if log == "" {
		log = filepath.Join(a.Home, ".local", "state", "wrk", "heavy.log")
	}
	if os.MkdirAll(filepath.Dir(log), 0700) == nil {
		if f, err := os.OpenFile(log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600); err == nil {
			b, _ := json.Marshal(record)
			_, _ = f.Write(append(b, '\n'))
			_ = f.Close()
		}
	}
	if rc != 0 {
		return fail(rc, "heavy command exited %d", rc)
	}
	return nil
}
