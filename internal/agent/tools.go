package agent

import "fmt"

// ToolID returns the correlation id from a tool payload (id or call_id).
func ToolID(tool map[string]any) string {
	if tool == nil {
		return ""
	}
	for _, k := range []string{"id", "call_id"} {
		if v, ok := tool[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// ClassifyToolEvent maps wire types onto call vs result.
// tool_card is an alias: result/error present → result, else → call.
func ClassifyToolEvent(eventType string, tool map[string]any) string {
	switch eventType {
	case "tool_call":
		return "tool_call"
	case "tool_result":
		return "tool_result"
	case "tool_card":
		if tool == nil {
			return "tool_call"
		}
		if _, has := tool["result"]; has {
			return "tool_result"
		}
		if st, _ := tool["status"].(string); st == "done" || st == "error" {
			return "tool_result"
		}
		if err, has := tool["error"]; has && err != nil && fmt.Sprint(err) != "" {
			return "tool_result"
		}
		return "tool_call"
	default:
		return ""
	}
}

func toolOpen(t map[string]any) bool {
	if _, has := t["result"]; has {
		return false
	}
	if st, _ := t["status"].(string); st == "done" || st == "error" {
		return false
	}
	return true
}

// findToolRow prefers id/call_id. Name fallback is only used when the
// incoming tool has no id, or for results whose id is unknown.
func findToolRow(acc []map[string]any, tool map[string]any, allowNameFallback bool) int {
	id := ToolID(tool)
	if id != "" {
		for i, t := range acc {
			if ToolID(t) == id {
				return i
			}
		}
		if !allowNameFallback {
			return -1
		}
	}
	name, _ := tool["name"].(string)
	if name == "" {
		return -1
	}
	for i, t := range acc {
		n, _ := t["name"].(string)
		if n == name && toolOpen(t) {
			return i
		}
	}
	return -1
}

func mergeTool(dst, src map[string]any) {
	for _, k := range []string{"id", "call_id", "name", "arguments", "result", "error"} {
		if v, ok := src[k]; ok {
			dst[k] = v
		}
	}
}

func resultStatus(tool map[string]any) string {
	if st, _ := tool["status"].(string); st == "error" {
		return "error"
	}
	if err, has := tool["error"]; has && err != nil && fmt.Sprint(err) != "" {
		return "error"
	}
	return "done"
}

// ApplyToolEvent merges a tool_call / tool_result / tool_card into the accumulator.
// Matching prefers tool.id / call_id; name + first-open is the fallback.
// Distinct ids never collapse into one row on tool_call.
// Each entry carries status: running | done | error.
func ApplyToolEvent(acc []map[string]any, eventType string, tool map[string]any) []map[string]any {
	if tool == nil {
		return acc
	}
	kind := ClassifyToolEvent(eventType, tool)
	if kind == "" {
		return acc
	}
	switch kind {
	case "tool_call":
		// New id → always a new row (do not collapse same-name calls).
		idx := findToolRow(acc, tool, false)
		if idx >= 0 {
			mergeTool(acc[idx], tool)
			if st, ok := tool["status"].(string); ok && st != "" {
				acc[idx]["status"] = st
			} else if toolOpen(acc[idx]) {
				acc[idx]["status"] = "running"
			}
			return acc
		}
		row := map[string]any{"status": "running"}
		mergeTool(row, tool)
		if st, ok := tool["status"].(string); ok && st != "" {
			row["status"] = st
		}
		return append(acc, row)
	case "tool_result":
		// Prefer id; if unknown id (or no id), fall back to name + first open.
		idx := findToolRow(acc, tool, true)
		status := resultStatus(tool)
		if idx >= 0 {
			mergeTool(acc[idx], tool)
			acc[idx]["status"] = status
			return acc
		}
		row := map[string]any{"status": status}
		mergeTool(row, tool)
		return append(acc, row)
	}
	return acc
}
