package buildsessions

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Hide Grok-only sessions from the default Build list.
//
// Grok Build's own listing (Summary.is_hidden) omits a session when
// hidden is explicitly true, or when session_kind starts with "subagent"
// (subagent, subagent_fork) unless hidden is explicitly false. Those child
// sessions are agent-to-agent prompts: a role=user line there is the parent
// Grok's spawn prompt, not a person.
//
// Otherwise a session is grok-only only when on-disk history exists and
// has no real human turn (assistant / system / agent / tool only, or
// synthetic user scaffolding). A later Grok reply does not hide a chat
// that already has a human turn. Empty or unreadable history with no
// subagent/hidden stamp is kept — we cannot prove it is grok-only.
// Forks, worktree sessions, and headless `grok -p` prompts stay listed
// when they contain a human turn (parent_session_id alone is not enough).

func includeGrokOnlyFromEnv() bool {
	v := strings.TrimSpace(os.Getenv("GROK_BRIDGE_INCLUDE_GROK_ONLY"))
	switch strings.ToLower(v) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// QueryIncludesGrokOnly reports whether ?include= lists grok-only sessions.
func QueryIncludesGrokOnly(raw string) bool {
	for _, part := range strings.Split(raw, ",") {
		switch strings.ToLower(strings.TrimSpace(part)) {
		case "grok-only", "grok_only":
			return true
		}
	}
	return false
}

func isGrokOnly(summaryPath string, raw rawSummary) bool {
	if raw.Hidden != nil && *raw.Hidden {
		return true
	}
	kind := strings.ToLower(strings.TrimSpace(raw.SessionKind))
	explicitShow := raw.Hidden != nil && !*raw.Hidden
	if !explicitShow && (strings.HasPrefix(kind, "subagent") || hasSubagentStamp(raw)) {
		return true
	}
	dir := filepath.Dir(summaryPath)
	hasHuman, sawActivity := sessionHasHumanTurn(dir)
	if hasHuman {
		return false
	}
	// History with only grok/tool/synthetic turns — not a user chat.
	return sawActivity
}

func hasSubagentStamp(raw rawSummary) bool {
	if strings.TrimSpace(raw.SubagentType) != "" ||
		strings.TrimSpace(raw.SubagentPersona) != "" ||
		strings.TrimSpace(raw.SubagentRole) != "" {
		return true
	}
	if raw.SubagentDepth != nil && *raw.SubagentDepth > 0 {
		return true
	}
	return false
}

// sessionHasHumanTurn scans chat_history.jsonl and updates.jsonl.
// hasHuman means a non-synthetic user turn (or ACP user_message_chunk).
// sawActivity means the session has conversation/tool bytes at all.
func sessionHasHumanTurn(dir string) (hasHuman, sawActivity bool) {
	for _, name := range []string{"chat_history.jsonl", "updates.jsonl"} {
		h, act := jsonlHasHumanTurn(filepath.Join(dir, name))
		if h {
			return true, true
		}
		if act {
			sawActivity = true
		}
	}
	return false, sawActivity
}

func jsonlHasHumanTurn(path string) (hasHuman, sawActivity bool) {
	f, err := os.Open(path)
	if err != nil {
		return false, false
	}
	defer f.Close()
	sc := bufio.NewReaderSize(f, 64*1024)
	for {
		line, err := readJSONLLine(sc)
		if line != "" {
			var m map[string]any
			if json.Unmarshal([]byte(line), &m) == nil {
				if historyLineIsHuman(m) {
					return true, true
				}
				if historyLineIsActivity(m) {
					sawActivity = true
				}
			}
		}
		if err != nil {
			break
		}
	}
	return false, sawActivity
}

// readJSONLLine reads one logical line. Oversize lines are skipped so a
// huge tool dump cannot hide a later user turn or abort the scan.
func readJSONLLine(r *bufio.Reader) (string, error) {
	const max = 2 << 20
	var b []byte
	for {
		chunk, err := r.ReadSlice('\n')
		tooLong := len(b)+len(chunk) > max
		if tooLong {
			if err == bufio.ErrBufferFull {
				if e2 := discardToNewline(r); e2 != nil {
					return "", e2
				}
			}
			// Complete line exceeded max, or we drained it. Skip it.
			if err == nil || err == bufio.ErrBufferFull {
				return "", nil
			}
			return "", err
		}
		b = append(b, chunk...)
		if err == bufio.ErrBufferFull {
			continue
		}
		return strings.TrimSpace(string(b)), err
	}
}

func discardToNewline(r *bufio.Reader) error {
	for {
		rest, err := r.ReadSlice('\n')
		if err == nil || (len(rest) > 0 && rest[len(rest)-1] == '\n') {
			return nil
		}
		if err != bufio.ErrBufferFull {
			return err
		}
	}
}

func historyLineIsHuman(m map[string]any) bool {
	if humanFromRoleMap(m) {
		return true
	}
	if msg, ok := m["message"].(map[string]any); ok && humanFromRoleMap(msg) {
		return true
	}
	if humanACP(m) {
		return true
	}
	if upd, ok := m["update"].(map[string]any); ok && humanACP(upd) {
		return true
	}
	if params, ok := m["params"].(map[string]any); ok {
		if humanACP(params) {
			return true
		}
		if upd, ok := params["update"].(map[string]any); ok && humanACP(upd) {
			return true
		}
	}
	return false
}

func historyLineIsActivity(m map[string]any) bool {
	if historyLineIsHuman(m) {
		return true
	}
	role := lineRole(m)
	switch role {
	case "assistant", "system", "agent", "grok", "tool", "tool_call", "tool_result", "function":
		return true
	}
	if msg, ok := m["message"].(map[string]any); ok {
		switch lineRole(msg) {
		case "assistant", "system", "agent", "grok", "tool", "tool_call", "tool_result", "function", "user":
			return true
		}
	}
	if sessionUpdateName(m) != "" {
		return true
	}
	if upd, ok := m["update"].(map[string]any); ok && sessionUpdateName(upd) != "" {
		return true
	}
	if params, ok := m["params"].(map[string]any); ok {
		if sessionUpdateName(params) != "" {
			return true
		}
		if upd, ok := params["update"].(map[string]any); ok && sessionUpdateName(upd) != "" {
			return true
		}
	}
	// Any non-empty role/type counts as activity (unknown grok-side events).
	return role != ""
}

func humanFromRoleMap(m map[string]any) bool {
	if lineRole(m) != "user" {
		return false
	}
	content := extractContent(m["content"])
	if content == "" {
		content = extractContent(m["text"])
	}
	return isRealUserContent(m, content)
}

func humanACP(m map[string]any) bool {
	su := sessionUpdateName(m)
	if su != "user_message_chunk" && su != "user_message" {
		return false
	}
	content := extractContent(m["content"])
	if content == "" {
		content = extractContent(m["text"])
	}
	return isRealUserContent(m, content)
}

func isRealUserContent(m map[string]any, content string) bool {
	if strings.TrimSpace(content) == "" {
		return false
	}
	if unwrapped, ok := unwrapUserQueries(content); ok {
		return strings.TrimSpace(unwrapped) != ""
	}
	if isSyntheticUserNoise(m, content) {
		return false
	}
	return true
}

func lineRole(m map[string]any) string {
	role := strings.ToLower(strings.TrimSpace(strField(m, "role")))
	if role == "" {
		role = strings.ToLower(strings.TrimSpace(strField(m, "type")))
	}
	return role
}

func sessionUpdateName(m map[string]any) string {
	su := strings.TrimSpace(strField(m, "sessionUpdate"))
	if su == "" {
		su = strings.TrimSpace(strField(m, "session_update"))
	}
	return strings.ToLower(su)
}
