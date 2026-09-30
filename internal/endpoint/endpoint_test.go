package endpoint

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPublicViewMissing(t *testing.T) {
	v := PublicView(t.TempDir())
	if v["configured"] != false {
		t.Fatalf("%v", v)
	}
	build, _ := v["build"].(map[string]any)
	if build == nil {
		t.Fatalf("expected build probe: %v", v)
	}
	if _, ok := build["reason"]; !ok {
		t.Fatalf("build reason missing: %v", build)
	}
}

func TestPublicViewPresent(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, Filename)
	if err := os.WriteFile(p, []byte(`{"source":"tailscale","url":"https://host.ts.net:4020/","magicdns":"host.ts.net","tailscale_ipv4":"100.1.2.3","port":4020,"updated_at":"2026-01-01T00:00:00Z"}`), 0644); err != nil {
		t.Fatal(err)
	}
	v := PublicViewWithRuntime(dir, Runtime{
		Hostname:   "desk-a",
		ListenAddr: "100.1.2.3:4020",
		Version:    "0.1.0",
		GOOS:       "linux",
		GOARCH:     "amd64",
	})
	if v["configured"] != true || v["url"] != "https://host.ts.net:4020/" {
		t.Fatalf("%v", v)
	}
	if v["hostname"] != "desk-a" || v["listen_addr"] != "100.1.2.3:4020" {
		t.Fatalf("identity: %v", v)
	}
	bi, _ := v["build_identity"].(map[string]any)
	if bi == nil || bi["version"] != "0.1.0" || bi["goos"] != "linux" {
		t.Fatalf("build_identity: %v", bi)
	}
	build, _ := v["build"].(map[string]any)
	if build == nil || build["reason"] == nil {
		t.Fatalf("build: %v", v)
	}
}

func TestPublicViewBuildUnavailable(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(t.TempDir(), "no-grok")
	t.Setenv("GROK_BRIDGE_GROK_HOME", missing)
	t.Setenv("GROK_HOME", "")
	v := PublicView(dir)
	if v["build_sessions"] != "unavailable" {
		t.Fatalf("want unavailable, got %v", v["build_sessions"])
	}
	if v["build_session_count"] != nil {
		t.Fatalf("count should be null when unavailable: %v", v["build_session_count"])
	}
}
