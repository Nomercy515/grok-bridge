package grokacp

import (
	"encoding/json"
	"fmt"
	"strings"
)

// BridgeEvent is a hub fan-out event (assistant_delta / tool_call / …).
type BridgeEvent map[string]any

// MapUpdate converts an ACP session/update payload into zero or more Bridge events.
// bridgeSessionID is the hub id (typically "build:<uuid>").
func MapUpdate(bridgeSessionID string, update map[string]any) []BridgeEvent {
	if update == nil {
		return nil
	}
	kind, _ := update["sessionUpdate"].(string)
	if kind == "" {
		kind, _ = update["session_update"].(string)
	}
	switch kind {
	case "agent_message_chunk":
		text := contentText(update["content"])
		if text == "" {
			return nil
		}
		return []BridgeEvent{{
			"type": "assistant_delta", "session_id": bridgeSessionID, "delta": text,
		}}
	case "tool_call":
		tool := mapToolCall(update)
		if tool == nil {
			return nil
		}
		return []BridgeEvent{{
			"type": "tool_call", "session_id": bridgeSessionID, "tool": tool,
		}}
	case "tool_call_update":
		tool := mapToolUpdate(update)
		if tool == nil {
			return nil
		}
		status, _ := tool["status"].(string)
		et := "tool_call"
		if status == "done" || status == "error" || status == "completed" || status == "failed" {
			et = "tool_result"
			if status == "completed" {
				tool["status"] = "done"
			}
			if status == "failed" {
				tool["status"] = "error"
			}
		}
		return []BridgeEvent{{
			"type": et, "session_id": bridgeSessionID, "tool": tool,
		}}
	default:
		// user_message_chunk, agent_thought_chunk, plan, etc. — ignore for Bridge UI
		return nil
	}
}

func contentText(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case map[string]any:
		if text, ok := t["text"].(string); ok {
			return text
		}
		if c, ok := t["content"].(string); ok {
			return c
		}
	}
	return ""
}

func mapToolCall(update map[string]any) map[string]any {
	id := strAny(update["toolCallId"])
	if id == "" {
		id = strAny(update["tool_call_id"])
	}
	name := strAny(update["title"])
	if name == "" {
		name = strAny(update["name"])
	}
	if meta, ok := update["_meta"].(map[string]any); ok {
		if xt, ok := meta["x.ai/tool"].(map[string]any); ok {
			if n := strAny(xt["name"]); n != "" {
				name = n
			} else if n := strAny(xt["label"]); n != "" && name == "" {
				name = n
			}
		}
	}
	if name == "" {
		name = "tool"
	}
	tool := map[string]any{
		"id":     id,
		"name":   name,
		"status": "running",
	}
	if raw, ok := update["rawInput"]; ok {
		tool["arguments"] = raw
	} else if raw, ok := update["raw_input"]; ok {
		tool["arguments"] = raw
	}
	return tool
}

func mapToolUpdate(update map[string]any) map[string]any {
	id := strAny(update["toolCallId"])
	if id == "" {
		id = strAny(update["tool_call_id"])
	}
	status := strAny(update["status"])
	if status == "" {
		status = "running"
	}
	tool := map[string]any{
		"id":     id,
		"status": status,
	}
	if name := strAny(update["title"]); name != "" {
		tool["name"] = name
	}
	if name := strAny(update["name"]); name != "" {
		tool["name"] = name
	}
	if content, ok := update["content"]; ok {
		tool["result"] = summarizeToolContent(content)
	}
	if raw, ok := update["rawOutput"]; ok {
		if tool["result"] == nil {
			tool["result"] = raw
		}
	}
	return tool
}

func summarizeToolContent(v any) any {
	switch t := v.(type) {
	case string:
		return t
	case []any:
		var parts []string
		for _, item := range t {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			typ, _ := m["type"].(string)
			switch typ {
			case "content":
				if inner, ok := m["content"].(map[string]any); ok {
					if text := contentText(inner); text != "" {
						parts = append(parts, text)
					}
				}
			case "diff":
				path, _ := m["path"].(string)
				parts = append(parts, fmt.Sprintf("diff %s", path))
			case "text":
				if text := contentText(m); text != "" {
					parts = append(parts, text)
				}
			}
		}
		if len(parts) == 1 {
			return parts[0]
		}
		if len(parts) > 1 {
			return strings.Join(parts, "\n")
		}
		return t
	default:
		return v
	}
}

func strAny(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case fmt.Stringer:
		return t.String()
	case json.Number:
		return t.String()
	default:
		return ""
	}
}

// StopReasonCancelled reports whether an ACP stopReason means cancel.
func StopReasonCancelled(reason string) bool {
	r := strings.ToLower(strings.TrimSpace(reason))
	return r == "cancelled" || r == "canceled"
}
