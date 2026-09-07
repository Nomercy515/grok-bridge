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
}

func TestPublicViewPresent(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, Filename)
	if err := os.WriteFile(p, []byte(`{"source":"tailscale","url":"https://host.ts.net:4020/","magicdns":"host.ts.net","tailscale_ipv4":"100.1.2.3","port":4020,"updated_at":"2026-01-01T00:00:00Z"}`), 0644); err != nil {
		t.Fatal(err)
	}
	v := PublicView(dir)
	if v["configured"] != true || v["url"] != "https://host.ts.net:4020/" {
		t.Fatalf("%v", v)
	}
}
