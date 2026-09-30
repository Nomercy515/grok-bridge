package buildsessions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestProbeStatusMissingSessionsDir(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent-grok")
	t.Setenv("GROK_BRIDGE_GROK_HOME", missing)
	t.Setenv("GROK_HOME", "")
	st := ProbeStatus()
	if st.Available {
		t.Fatalf("expected unavailable: %+v", st)
	}
	if st.GrokHome != filepath.Clean(missing) {
		t.Fatalf("grok_home=%q", st.GrokHome)
	}
	if st.Reason != "missing_sessions_dir" {
		t.Fatalf("%+v", st)
	}
	if st.SessionCount != 0 {
		t.Fatalf("count=%d", st.SessionCount)
	}
}

func TestProbeStatusEmptySessions(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GROK_BRIDGE_GROK_HOME", home)
	t.Setenv("GROK_HOME", "")
	st := ProbeStatus()
	if !st.Available || st.Reason != "empty" || st.SessionCount != 0 {
		t.Fatalf("%+v", st)
	}
}

func TestProbeStatusWithSessions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_BRIDGE_GROK_HOME", home)
	t.Setenv("GROK_HOME", "")
	sid := "sess-status-1"
	created := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	writeFixture(t, home, "/tmp/proj", sid, map[string]any{
		"info":              map[string]any{"id": sid, "cwd": "/tmp/proj"},
		"generated_title":   "Status probe",
		"created_at":        created.Format(time.RFC3339),
		"updated_at":        created.Format(time.RFC3339),
		"num_chat_messages": 2,
	}, []string{
		`{"id":"m1","role":"user","content":"hi","timestamp":"2026-09-01T12:00:00Z"}`,
		`{"id":"m2","role":"assistant","content":"yo","timestamp":"2026-09-01T12:00:01Z"}`,
	})
	st := ProbeStatus()
	if !st.Available || st.Reason != "ok" || st.SessionCount < 1 {
		t.Fatalf("%+v", st)
	}
	b, _ := json.Marshal(st)
	if len(b) < 10 {
		t.Fatal(string(b))
	}
}
