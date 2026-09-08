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

func TestListHidesGrokOnlySessions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_BRIDGE_GROK_HOME", home)
	t.Setenv("GROK_HOME", "")
	t.Setenv("GROK_BRIDGE_INCLUDE_GROK_ONLY", "")

	// Human + grok replies: keep, even though the latest turn is assistant.
	writeFixture(t, home, "/tmp/proj", "human-1", map[string]any{
		"info":              map[string]any{"id": "human-1", "cwd": "/tmp/proj"},
		"generated_title":   "Fix login",
		"updated_at":        "2026-09-07T12:00:00Z",
		"num_chat_messages": 2,
	}, []string{
		`{"role":"user","content":"please fix login"}`,
		`{"role":"assistant","content":"Looking at auth."}`,
	})

	// Mixed: user turn plus a later grok-to-grok style assistant reply. Keep.
	writeFixture(t, home, "/tmp/proj", "mixed-1", map[string]any{
		"info":            map[string]any{"id": "mixed-1", "cwd": "/tmp/proj"},
		"generated_title": "Mixed still listed",
		"updated_at":      "2026-09-07T13:00:00Z",
	}, []string{
		`{"role":"user","content":"<user_query>add tests</user_query>"}`,
		`{"role":"assistant","content":"Spawned a helper."}`,
		`{"role":"assistant","content":"Helper finished."}`,
	})

	// Subagent: parent grok prompt stored as role=user. Hide.
	writeFixture(t, home, "/tmp/proj", "sub-1", map[string]any{
		"info":              map[string]any{"id": "sub-1", "cwd": "/tmp/proj"},
		"generated_title":   "Explore auth helpers",
		"session_kind":      "subagent",
		"parent_session_id": "human-1",
		"subagent_type":     "explore",
		"updated_at":        "2026-09-07T14:00:00Z",
	}, []string{
		`{"role":"user","content":"Find every login helper and report back."}`,
		`{"role":"assistant","content":"Searching the tree."}`,
	})

	// subagent_fork, only assistant/tool. Hide.
	writeFixture(t, home, "/tmp/proj", "subfork-1", map[string]any{
		"info":         map[string]any{"id": "subfork-1"},
		"title":        "Child fork",
		"session_kind": "subagent_fork",
		"updated_at":   "2026-09-07T14:30:00Z",
	}, []string{
		`{"role":"assistant","content":"Continuing the parent prompt."}`,
		`{"role":"tool","content":"read file"}`,
	})

	// Unstamped history with no human turn. Hide.
	writeFixture(t, home, "/tmp/proj", "agent-only", map[string]any{
		"info":            map[string]any{"id": "agent-only"},
		"generated_title": "Internal prompt",
		"updated_at":      "2026-09-07T15:00:00Z",
	}, []string{
		`{"role":"system","content":"you are a helper"}`,
		`{"role":"assistant","content":"Grok to grok: draft the patch."}`,
		`{"role":"agent","content":"acknowledged"}`,
	})

	// Synthetic user scaffolding only. Hide.
	writeFixture(t, home, "/tmp/proj", "synthetic-1", map[string]any{
		"info":       map[string]any{"id": "synthetic-1"},
		"title":      "Scaffold only",
		"updated_at": "2026-09-07T15:10:00Z",
	}, []string{
		`{"role":"user","content":"<user_info>OS: Linux</user_info>"}`,
		`{"role":"user","synthetic_reason":"context_inject","content":"injected"}`,
		`{"role":"assistant","content":"ok"}`,
	})

	// Fork of a real chat still has a human turn. Keep despite parent_session_id.
	writeFixture(t, home, "/tmp/proj", "fork-1", map[string]any{
		"info":              map[string]any{"id": "fork-1"},
		"generated_title":   "Forked login work",
		"session_kind":      "fork",
		"parent_session_id": "human-1",
		"updated_at":        "2026-09-07T16:00:00Z",
	}, []string{
		`{"role":"user","content":"please fix login"}`,
		`{"role":"assistant","content":"Continuing on the fork."}`,
	})

	// Empty unstamped session: cannot prove grok-only. Keep.
	writeFixture(t, home, "/tmp/proj", "empty-1", map[string]any{
		"info":       map[string]any{"id": "empty-1"},
		"title":      "New chat",
		"updated_at": "2026-09-07T11:00:00Z",
	}, nil)

	// ACP user_message_chunk in updates.jsonl counts as a human turn.
	acpDir := writeFixture(t, home, "/tmp/proj", "acp-user", map[string]any{
		"info":       map[string]any{"id": "acp-user"},
		"title":      "ACP user",
		"updated_at": "2026-09-07T16:30:00Z",
	}, []string{
		`{"role":"assistant","content":"working"}`,
	})
	if err := os.WriteFile(filepath.Join(acpDir, "updates.jsonl"), []byte(
		`{"params":{"update":{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"ship the fix"}}}}`+"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}

	list := List()
	got := map[string]bool{}
	for _, s := range list {
		got[s.ID] = s.GrokOnly
	}
	want := []string{"build:human-1", "build:mixed-1", "build:fork-1", "build:empty-1", "build:acp-user"}
	for _, id := range want {
		if _, ok := got[id]; !ok {
			t.Fatalf("expected %s in default list, got %+v", id, got)
		}
		if got[id] {
			t.Fatalf("%s should not be marked grok_only", id)
		}
	}
	for _, id := range []string{"build:sub-1", "build:subfork-1", "build:agent-only", "build:synthetic-1"} {
		if _, ok := got[id]; ok {
			t.Fatalf("grok-only %s should be hidden, got %+v", id, got)
		}
	}

	// Opt-in lists them, badged, without dropping real chats.
	shown := ListIncluding(true)
	shownIDs := map[string]bool{}
	for _, s := range shown {
		shownIDs[s.ID] = s.GrokOnly
	}
	if !shownIDs["build:sub-1"] || !shownIDs["build:agent-only"] || !shownIDs["build:synthetic-1"] {
		t.Fatalf("include should surface grok-only, got %+v", shownIDs)
	}
	if !shownIDs["build:sub-1"] || shownIDs["build:human-1"] {
		t.Fatalf("badge: sub should be grok_only and human should not, got %+v", shownIDs)
	}

	// Env opt-in matches the query flag.
	t.Setenv("GROK_BRIDGE_INCLUDE_GROK_ONLY", "1")
	viaEnv := List()
	envIDs := map[string]struct{}{}
	for _, s := range viaEnv {
		envIDs[s.ID] = struct{}{}
	}
	if _, ok := envIDs["build:sub-1"]; !ok {
		t.Fatalf("env include missing sub-1: %+v", envIDs)
	}
}
