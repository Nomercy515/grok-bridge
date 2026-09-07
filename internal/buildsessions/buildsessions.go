package buildsessions

import (
	"bufio"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"grok-bridge/internal/sessions"
)

// List walks $GROK_HOME/sessions for summary.json files and returns
// SessionSummary values with Source "build" and id prefixed "build:".
// Missing or unreadable Grok home yields an empty slice (hub-native still works).
func List() []sessions.SessionSummary {
	root := sessionsRoot()
	if root == "" {
		return nil
	}
	st, err := os.Stat(root)
	if err != nil || !st.IsDir() {
		return nil
	}
	var out []sessions.SessionSummary
	seen := map[string]struct{}{}

	// Prefer nested layout: sessions/<escaped-cwd>/<session-id>/summary.json
	// Also tolerate flat: sessions/<session-id>/summary.json
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d == nil || d.IsDir() {
			return nil
		}
		if d.Name() != "summary.json" {
			return nil
		}
		sum, ok := readSummaryFile(path)
		if !ok || sum.ID == "" {
			return nil
		}
		apiID := WithPrefix(sum.ID)
		if _, dup := seen[apiID]; dup {
			return nil
		}
		seen[apiID] = struct{}{}
		sum.ID = apiID
		sum.Source = "build"
		out = append(out, sum)
		return nil
	})

	sort.Slice(out, func(i, j int) bool {
		return out[i].UpdatedAt > out[j].UpdatedAt
	})
	return out
}

// Get loads a Build session by API id ("build:<id>") or raw id.
// Returns a sessions.Session shaped for the hub UI (send/resume via ACP).
func Get(id string) (*sessions.Session, error) {
	raw, ok := StripPrefix(id)
	if !ok {
		raw = id
	}
	if raw == "" || strings.Contains(raw, "..") || strings.ContainsAny(raw, `/\`) {
		return nil, os.ErrNotExist
	}
	dir, err := findSessionDir(raw)
	if err != nil || dir == "" {
		return nil, os.ErrNotExist
	}
	sum, ok := readSummaryFile(filepath.Join(dir, "summary.json"))
	if !ok {
		return nil, os.ErrNotExist
	}
	msgs := readChatHistory(filepath.Join(dir, "chat_history.jsonl"))
	title := sum.Title
	if title == "" {
		title = "Untitled"
	}
	created := sum.CreatedAt
	updated := sum.UpdatedAt
	if created == 0 && len(msgs) > 0 {
		created = msgs[0].TS
	}
	if updated == 0 && len(msgs) > 0 {
		updated = msgs[len(msgs)-1].TS
	}
	sess := &sessions.Session{
		ID:        WithPrefix(raw),
		Title:     title,
		CreatedAt: created,
		UpdatedAt: updated,
		Messages:  msgs,
		Source:    "build",
		ReadOnly:  false, // live send/receive via Grok ACP
	}
	applySignals(sess, filepath.Join(dir, "signals.json"))
	return sess, nil
}

// ResolveMeta returns the raw session UUID and cwd for ACP session/load.
func ResolveMeta(id string) (rawID, cwd string, err error) {
	raw, ok := StripPrefix(id)
	if !ok {
		raw = id
	}
	if raw == "" || strings.Contains(raw, "..") || strings.ContainsAny(raw, `/\`) {
		return "", "", os.ErrNotExist
	}
	dir, err := findSessionDir(raw)
	if err != nil || dir == "" {
		return "", "", os.ErrNotExist
	}
	b, err := os.ReadFile(filepath.Join(dir, "summary.json"))
	if err != nil {
		return "", "", err
	}
	var rawSum rawSummary
	if json.Unmarshal(b, &rawSum) != nil {
		return "", "", os.ErrNotExist
	}
	cwd = strings.TrimSpace(rawSum.Info.Cwd)
	if cwd == "" {
		// Nested layout: sessions/<escaped-cwd>/<id>/ — best-effort unescape
		parent := filepath.Base(filepath.Dir(dir))
		if parent != "" && parent != "sessions" && parent != raw {
			if u, e := pathUnescape(parent); e == nil && u != "" {
				cwd = u
			}
		}
	}
	if cwd == "" {
		cwd = "."
	}
	return raw, cwd, nil
}

func findSessionDir(rawID string) (string, error) {
	root := sessionsRoot()
	if root == "" {
		return "", os.ErrNotExist
	}
	// Fast path: sessions/<anything>/<rawID>/
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		// flat: sessions/<rawID>/
		if e.Name() == rawID {
			cand := filepath.Join(root, e.Name())
			if fileExists(filepath.Join(cand, "summary.json")) {
				return cand, nil
			}
		}
		// nested: sessions/<cwd-escape>/<rawID>/
		cand := filepath.Join(root, e.Name(), rawID)
		if fileExists(filepath.Join(cand, "summary.json")) {
			return cand, nil
		}
	}
	// Slow fallback walk (tolerates unexpected depth)
	var found string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d == nil || !d.IsDir() {
			return nil
		}
		if d.Name() != rawID {
			return nil
		}
		if fileExists(filepath.Join(path, "summary.json")) {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	if found == "" {
		return "", os.ErrNotExist
	}
	return found, nil
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func pathUnescape(s string) (string, error) {
	return url.PathUnescape(s)
}


// signals.json (beside summary.json) carries live context-window usage from Grok Build.
type rawSignals struct {
	ContextTokensUsed   *int `json:"contextTokensUsed"`
	ContextWindowTokens *int `json:"contextWindowTokens"`
	ContextWindowUsage  *int `json:"contextWindowUsage"`
}

func applySignals(sess *sessions.Session, path string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var sig rawSignals
	if json.Unmarshal(b, &sig) != nil {
		return
	}
	if sig.ContextTokensUsed != nil {
		sess.ContextTokensUsed = sig.ContextTokensUsed
	}
	if sig.ContextWindowTokens != nil {
		sess.ContextWindowTokens = sig.ContextWindowTokens
	}
	if sig.ContextWindowUsage != nil {
		sess.ContextWindowUsage = sig.ContextWindowUsage
	}
}

// Flexible summary.json decode.
type rawSummary struct {
	Info struct {
		ID  string `json:"id"`
		Cwd string `json:"cwd"`
	} `json:"info"`
	ID              string          `json:"id"`
	SessionSummary  string          `json:"session_summary"`
	GeneratedTitle  string          `json:"generated_title"`
	Title           string          `json:"title"`
	CreatedAt       json.RawMessage `json:"created_at"`
	UpdatedAt       json.RawMessage `json:"updated_at"`
	LastActiveAt    json.RawMessage `json:"last_active_at"`
	NumMessages     int             `json:"num_messages"`
	NumChatMessages int             `json:"num_chat_messages"`
	MessageCount    int             `json:"message_count"`
}

func readSummaryFile(path string) (sessions.SessionSummary, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return sessions.SessionSummary{}, false
	}
	var raw rawSummary
	if json.Unmarshal(b, &raw) != nil {
		return sessions.SessionSummary{}, false
	}
	id := raw.Info.ID
	if id == "" {
		id = raw.ID
	}
	if id == "" {
		// Derive from parent directory name
		id = filepath.Base(filepath.Dir(path))
	}
	title := firstNonEmpty(raw.GeneratedTitle, raw.SessionSummary, raw.Title, "Untitled")
	created := parseTimeFlex(raw.CreatedAt)
	updated := parseTimeFlex(raw.UpdatedAt)
	if updated == 0 {
		updated = parseTimeFlex(raw.LastActiveAt)
	}
	if updated == 0 {
		if info, err := os.Stat(path); err == nil {
			updated = float64(info.ModTime().UnixNano()) / 1e9
		}
	}
	count := raw.NumChatMessages
	if count == 0 {
		count = raw.NumMessages
	}
	if count == 0 {
		count = raw.MessageCount
	}
	return sessions.SessionSummary{
		ID:           id,
		Title:        title,
		CreatedAt:    created,
		UpdatedAt:    updated,
		MessageCount: count,
		Source:       "build",
	}, true
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func parseTimeFlex(raw json.RawMessage) float64 {
	if len(raw) == 0 || string(raw) == "null" {
		return 0
	}
	// number (unix seconds or ms)
	var f float64
	if json.Unmarshal(raw, &f) == nil {
		if f > 1e12 {
			// ms
			return f / 1000
		}
		return f
	}
	var s string
	if json.Unmarshal(raw, &s) != nil || s == "" {
		return 0
	}
	if n, err := strconv.ParseFloat(s, 64); err == nil {
		if n > 1e12 {
			return n / 1000
		}
		return n
	}
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return float64(t.UnixNano()) / 1e9
		}
	}
	return 0
}

func readChatHistory(path string) []sessions.Message {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []sessions.Message
	sc := bufio.NewScanner(f)
	// Allow long lines but skip absurd system dumps (>2MiB line)
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 2<<20)
	idx := 0
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		msg, ok := parseHistoryLine(line, idx)
		if !ok {
			continue
		}
		out = append(out, msg)
		idx++
	}
	return out
}

func parseHistoryLine(line string, idx int) (sessions.Message, bool) {
	var m map[string]any
	if json.Unmarshal([]byte(line), &m) != nil {
		return sessions.Message{}, false
	}
	role := strings.ToLower(strField(m, "role"))
	if role == "" {
		// Some lines use type=user/assistant
		role = strings.ToLower(strField(m, "type"))
	}
	switch role {
	case "user", "assistant":
		// ok
	case "system":
		return sessions.Message{}, false
	default:
		// Skip tool/event noise unless it has explicit chat role
		return sessions.Message{}, false
	}
	content := extractContent(m["content"])
	if content == "" {
		content = extractContent(m["text"])
	}
	if content == "" {
		return sessions.Message{}, false
	}
	// Skip huge system-like dumps that slipped through as user/assistant
	if len(content) > 200_000 {
		return sessions.Message{}, false
	}
	if role == "user" {
		if unwrapped, ok := unwrapUserQueries(content); ok {
			content = unwrapped
		} else if isSyntheticUserNoise(m, content) {
			// Reminder / user_info-only lines are not "You" bubbles.
			return sessions.Message{}, false
		}
		if strings.TrimSpace(content) == "" {
			return sessions.Message{}, false
		}
	}
	id := strField(m, "id")
	if id == "" {
		id = strField(m, "message_id")
	}
	if id == "" {
		id = "build-msg-" + strconv.Itoa(idx)
	}
	ts := parseAnyTime(m["ts"])
	if ts == 0 {
		ts = parseAnyTime(m["timestamp"])
	}
	if ts == 0 {
		ts = parseAnyTime(m["created_at"])
	}
	return sessions.Message{
		ID:      id,
		Role:    role,
		Content: content,
		TS:      ts,
	}, true
}

var userQueryRe = regexp.MustCompile(`(?is)<user_query>(.*?)</user_query>`)

// RE2 has no backrefs — strip each known synthetic wrapper separately.
var syntheticBlockRes = []*regexp.Regexp{
	regexp.MustCompile(`(?is)<system-reminder(?:\s[^>]*)?>[\s\S]*?</system-reminder>`),
	regexp.MustCompile(`(?is)<user_info(?:\s[^>]*)?>[\s\S]*?</user_info>`),
}

// unwrapUserQueries extracts inner text from complete <user_query>…</user_query>
// blocks (case-insensitive). When present, joined inners become Message.Content
// so the UI shows a normal You bubble instead of tagged wrappers.
func unwrapUserQueries(content string) (string, bool) {
	matches := userQueryRe.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return content, false
	}
	parts := make([]string, 0, len(matches))
	for _, m := range matches {
		parts = append(parts, m[1])
	}
	return strings.Join(parts, "\n"), true
}

func stripSyntheticBlocks(s string) (stripped string, had bool) {
	stripped = s
	for _, re := range syntheticBlockRes {
		if re.MatchString(stripped) {
			had = true
			stripped = re.ReplaceAllString(stripped, "")
		}
	}
	return strings.TrimSpace(stripped), had
}

// isSyntheticUserNoise reports user-role lines that are only Bridge/Grok
// scaffolding (system-reminder, user_info, synthetic_reason) with no user_query.
func isSyntheticUserNoise(m map[string]any, content string) bool {
	if userQueryRe.MatchString(content) {
		return false
	}
	if reason := strField(m, "synthetic_reason"); reason != "" {
		return true
	}
	s := strings.TrimSpace(content)
	if s == "" {
		return true
	}
	stripped, had := stripSyntheticBlocks(s)
	return had && stripped == ""
}

func strField(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	default:
		return ""
	}
}

func extractContent(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case []any:
		var parts []string
		for _, item := range t {
			switch b := item.(type) {
			case string:
				if b != "" {
					parts = append(parts, b)
				}
			case map[string]any:
				typ, _ := b["type"].(string)
				if typ != "" && typ != "text" && typ != "input_text" && typ != "output_text" {
					continue
				}
				if text, ok := b["text"].(string); ok && text != "" {
					parts = append(parts, text)
				} else if text, ok := b["content"].(string); ok && text != "" {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "\n")
	case map[string]any:
		if text, ok := t["text"].(string); ok {
			return text
		}
		if text, ok := t["content"].(string); ok {
			return text
		}
	}
	return ""
}

func parseAnyTime(v any) float64 {
	switch t := v.(type) {
	case nil:
		return 0
	case float64:
		if t > 1e12 {
			return t / 1000
		}
		return t
	case json.Number:
		f, _ := t.Float64()
		if f > 1e12 {
			return f / 1000
		}
		return f
	case string:
		b, _ := json.Marshal(t)
		return parseTimeFlex(b)
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return 0
		}
		return parseTimeFlex(b)
	}
}
