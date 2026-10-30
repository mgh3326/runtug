package runtug

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigPathFleetResolution(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("FLEET_HOSTS_CONFIG", "")
	t.Setenv("WRK_HOSTS_CONFIG", "")

	var warn bytes.Buffer
	if got := configPath(&warn); got != filepath.Join(xdg, "fleet", "hosts.toml") || warn.Len() != 0 {
		t.Fatalf("default path = %q warn=%q", got, warn.String())
	}

	t.Setenv("FLEET_HOSTS_CONFIG", "/tmp/explicit-fleet.toml")
	if got := configPath(&warn); got != "/tmp/explicit-fleet.toml" {
		t.Fatalf("FLEET_HOSTS_CONFIG must win: %q", got)
	}
	t.Setenv("FLEET_HOSTS_CONFIG", "")

	// One-release fallback: only the retired wrk path exists.
	old := filepath.Join(xdg, "wrk", "hosts.toml")
	if e := os.MkdirAll(filepath.Dir(old), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(old, []byte("[local]\n"), 0600); e != nil {
		t.Fatal(e)
	}
	warn.Reset()
	if got := configPath(&warn); got != old || !strings.Contains(warn.String(), "fleet") {
		t.Fatalf("retired fallback = %q warn=%q", got, warn.String())
	}

	// The fleet path wins once both exist.
	next := filepath.Join(xdg, "fleet", "hosts.toml")
	if e := os.MkdirAll(filepath.Dir(next), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(next, []byte("[local]\n"), 0600); e != nil {
		t.Fatal(e)
	}
	warn.Reset()
	if got := configPath(&warn); got != next || warn.Len() != 0 {
		t.Fatalf("fleet path must win: %q warn=%q", got, warn.String())
	}

	// The retired env var is ignored while the fleet config exists.
	t.Setenv("WRK_HOSTS_CONFIG", "/tmp/explicit-wrk.toml")
	warn.Reset()
	if got := configPath(&warn); got != next || !strings.Contains(warn.String(), "ignored") {
		t.Fatalf("WRK_HOSTS_CONFIG must not override the fleet config: %q warn=%q", got, warn.String())
	}

	// With the fleet file gone it still loads for one release, with a warning.
	if e := os.Remove(next); e != nil {
		t.Fatal(e)
	}
	warn.Reset()
	if got := configPath(&warn); got != "/tmp/explicit-wrk.toml" || !strings.Contains(warn.String(), "FLEET_HOSTS_CONFIG") {
		t.Fatalf("WRK_HOSTS_CONFIG fallback = %q warn=%q", got, warn.String())
	}
}
