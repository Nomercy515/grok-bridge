package agent

import (
	"testing"
)

func TestApplyToolEventMatchByID(t *testing.T) {
	var acc []map[string]any
	acc = ApplyToolEvent(acc, "tool_call", map[string]any{
		"id": "tc1", "name": "search", "arguments": map[string]any{"q": "a"},
	})
	acc = ApplyToolEvent(acc, "tool_call", map[string]any{
		"id": "tc2", "name": "search", "arguments": map[string]any{"q": "b"},
	})
	if len(acc) != 2 {
		t.Fatalf("want 2 open calls, got %d", len(acc))
	}
	acc = ApplyToolEvent(acc, "tool_result", map[string]any{
		"id": "tc2", "name": "search", "result": map[string]any{"ok": true},
	})
	if len(acc) != 2 {
		t.Fatalf("want still 2, got %d", len(acc))
	}
	if acc[1]["status"] != "done" {
		t.Fatalf("tc2 status=%v", acc[1]["status"])
	}
	if _, has := acc[0]["result"]; has {
		t.Fatal("tc1 should still be open")
	}
	if acc[0]["status"] != "running" {
		t.Fatalf("tc1 status=%v", acc[0]["status"])
	}
}

func TestApplyToolEventFallbackName(t *testing.T) {
	var acc []map[string]any
	acc = ApplyToolEvent(acc, "tool_call", map[string]any{
		"name": "echo", "arguments": map[string]any{"x": 1},
	})
	acc = ApplyToolEvent(acc, "tool_result", map[string]any{
		"name": "echo", "result": "hi",
	})
	if len(acc) != 1 || acc[0]["status"] != "done" || acc[0]["result"] != "hi" {
		t.Fatalf("%v", acc)
	}
}

func TestApplyToolEventToolCardAlias(t *testing.T) {
	var acc []map[string]any
	acc = ApplyToolEvent(acc, "tool_card", map[string]any{
		"call_id": "c1", "name": "lookup", "arguments": map[string]any{"k": "v"},
	})
	if ClassifyToolEvent("tool_card", acc[0]) != "tool_call" {
		t.Fatal("expected call classify")
	}
	acc = ApplyToolEvent(acc, "tool_card", map[string]any{
		"call_id": "c1", "name": "lookup", "result": 42, "status": "done",
	})
	if len(acc) != 1 || acc[0]["status"] != "done" || acc[0]["result"] != 42 {
		t.Fatalf("%v", acc)
	}
	if ToolID(acc[0]) != "c1" {
		t.Fatalf("id=%s", ToolID(acc[0]))
	}
}

func TestApplyToolEventErrorStatus(t *testing.T) {
	var acc []map[string]any
	acc = ApplyToolEvent(acc, "tool_call", map[string]any{"id": "e1", "name": "x"})
	acc = ApplyToolEvent(acc, "tool_result", map[string]any{
		"id": "e1", "name": "x", "error": "boom", "status": "error",
	})
	if acc[0]["status"] != "error" {
		t.Fatalf("%v", acc[0])
	}
}
