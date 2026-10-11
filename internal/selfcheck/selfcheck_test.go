package selfcheck

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRunReportsChecks(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GROK_BRIDGE_GROK_HOME", home)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "grok"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	cs := Run(context.Background(), Options{Bind: "127.0.0.1:1", Auth: func(context.Context, string) (bool, string) { return true, "logged in" }})
	got := map[string]Check{}
	for _, c := range cs {
		got[c.Name] = c
	}
	for _, n := range []string{"grok_binary", "grok_auth", "sessions_dir", "agent_port", "run_user"} {
		if _, ok := got[n]; !ok {
			t.Fatalf("missing check %s in %+v", n, cs)
		}
	}
	if !got["grok_binary"].OK || !got["sessions_dir"].OK || !got["agent_port"].OK {
		t.Fatalf("unexpected failures: %+v", cs)
	}
	if Failed(cs) {
		t.Fatal("no fatal expected")
	}
}

func TestMissingBinaryIsFatal(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("GROK_BRIDGE_GROK_HOME", t.TempDir())
	cs := Run(context.Background(), Options{Bind: "127.0.0.1:1"})
	if !Failed(cs) {
		t.Fatalf("expected fatal on missing binary: %+v", cs)
	}
}
