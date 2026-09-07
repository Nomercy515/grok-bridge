package sessions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"grok-bridge/internal/auth"
)

// Store persists chat sessions under <root>/sessions/.
type Store struct {
	mu          sync.Mutex
	Root        string
	sessionsDir string
}

func DefaultDataDir() string {
	if env := os.Getenv("GROK_BRIDGE_DATA"); env != "" {
		return filepath.Clean(env)
	}
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	cwdData := filepath.Join(cwd, "data")
	home, _ := os.UserHomeDir()
	homeData := filepath.Join(home, ".grok-bridge")
	if st, err := os.Stat(cwdData); err == nil && st.IsDir() {
		return cwdData
	}
	if _, err := os.Stat(homeData); err == nil {
		return homeData
	}
	return cwdData
}

func NewStore(root string) (*Store, error) {
	if root == "" {
		root = DefaultDataDir()
	}
	sdir := filepath.Join(root, "sessions")
	if err := os.MkdirAll(sdir, 0o755); err != nil {
		return nil, err
	}
	return &Store{Root: root, sessionsDir: sdir}, nil
}

func (s *Store) path(sessionID string) (string, error) {
	safe := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return -1
	}, sessionID)
	if safe != sessionID || safe == "" {
		return "", errInvalidSession
	}
	return filepath.Join(s.sessionsDir, safe+".json"), nil
}

var errInvalidSession = &pathError{"invalid session id"}

type pathError struct{ s string }

func (e *pathError) Error() string { return e.s }

type Message struct {
	ID        string           `json:"id"`
	Role      string           `json:"role"`
	Content   string           `json:"content"`
	TS        float64          `json:"ts"`
	Tools     []map[string]any `json:"tools,omitempty"`
	Cancelled bool             `json:"cancelled,omitempty"`
}

type Session struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	CreatedAt float64   `json:"created_at"`
	UpdatedAt float64   `json:"updated_at"`
	Messages  []Message `json:"messages"`
	Source    string    `json:"source,omitempty"`   // "bridge" | "build"
	ReadOnly  bool      `json:"readonly,omitempty"` // true for on-disk Build sessions
}

type SessionSummary struct {
	ID           string  `json:"id"`
	Title        string  `json:"title"`
	CreatedAt    float64 `json:"created_at"`
	UpdatedAt    float64 `json:"updated_at"`
	MessageCount int     `json:"message_count"`
	Source       string  `json:"source,omitempty"` // "bridge" | "build"
}

func (s *Store) ListSessions() []SessionSummary {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.sessionsDir)
	if err != nil {
		return nil
	}
	type item struct {
		sum SessionSummary
		mt  time.Time
	}
	var items []item
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.sessionsDir, e.Name()))
		if err != nil {
			continue
		}
		var sess Session
		if json.Unmarshal(b, &sess) != nil || sess.ID == "" {
			continue
		}
		info, _ := e.Info()
		mt := time.Now()
		if info != nil {
			mt = info.ModTime()
		}
		title := sess.Title
		if title == "" {
			title = "Untitled"
		}
		items = append(items, item{
			sum: SessionSummary{
				ID: sess.ID, Title: title,
				CreatedAt: sess.CreatedAt, UpdatedAt: sess.UpdatedAt,
				MessageCount: len(sess.Messages),
			},
			mt: mt,
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].mt.After(items[j].mt) })
	out := make([]SessionSummary, len(items))
	for i, it := range items {
		out[i] = it.sum
	}
	return out
}

func (s *Store) Get(sessionID string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getLocked(sessionID)
}

func (s *Store) getLocked(sessionID string) (*Session, error) {
	p, err := s.path(sessionID)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var sess Session
	if err := json.Unmarshal(b, &sess); err != nil {
		return nil, err
	}
	return &sess, nil
}

func (s *Store) Create(title string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := float64(time.Now().UnixNano()) / 1e9
	if title == "" {
		title = "New chat"
	}
	sess := &Session{
		ID:        uuid.NewString(),
		Title:     title,
		CreatedAt: now,
		UpdatedAt: now,
		Messages:  []Message{},
	}
	if err := s.writeLocked(sess); err != nil {
		return nil, err
	}
	return sess, nil
}

func (s *Store) AppendMessage(sessionID, role, content string, extra map[string]any) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, err := s.getLocked(sessionID)
	if err != nil {
		return nil, err
	}
	if sess == nil {
		return nil, os.ErrNotExist
	}
	safeRole := role
	if !auth.AllowedMessageRoles[role] {
		safeRole = "assistant"
	}
	msg := Message{
		ID:      uuid.NewString(),
		Role:    safeRole,
		Content: content,
		TS:      float64(time.Now().UnixNano()) / 1e9,
	}
	if extra != nil {
		if tools, ok := extra["tools"].([]map[string]any); ok {
			msg.Tools = tools
		} else if toolsAny, ok := extra["tools"].([]any); ok {
			for _, t := range toolsAny {
				if m, ok := t.(map[string]any); ok {
					msg.Tools = append(msg.Tools, m)
				}
			}
		}
		if c, ok := extra["cancelled"].(bool); ok && c {
			msg.Cancelled = true
		}
	}
	sess.Messages = append(sess.Messages, msg)
	sess.UpdatedAt = float64(time.Now().UnixNano()) / 1e9
	if safeRole == "user" && (sess.Title == "" || sess.Title == "New chat") {
		t := strings.TrimSpace(content)
		if len(t) > 48 {
			t = t[:48]
		}
		if t == "" {
			t = "New chat"
		}
		sess.Title = t
	}
	if err := s.writeLocked(sess); err != nil {
		return nil, err
	}
	return sess, nil
}

func (s *Store) EnsureDemoSessions() ([]*Session, error) {
	if len(s.ListSessions()) > 0 {
		return nil, nil
	}
	samples := []struct {
		title string
		msgs  []struct {
			role, content string
			tools         []map[string]any
		}
	}{
		{
			title: "Welcome to Grok Bridge",
			msgs: []struct {
				role, content string
				tools         []map[string]any
			}{
				{"user", "What is Grok Bridge?", nil},
				{"assistant", "Grok Bridge is a LAN/Tailscale phone/browser cockpit: a hub UI on port 4020 with live chat over WebSocket and a pluggable agent adapter. Demo mode streams canned replies.", nil},
			},
		},
		{
			title: "Sample tool card",
			msgs: []struct {
				role, content string
				tools         []map[string]any
			}{
				{"user", "Show me a tool call", nil},
				{"assistant", "Sure — here's a demo tool card from a prior turn.", []map[string]any{
					{"id": "demo-sample-1", "name": "demo_lookup", "status": "done",
						"arguments": map[string]any{"query": "sample", "limit": 3},
						"result":    map[string]any{"items": []any{"alpha", "beta", "gamma"}, "source": "demo"}},
				}},
			},
		},
	}
	var created []*Session
	for _, sample := range samples {
		sess, err := s.Create(sample.title)
		if err != nil {
			return created, err
		}
		for _, m := range sample.msgs {
			extra := map[string]any{}
			if m.tools != nil {
				extra["tools"] = m.tools
			}
			if _, err := s.AppendMessage(sess.ID, m.role, m.content, extra); err != nil {
				return created, err
			}
		}
		full, _ := s.Get(sess.ID)
		created = append(created, full)
	}
	return created, nil
}

func (s *Store) writeLocked(sess *Session) error {
	p, err := s.path(sess.ID)
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(sess, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
