package runtug

import "testing"

func TestProcNameStripsExeSuffix(t *testing.T) {
	for in, want := range map[string]string{
		"/home/u/.local/lib/node_modules/@anthropic-ai/claude-code/bin/claude.exe": "claude",
		"claude":         "claude",
		"/usr/bin/devin": "devin",
		"kimi-code":      "kimi-code",
	} {
		if got := procName(in); got != want {
			t.Fatalf("procName(%q) = %q, want %q", in, got, want)
		}
	}
}
