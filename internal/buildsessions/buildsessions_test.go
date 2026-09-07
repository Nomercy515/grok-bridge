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
	if sess.ReadOnly || sess.Source != "build" {
		t.Fatalf("flags: %+v (want live build, not readonly)", sess)
	}
	raw, cwd, err := ResolveMeta("build:" + sid)
	if err != nil || raw != sid || cwd != "/Users/me/proj" {
		t.Fatalf("ResolveMeta: raw=%q cwd=%q err=%v", raw, cwd, err)
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

func TestGetContextFromSignals(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_BRIDGE_GROK_HOME", home)
	t.Setenv("GROK_HOME", "")

	sid := "sess-ctx-1"
	dir := writeFixture(t, home, "/Users/me/proj", sid, map[string]any{
		"info":              map[string]any{"id": sid, "cwd": "/Users/me/proj"},
		"generated_title":   "Context meter",
		"updated_at":        "2026-09-06T18:30:00Z",
		"num_chat_messages": 1,
	}, []string{
		`{"id":"m1","role":"user","content":"hi","timestamp":"2026-09-06T18:00:00Z"}`,
	})
	signals := map[string]any{
		"contextTokensUsed":   310965,
		"contextWindowTokens": 500000,
		"contextWindowUsage":  62,
	}
	b, _ := json.MarshalIndent(signals, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "signals.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}

	sess, err := Get("build:" + sid)
	if err != nil || sess == nil {
		t.Fatalf("get: %v", err)
	}
	if sess.ContextTokensUsed == nil || *sess.ContextTokensUsed != 310965 {
		t.Fatalf("context_tokens_used=%v", sess.ContextTokensUsed)
	}
	if sess.ContextWindowTokens == nil || *sess.ContextWindowTokens != 500000 {
		t.Fatalf("context_window_tokens=%v", sess.ContextWindowTokens)
	}
	if sess.ContextWindowUsage == nil || *sess.ContextWindowUsage != 62 {
		t.Fatalf("context_window_usage=%v", sess.ContextWindowUsage)
	}
}

func TestGetWithoutSignalsOmitsContext(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_BRIDGE_GROK_HOME", home)
	t.Setenv("GROK_HOME", "")
	sid := "sess-no-sig"
	writeFixture(t, home, "/tmp", sid, map[string]any{
		"info":  map[string]any{"id": sid},
		"title": "No signals",
	}, []string{
		`{"role":"user","content":"hi"}`,
	})
	sess, err := Get("build:" + sid)
	if err != nil || sess == nil {
		t.Fatalf("get: %v", err)
	}
	if sess.ContextTokensUsed != nil || sess.ContextWindowTokens != nil || sess.ContextWindowUsage != nil {
		t.Fatalf("expected omitted context fields, got used=%v window=%v usage=%v",
			sess.ContextTokensUsed, sess.ContextWindowTokens, sess.ContextWindowUsage)
	}
}

func TestUnwrapUserQueryAndSkipSynthetic(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_BRIDGE_GROK_HOME", home)
	t.Setenv("GROK_HOME", "")

	sid := "sess-unwrap-1"
	writeFixture(t, home, "/tmp/proj", sid, map[string]any{
		"info":              map[string]any{"id": sid, "cwd": "/tmp/proj"},
		"generated_title":   "Unwrap query",
		"updated_at":        "2026-09-07T12:00:00Z",
		"num_chat_messages": 2,
	}, []string{
		// Reminder-only user line → skipped (not a You bubble)
		`{"id":"noise1","role":"user","content":"<system-reminder>Always follow the rules.</system-reminder>","timestamp":"2026-09-07T12:00:00Z"}`,
		// user_info-only → skipped
		`{"id":"noise2","role":"user","content":"<user_info>\nOS: Linux\n</user_info>","timestamp":"2026-09-07T12:00:01Z"}`,
		// synthetic_reason without user_query → skipped
		`{"id":"noise3","role":"user","synthetic_reason":"context_inject","content":"injected scaffolding","timestamp":"2026-09-07T12:00:02Z"}`,
		// Wrapped query alone → unwrapped to plain text
		`{"id":"m1","role":"user","content":"<user_query>hi there</user_query>","timestamp":"2026-09-07T12:01:00Z"}`,
		`{"id":"m2","role":"assistant","content":"Hello!","timestamp":"2026-09-07T12:01:05Z"}`,
		// Query with surrounding reminder → prefer unwrapped query inners
		`{"id":"m3","role":"user","content":"<system-reminder>note</system-reminder>\n<user_query>fix the bug</user_query>","timestamp":"2026-09-07T12:02:00Z"}`,
		// Case-insensitive tag
		`{"id":"m4","role":"user","content":"<USER_QUERY>Case OK</USER_QUERY>","timestamp":"2026-09-07T12:03:00Z"}`,
		// Plain user text still works
		`{"id":"m5","role":"user","content":"plain hi","timestamp":"2026-09-07T12:04:00Z"}`,
	})

	sess, err := Get("build:" + sid)
	if err != nil || sess == nil {
		t.Fatalf("get: %v", err)
	}

	var users []string
	for _, m := range sess.Messages {
		if m.Role == "user" {
			users = append(users, m.Content)
		}
	}
	want := []string{"hi there", "fix the bug", "Case OK", "plain hi"}
	if len(users) != len(want) {
		t.Fatalf("user msgs=%d %+v want %d %+v (all msgs=%+v)", len(users), users, len(want), want, sess.Messages)
	}
	for i := range want {
		if users[i] != want[i] {
			t.Fatalf("users[%d]=%q want %q", i, users[i], want[i])
		}
	}
}

func TestUnwrapUserQueriesHelper(t *testing.T) {
	got, ok := unwrapUserQueries(`<user_query>a</user_query>`)
	if !ok || got != "a" {
		t.Fatalf("simple: got=%q ok=%v", got, ok)
	}
	got, ok = unwrapUserQueries(`pre <user_query>one</user_query> mid <user_query>two</user_query>`)
	if !ok || got != "one\ntwo" {
		t.Fatalf("multi: got=%q ok=%v", got, ok)
	}
	got, ok = unwrapUserQueries("no tags here")
	if ok || got != "no tags here" {
		t.Fatalf("none: got=%q ok=%v", got, ok)
	}
}
