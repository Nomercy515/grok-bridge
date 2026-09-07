package grokacp

import (
	"testing"
)

func TestMapUpdateAgentMessageChunk(t *testing.T) {
	evs := MapUpdate("build:abc", map[string]any{
		"sessionUpdate": "agent_message_chunk",
		"content":       map[string]any{"type": "text", "text": "Hello"},
	})
	if len(evs) != 1 {
		t.Fatalf("len=%d", len(evs))
	}
	if evs[0]["type"] != "assistant_delta" || evs[0]["delta"] != "Hello" {
		t.Fatalf("%v", evs[0])
	}
	if evs[0]["session_id"] != "build:abc" {
		t.Fatalf("sid=%v", evs[0]["session_id"])
	}
}

func TestMapUpdateToolCallAndResult(t *testing.T) {
	call := MapUpdate("build:x", map[string]any{
		"sessionUpdate": "tool_call",
		"toolCallId":    "call-1",
		"title":         "Read notes",
		"rawInput":      map[string]any{"path": "notes.txt"},
		"_meta": map[string]any{
			"x.ai/tool": map[string]any{"name": "read_file", "kind": "read"},
		},
	})
	if len(call) != 1 || call[0]["type"] != "tool_call" {
		t.Fatalf("%v", call)
	}
	tool, _ := call[0]["tool"].(map[string]any)
	if tool["id"] != "call-1" || tool["name"] != "read_file" || tool["status"] != "running" {
		t.Fatalf("%v", tool)
	}

	res := MapUpdate("build:x", map[string]any{
		"sessionUpdate": "tool_call_update",
		"toolCallId":    "call-1",
		"status":        "completed",
		"content": []any{
			map[string]any{
				"type":    "content",
				"content": map[string]any{"type": "text", "text": "line one"},
			},
		},
	})
	if len(res) != 1 || res[0]["type"] != "tool_result" {
		t.Fatalf("%v", res)
	}
	tool2, _ := res[0]["tool"].(map[string]any)
	if tool2["status"] != "done" || tool2["result"] != "line one" {
		t.Fatalf("%v", tool2)
	}
}

func TestMapUpdateIgnoresThought(t *testing.T) {
	evs := MapUpdate("build:x", map[string]any{
		"sessionUpdate": "agent_thought_chunk",
		"content":       map[string]any{"type": "text", "text": "hmm"},
	})
	if len(evs) != 0 {
		t.Fatalf("%v", evs)
	}
}

func TestNormalizeWSURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", DefaultWSURL},
		{"ws://127.0.0.1:2419", "ws://127.0.0.1:2419/ws"},
		{"ws://127.0.0.1:2419/", "ws://127.0.0.1:2419/ws"},
		{"ws://127.0.0.1:2419/ws", "ws://127.0.0.1:2419/ws"},
		{"http://127.0.0.1:2419", "ws://127.0.0.1:2419/ws"},
		{"127.0.0.1:2419", "ws://127.0.0.1:2419/ws"},
	}
	for _, c := range cases {
		got := NormalizeWSURL(c.in)
		if got != c.want {
			t.Fatalf("NormalizeWSURL(%q)=%q want %q", c.in, got, c.want)
		}
	}
}

func TestStopReasonCancelled(t *testing.T) {
	if !StopReasonCancelled("cancelled") || !StopReasonCancelled("Canceled") {
		t.Fatal("expected cancelled")
	}
	if StopReasonCancelled("end_turn") {
		t.Fatal("end_turn not cancelled")
	}
}
