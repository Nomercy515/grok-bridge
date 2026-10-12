package hub

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"grok-bridge/internal/buildsessions"
)

// Turn state + per-session message queue.
//
// The hub is the single source of truth for "is a reply running in this
// session?". Clients must not infer it from the stream of WS events alone:
// a dropped socket, a sleeping phone or a background tab can miss
// assistant_done, which used to leave the UI stuck on Stop. Clients ask
// GET /api/sessions/{id}/status (and receive turn_status over WS) instead.
//
// Messages submitted while a turn is running are appended to a FIFO queue
// that is persisted under <data>/queues/ so it survives page refreshes and
// hub restarts. When a turn completes normally the hub sends the next queued
// message automatically. When a turn is stopped (or fails) the queue is
// paused instead, so Stop never fires the next prompt behind the user's back;
// POST /api/sessions/{id}/queue/send resumes it.

const (
	TurnIdle    = "idle"
	TurnRunning = "running"

	// Bounds keep a runaway client from filling the disk.
	maxQueueItems   = 50
	maxQueueContent = 64 * 1024
)

var (
	errQueueFull     = errors.New("queue full")
	errQueueNotFound = errors.New("queued message not found")
	errQueueEmpty    = errors.New("content required")
	errQueueTooLarge = errors.New("content too large")
)

// QueuedMessage is one user message waiting for the current turn to end.
type QueuedMessage struct {
	ID        string  `json:"id"`
	Content   string  `json:"content"`
	CreatedAt float64 `json:"created_at"`
	UpdatedAt float64 `json:"updated_at,omitempty"`
}

// TurnStatus is the wire shape of GET /api/sessions/{id}/status and of the
// "status" field on turn_status / session.snapshot WS events.
type TurnStatus struct {
	SessionID   string          `json:"session_id"`
	State       string          `json:"state"`
	TurnID      string          `json:"turn_id,omitempty"`
	StartedAt   float64         `json:"started_at,omitempty"`
	UpdatedAt   float64         `json:"updated_at,omitempty"`
	LastTurnID  string          `json:"last_turn_id,omitempty"`
	LastResult  string          `json:"last_result,omitempty"` // done | cancelled | error
	LastEndedAt float64         `json:"last_ended_at,omitempty"`
	Queue       []QueuedMessage `json:"queue"`
	QueuePaused bool            `json:"queue_paused"`
	ServerTime  float64         `json:"server_time"`
}

type turnEntry struct {
	running     bool
	turnID      string
	startedAt   float64
	updatedAt   float64
	lastTurnID  string
	lastResult  string
	lastEndedAt float64
	queue       []QueuedMessage
	paused      bool
	cancelReq   bool // Stop was requested for the running turn
	loaded      bool // queue read from disk
}

type queueFile struct {
	SessionID string          `json:"session_id"`
	Paused    bool            `json:"paused"`
	Items     []QueuedMessage `json:"items"`
}

type turnTracker struct {
	mu      sync.Mutex
	dir     string // <data>/queues ("" disables persistence)
	entries map[string]*turnEntry
}

func newTurnTracker(dataRoot string) *turnTracker {
	dir := ""
	if dataRoot != "" {
		dir = filepath.Join(dataRoot, "queues")
	}
	return &turnTracker{dir: dir, entries: make(map[string]*turnEntry)}
}

func nowSec() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func newShortID(prefix string) string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:])
}

func (t *turnTracker) queuePath(sid string) string {
	if t.dir == "" {
		return ""
	}
	safe := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, sid)
	if safe == "" {
		return ""
	}
	return filepath.Join(t.dir, safe+".json")
}

// entryLocked returns the entry for sid, loading a persisted queue once.
// A queue found on disk at load time belongs to a hub that has since
// restarted, so nothing is running for it: load it paused.
func (t *turnTracker) entryLocked(sid string) *turnEntry {
	e := t.entries[sid]
	if e == nil {
		e = &turnEntry{}
		t.entries[sid] = e
	}
	if !e.loaded {
		e.loaded = true
		if p := t.queuePath(sid); p != "" {
			if b, err := os.ReadFile(p); err == nil {
				var qf queueFile
				if json.Unmarshal(b, &qf) == nil && qf.SessionID == sid && len(qf.Items) > 0 {
					e.queue = qf.Items
					e.paused = true
				}
			}
		}
	}
	return e
}

func (t *turnTracker) persistLocked(sid string, e *turnEntry) {
	p := t.queuePath(sid)
	if p == "" {
		return
	}
	if len(e.queue) == 0 {
		_ = os.Remove(p)
		return
	}
	if err := os.MkdirAll(t.dir, 0o700); err != nil {
		return
	}
	b, err := json.MarshalIndent(queueFile{SessionID: sid, Paused: e.paused, Items: e.queue}, "", "  ")
	if err != nil {
		return
	}
	tmp := p + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, p)
	}
}

func (t *turnTracker) snapshotLocked(sid string, e *turnEntry) TurnStatus {
	st := TurnStatus{
		SessionID:   sid,
		State:       TurnIdle,
		LastTurnID:  e.lastTurnID,
		LastResult:  e.lastResult,
		LastEndedAt: e.lastEndedAt,
		QueuePaused: e.paused && len(e.queue) > 0,
		Queue:       append([]QueuedMessage{}, e.queue...),
		ServerTime:  nowSec(),
	}
	if e.running {
		st.State = TurnRunning
		st.TurnID = e.turnID
		st.StartedAt = e.startedAt
		st.UpdatedAt = e.updatedAt
	}
	return st
}

// Status returns the current turn status for sid.
func (t *turnTracker) Status(sid string) TurnStatus {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.snapshotLocked(sid, t.entryLocked(sid))
}

// Touch records stream activity for a running turn (last-update).
func (t *turnTracker) Touch(sid string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if e := t.entries[sid]; e != nil && e.running {
		e.updatedAt = nowSec()
	}
}

// Submit starts a turn when the session is idle and nothing is queued.
// Otherwise the message is appended to the queue (FIFO: new text always goes
// behind what is already waiting, even if the queue is paused). Returns
// started=true when the caller must run content now; the entry is already
// marked running so concurrent submits queue behind it.
func (t *turnTracker) Submit(sid, content string) (started bool, item *QueuedMessage, st TurnStatus, err error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return false, nil, TurnStatus{}, errQueueEmpty
	}
	if len(content) > maxQueueContent {
		return false, nil, TurnStatus{}, errQueueTooLarge
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.entryLocked(sid)
	if !e.running && len(e.queue) == 0 {
		t.beginLocked(e)
		return true, nil, t.snapshotLocked(sid, e), nil
	}
	if len(e.queue) >= maxQueueItems {
		return false, nil, t.snapshotLocked(sid, e), errQueueFull
	}
	now := nowSec()
	q := QueuedMessage{ID: newShortID("q-"), Content: content, CreatedAt: now}
	e.queue = append(e.queue, q)
	t.persistLocked(sid, e)
	return false, &q, t.snapshotLocked(sid, e), nil
}

func (t *turnTracker) beginLocked(e *turnEntry) {
	now := nowSec()
	e.running = true
	e.turnID = newShortID("t-")
	e.startedAt = now
	e.updatedAt = now
	e.cancelReq = false
}

// MarkCancel records that Stop was requested for the running turn so it is
// finished as "cancelled" (which pauses the queue) even if the agent ends
// the stream without saying so.
func (t *turnTracker) MarkCancel(sid string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.entryLocked(sid)
	if !e.running {
		return false
	}
	e.cancelReq = true
	return true
}

// Finish marks the running turn as ended with result and, unless the queue
// is paused (or the turn was stopped / failed, which pauses it), pops the
// next queued message and marks a new turn running for it.
func (t *turnTracker) Finish(sid, result string) (next *QueuedMessage, st TurnStatus) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.entryLocked(sid)
	if e.running {
		e.lastTurnID = e.turnID
	}
	if e.cancelReq {
		result = "cancelled"
	}
	e.cancelReq = false
	e.running = false
	e.turnID = ""
	e.lastResult = result
	e.lastEndedAt = nowSec()
	if result != "done" && len(e.queue) > 0 {
		e.paused = true
	}
	if !e.paused && len(e.queue) > 0 {
		q := e.queue[0]
		e.queue = e.queue[1:]
		t.beginLocked(e)
		next = &q
	}
	if len(e.queue) == 0 {
		e.paused = false
	}
	t.persistLocked(sid, e)
	return next, t.snapshotLocked(sid, e)
}

// Resume un-pauses the queue. If the session is idle it pops the next
// message and marks a turn running for it (returned as next).
func (t *turnTracker) Resume(sid string) (next *QueuedMessage, st TurnStatus) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.entryLocked(sid)
	e.paused = false
	if !e.running && len(e.queue) > 0 {
		q := e.queue[0]
		e.queue = e.queue[1:]
		t.beginLocked(e)
		next = &q
	}
	t.persistLocked(sid, e)
	return next, t.snapshotLocked(sid, e)
}

// Edit replaces the content of a queued message.
func (t *turnTracker) Edit(sid, qid, content string) (TurnStatus, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return TurnStatus{}, errQueueEmpty
	}
	if len(content) > maxQueueContent {
		return TurnStatus{}, errQueueTooLarge
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.entryLocked(sid)
	for i := range e.queue {
		if e.queue[i].ID == qid {
			e.queue[i].Content = content
			e.queue[i].UpdatedAt = nowSec()
			t.persistLocked(sid, e)
			return t.snapshotLocked(sid, e), nil
		}
	}
	return t.snapshotLocked(sid, e), errQueueNotFound
}

// Remove deletes a queued message.
func (t *turnTracker) Remove(sid, qid string) (TurnStatus, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.entryLocked(sid)
	for i := range e.queue {
		if e.queue[i].ID == qid {
			e.queue = append(e.queue[:i], e.queue[i+1:]...)
			if len(e.queue) == 0 {
				e.paused = false
			}
			t.persistLocked(sid, e)
			return t.snapshotLocked(sid, e), nil
		}
	}
	return t.snapshotLocked(sid, e), errQueueNotFound
}

// ---- Server glue ----

func (s *Server) broadcastTurnStatus(st TurnStatus) {
	s.Hub.Broadcast(map[string]any{
		"type": "turn_status", "session_id": st.SessionID, "status": st,
	}, st.SessionID, nil)
}

func (s *Server) runnerFor(sid string) func(context.Context, string, string, func(string)) (string, error) {
	if _, isBuild := buildsessions.StripPrefix(sid); isBuild {
		return s.runBuildChat
	}
	return s.runChat
}

// submitMessage is the one entry point for user messages (WS chat.send and
// POST /messages). If the session is idle it starts a turn in the
// background; otherwise the message is queued. onUserSaved / onErr only
// apply when a turn is started for this message.
func (s *Server) submitMessage(sid, content string, onUserSaved func(string), onErr func(error)) (*QueuedMessage, TurnStatus, error) {
	started, item, st, err := s.Turns.Submit(sid, content)
	if err != nil {
		return nil, st, err
	}
	s.broadcastTurnStatus(st)
	if started {
		go s.turnLoop(sid, strings.TrimSpace(content), onUserSaved, onErr)
	}
	return item, st, nil
}

// turnLoop runs content, then keeps draining the queue while the tracker
// hands back a next message. The tracker already marked each turn running
// before the goroutine runs it, so status never flickers to idle between
// queued turns.
func (s *Server) turnLoop(sid, content string, onUserSaved func(string), onErr func(error)) {
	for {
		run := s.runnerFor(sid)
		_, isBuild := buildsessions.StripPrefix(sid)
		ctx := context.Background()
		res := &turnResult{}
		ctx = context.WithValue(ctx, turnResultKey{}, res)
		_, err := run(ctx, sid, content, onUserSaved)
		result := res.result()
		if err != nil {
			result = "error"
			if isBuild {
				s.Hub.Broadcast(map[string]any{
					"type": "assistant_done", "session_id": sid,
					"content": "", "error": err.Error(), "hint": agentHint(err),
				}, sid, nil)
			}
			if onErr != nil {
				onErr(err)
			}
		}
		next, st := s.Turns.Finish(sid, result)
		s.broadcastTurnStatus(st)
		if next == nil {
			return
		}
		content, onUserSaved, onErr = next.Content, nil, nil
	}
}

// turnResult lets runChat / runBuildChat report how the turn ended without
// changing their signatures (tests and other callers use them directly).
type turnResultKey struct{}

type turnResult struct {
	mu        sync.Mutex
	cancelled bool
	errored   bool
}

func (r *turnResult) result() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case r.cancelled:
		return "cancelled"
	case r.errored:
		return "error"
	}
	return "done"
}

// noteTurnEvent inspects a streamed event and records cancellation / error
// on the turnResult carried by ctx (if any).
func noteTurnEvent(ctx context.Context, event map[string]any) {
	r, _ := ctx.Value(turnResultKey{}).(*turnResult)
	if r == nil {
		return
	}
	if t, _ := event["type"].(string); t != "assistant_done" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	errStr, _ := event["error"].(string)
	if event["cancelled"] == true || errStr == "cancelled" {
		r.cancelled = true
	} else if errStr != "" {
		r.errored = true
	}
}
