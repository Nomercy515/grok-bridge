package buildsessions

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGrokHomeResolutionOrder(t *testing.T) {
	t.Setenv("GROK_BRIDGE_GROK_HOME", "")
	t.Setenv("GROK_HOME", "")

	bridgeHome := t.TempDir()
	grokHome := t.TempDir()

	t.Setenv("GROK_BRIDGE_GROK_HOME", bridgeHome)
	t.Setenv("GROK_HOME", grokHome)
	if got := GrokHome(); got != filepath.Clean(bridgeHome) {
		t.Fatalf("prefer GROK_BRIDGE_GROK_HOME: got %q want %q", got, bridgeHome)
	}

	t.Setenv("GROK_BRIDGE_GROK_HOME", "")
	if got := GrokHome(); got != filepath.Clean(grokHome) {
		t.Fatalf("prefer GROK_HOME: got %q want %q", got, grokHome)
	}

	t.Setenv("GROK_HOME", "")
	got := GrokHome()
	if got == "" {
		t.Fatal("expected fallback ~/.grok")
	}
	if filepath.Base(got) != ".grok" {
		t.Fatalf("fallback base: %q", got)
	}
}

func TestListEmptyWhenMissing(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-grok-home")
	t.Setenv("GROK_BRIDGE_GROK_HOME", missing)
	t.Setenv("GROK_HOME", "")
	if got := List(); len(got) != 0 {
		t.Fatalf("expected empty, got %+v", got)
	}
}

func writeFixture(t *testing.T, home, cwd, sid string, summary map[string]any, lines []string) string {
	t.Helper()
	escaped := url.PathEscape(cwd)
	dir := filepath.Join(home, "sessions", escaped, sid)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.MarshalIndent(summary, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "summary.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	var body string
	for _, ln := range lines {
		body += ln + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "chat_history.jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestListAndGetNested(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_BRIDGE_GROK_HOME", home)
	t.Setenv("GROK_HOME", "")

	sid := "sess-abc-123"
	created := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	updated := time.Date(2026, 9, 6, 18, 30, 0, 0, time.UTC)
	writeFixture(t, home, "/Users/me/proj", sid, map[string]any{
		"info":              map[string]any{"id": sid, "cwd": "/Users/me/proj"},
		"generated_title":   "Wire up auth",
		"created_at":        created.Format(time.RFC3339),
		"updated_at":        updated.Format(time.RFC3339),
		"num_chat_messages": 2,
	}, []string{
		`{"role":"system","content":"HUGE SYSTEM PROMPT IGNORE"}`,
		`{"id":"m1","role":"user","content":"please fix login","timestamp":"2026-09-06T18:00:00Z"}`,
		`{"id":"m2","role":"assistant","content":"Sure — looking at auth next.","timestamp":"2026-09-06T18:01:00Z"}`,
		`{"id":"m3","role":"user","content":[{"type":"text","text":"also add tests"}],"timestamp":"2026-09-06T18:02:00Z"}`,
		`{"type":"tool_call","name":"read"}`,
	})

	list := List()
	if len(list) != 1 {
		t.Fatalf("list len=%d %+v", len(list), list)
	}
	if list[0].ID != "build:"+sid {
		t.Fatalf("id=%q", list[0].ID)
	}
	if list[0].Source != "build" {
		t.Fatalf("source=%q", list[0].Source)
	}
	if list[0].Title != "Wire up auth" {
		t.Fatalf("title=%q", list[0].Title)
	}
	if list[0].MessageCount != 2 {
		t.Fatalf("count=%d", list[0].MessageCount)
	}

	sess, err := Get("build:" + sid)
	if err != nil || sess == nil {
		t.Fatalf("get: %v", err)
	}
	if !sess.ReadOnly || sess.Source != "build" {
		t.Fatalf("flags: %+v", sess)
	}
	if len(sess.Messages) != 3 {
		t.Fatalf("messages=%d %+v", len(sess.Messages), sess.Messages)
	}
	if sess.Messages[0].Content != "please fix login" {
		t.Fatalf("msg0=%q", sess.Messages[0].Content)
	}
	if sess.Messages[2].Content != "also add tests" {
		t.Fatalf("msg2=%q", sess.Messages[2].Content)
	}
}

func TestListFlatLayout(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_BRIDGE_GROK_HOME", home)
	t.Setenv("GROK_HOME", "")
	sid := "flat-1"
	dir := filepath.Join(home, "sessions", sid)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	summary := map[string]any{
		"info":         map[string]any{"id": sid},
		"title":        "Flat session",
		"updated_at":   1780000000.0,
		"num_messages": 1,
	}
	b, _ := json.Marshal(summary)
	_ = os.WriteFile(filepath.Join(dir, "summary.json"), b, 0o644)
	_ = os.WriteFile(filepath.Join(dir, "chat_history.jsonl"), []byte(
		`{"role":"user","content":"hi"}`+"\n"+`{"role":"assistant","content":"yo"}`+"\n",
	), 0o644)

	list := List()
	if len(list) != 1 || list[0].ID != "build:"+sid {
		t.Fatalf("%+v", list)
	}
	sess, err := Get(sid)
	if err != nil || sess == nil || len(sess.Messages) != 2 {
		t.Fatalf("get flat: %v %+v", err, sess)
	}
}

func TestGetMissing(t *testing.T) {
	t.Setenv("GROK_BRIDGE_GROK_HOME", t.TempDir())
	t.Setenv("GROK_HOME", "")
	_, err := Get("build:does-not-exist")
	if err == nil {
		t.Fatal("expected error")
	}
}
