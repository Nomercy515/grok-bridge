package hub_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"grok-bridge/internal/agent"
	"grok-bridge/internal/auth"
	"grok-bridge/internal/hub"
	"grok-bridge/internal/restart"
	"grok-bridge/internal/sessions"
)

// gatedAgent holds every turn open until release() (or ctx cancel), and
// records the prompts it was asked to answer, in order.
type gatedAgent struct {
	mu   sync.Mutex
	seen []string
	gate chan struct{}
}

func newGatedAgent() *gatedAgent { return &gatedAgent{gate: make(chan struct{}, 16)} }

func (g *gatedAgent) release() { g.gate <- struct{}{} }

func (g *gatedAgent) prompts() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string{}, g.seen...)
}

func (g *gatedAgent) StreamReply(ctx context.Context, sid, msg string, _ []map[string]any) (<-chan agent.Event, error) {
	g.mu.Lock()
	g.seen = append(g.seen, msg)
	g.mu.Unlock()
	ch := make(chan agent.Event, 4)
	go func() {
		defer close(ch)
		ch <- agent.Event{"type": "assistant_start", "session_id": sid}
		select {
		case <-ctx.Done():
			ch <- agent.Event{"type": "assistant_done", "session_id": sid, "content": "", "cancelled": true, "error": "cancelled"}
		case <-g.gate:
			ch <- agent.Event{"type": "assistant_delta", "session_id": sid, "delta": "re: " + msg}
			ch <- agent.Event{"type": "assistant_done", "session_id": sid, "content": "re: " + msg}
		}
	}()
	return ch, nil
}

func gatedServer(t *testing.T, dir string) (string, *hub.Server, *gatedAgent, func()) {
	t.Helper()
	store, err := sessions.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	a, err := auth.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	a.RotateForTests(testCode, testToken)
	g := newGatedAgent()
	srv, err := hub.NewServer(hub.Options{
		Store: store, Agent: g, Auth: a,
		Restart: restart.New(dir, func() {}),
		WebFS:   os.DirFS(findWeb(t)),
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := newHTTPTestServer(srv.Handler())
	return ts.URL, srv, g, ts.Close
}

func doJSON(t *testing.T, method, url string, body any, authed bool) (int, map[string]any) {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, url, rdr)
	req.Header.Set("Content-Type", "application/json")
	if authed {
		req.Header.Set("Authorization", "Bearer "+testToken)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func getStatus(t *testing.T, base, sid string) map[string]any {
	t.Helper()
	code, st := doJSON(t, "GET", base+"/api/sessions/"+sid+"/status", nil, true)
	if code != 200 {
		t.Fatalf("status code %d %v", code, st)
	}
	return st
}

func queueContents(st map[string]any) []string {
	var out []string
	q, _ := st["queue"].([]any)
	for _, it := range q {
		m, _ := it.(map[string]any)
		c, _ := m["content"].(string)
		out = append(out, c)
	}
	return out
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func newSession(t *testing.T, srv *hub.Server) string {
	t.Helper()
	sess, err := srv.Hub.Store.Create("queue test")
	if err != nil {
		t.Fatal(err)
	}
	return sess.ID
}

func post(t *testing.T, base, sid, content string) (int, map[string]any) {
	return doJSON(t, "POST", base+"/api/sessions/"+sid+"/messages", map[string]any{"content": content}, true)
}

func TestSessionStatusEndpointAndAuth(t *testing.T) {
	base, srv, g, done := gatedServer(t, t.TempDir())
	defer done()
	sid := newSession(t, srv)

	// Auth: every status / queue route needs the bearer token.
	for _, rt := range []struct{ m, p string }{
		{"GET", "/status"}, {"GET", "/queue"}, {"POST", "/queue"},
		{"POST", "/queue/send"}, {"PUT", "/queue/q-x"}, {"DELETE", "/queue/q-x"},
	} {
		code, _ := doJSON(t, rt.m, base+"/api/sessions/"+sid+rt.p, map[string]any{"content": "x"}, false)
		if code != 401 {
			t.Fatalf("%s %s unauthenticated: got %d, want 401", rt.m, rt.p, code)
		}
	}
	if code, _ := doJSON(t, "GET", base+"/api/sessions/nope/status", nil, true); code != 404 {
		t.Fatalf("unknown session: got %d", code)
	}

	st := getStatus(t, base, sid)
	if st["state"] != "idle" || st["session_id"] != sid {
		t.Fatalf("fresh status: %v", st)
	}
	if code, _ := post(t, base, sid, "hello"); code != 200 {
		t.Fatalf("first send code %d", code)
	}
	st = getStatus(t, base, sid)
	if st["state"] != "running" || st["turn_id"] == "" || st["started_at"] == nil || st["updated_at"] == nil {
		t.Fatalf("running status: %v", st)
	}
	turnID := st["turn_id"]
	g.release()
	waitFor(t, "idle", func() bool { return getStatus(t, base, sid)["state"] == "idle" })
	st = getStatus(t, base, sid)
	if st["last_result"] != "done" || st["last_turn_id"] != turnID {
		t.Fatalf("finished status: %v", st)
	}
}

func TestQueueFIFODrainsWhenIdle(t *testing.T) {
	base, srv, g, done := gatedServer(t, t.TempDir())
	defer done()
	sid := newSession(t, srv)

	if code, _ := post(t, base, sid, "one"); code != 200 {
		t.Fatalf("one: %d", code)
	}
	for _, m := range []string{"two", "three"} {
		code, body := post(t, base, sid, m)
		if code != 202 || body["queued"] != true {
			t.Fatalf("%s should queue: %d %v", m, code, body)
		}
	}
	// Explicit queue route behaves the same.
	if code, body := doJSON(t, "POST", base+"/api/sessions/"+sid+"/queue", map[string]any{"content": "four"}, true); code != 200 || body["queued"] != true {
		t.Fatalf("queue POST: %d %v", code, body)
	}
	if got := queueContents(getStatus(t, base, sid)); strings.Join(got, ",") != "two,three,four" {
		t.Fatalf("queue order: %v", got)
	}
	for i := 0; i < 4; i++ {
		waitFor(t, "turn running", func() bool { return len(g.prompts()) == i+1 })
		g.release()
	}
	waitFor(t, "drained", func() bool {
		st := getStatus(t, base, sid)
		return st["state"] == "idle" && len(queueContents(st)) == 0
	})
	if got := strings.Join(g.prompts(), ","); got != "one,two,three,four" {
		t.Fatalf("agent order: %s", got)
	}
	// Transcript has all 4 user + 4 assistant messages in order.
	sess, _ := srv.Hub.Store.Get(sid)
	var users []string
	for _, m := range sess.Messages {
		if m.Role == "user" {
			users = append(users, m.Content)
		}
	}
	if strings.Join(users, ",") != "one,two,three,four" {
		t.Fatalf("transcript users: %v", users)
	}
}

func TestStopKeepsQueueAndSendNowResumes(t *testing.T) {
	base, srv, g, done := gatedServer(t, t.TempDir())
	defer done()
	sid := newSession(t, srv)
	post(t, base, sid, "long one")
	post(t, base, sid, "next")
	waitFor(t, "first turn", func() bool { return len(g.prompts()) == 1 })

	code, body := doJSON(t, "POST", base+"/api/sessions/"+sid+"/cancel", map[string]any{}, true)
	if code != 200 || body["turn_cancelled"] != true {
		t.Fatalf("cancel: %d %v", code, body)
	}
	waitFor(t, "idle after stop", func() bool { return getStatus(t, base, sid)["state"] == "idle" })
	st := getStatus(t, base, sid)
	if got := queueContents(st); len(got) != 1 || got[0] != "next" {
		t.Fatalf("queue after stop: %v", got)
	}
	if st["queue_paused"] != true || st["last_result"] != "cancelled" {
		t.Fatalf("status after stop: %v", st)
	}
	time.Sleep(100 * time.Millisecond)
	if n := len(g.prompts()); n != 1 {
		t.Fatalf("stop must not auto-send the queue; agent saw %d prompts", n)
	}

	code, body = doJSON(t, "POST", base+"/api/sessions/"+sid+"/queue/send", nil, true)
	if code != 200 || body["sent"] == nil {
		t.Fatalf("send now: %d %v", code, body)
	}
	waitFor(t, "queued turn", func() bool { return len(g.prompts()) == 2 })
	if g.prompts()[1] != "next" {
		t.Fatalf("resumed prompt: %v", g.prompts())
	}
	g.release()
	waitFor(t, "idle", func() bool {
		st := getStatus(t, base, sid)
		return st["state"] == "idle" && len(queueContents(st)) == 0 && st["queue_paused"] == false
	})
}

func TestQueueEditRemoveAndPersistence(t *testing.T) {
	dir := t.TempDir()
	base, srv, g, done := gatedServer(t, dir)
	sid := newSession(t, srv)
	post(t, base, sid, "running")
	_, b1 := post(t, base, sid, "draft A")
	_, b2 := post(t, base, sid, "draft B")
	id1 := b1["queue_item"].(map[string]any)["id"].(string)
	id2 := b2["queue_item"].(map[string]any)["id"].(string)

	if code, _ := doJSON(t, "PUT", base+"/api/sessions/"+sid+"/queue/"+id1, map[string]any{"content": "edited A"}, true); code != 200 {
		t.Fatalf("edit: %d", code)
	}
	if code, _ := doJSON(t, "PUT", base+"/api/sessions/"+sid+"/queue/"+id1, map[string]any{"content": "  "}, true); code != 400 {
		t.Fatalf("empty edit should 400, got %d", code)
	}
	if code, _ := doJSON(t, "DELETE", base+"/api/sessions/"+sid+"/queue/"+id2, nil, true); code != 200 {
		t.Fatalf("remove: %d", code)
	}
	if code, _ := doJSON(t, "DELETE", base+"/api/sessions/"+sid+"/queue/"+id2, nil, true); code != 404 {
		t.Fatalf("second remove should 404, got %d", code)
	}
	if got := queueContents(getStatus(t, base, sid)); len(got) != 1 || got[0] != "edited A" {
		t.Fatalf("queue after edit/remove: %v", got)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "queues", "*.json"))
	if len(files) != 1 {
		t.Fatalf("expected 1 persisted queue file, got %v", files)
	}
	if fi, err := os.Stat(files[0]); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("queue file perms: %v %v", fi.Mode(), err)
	}

	// Simulate a hub restart mid-turn: a new server on the same data dir
	// sees the queue, idle (nothing is running any more) and paused.
	done()
	_ = g
	base2, _, g2, done2 := gatedServer(t, dir)
	defer done2()
	st := getStatus(t, base2, sid)
	if st["state"] != "idle" || st["queue_paused"] != true {
		t.Fatalf("status after restart: %v", st)
	}
	if got := queueContents(st); len(got) != 1 || got[0] != "edited A" {
		t.Fatalf("queue after restart: %v", got)
	}
	doJSON(t, "POST", base2+"/api/sessions/"+sid+"/queue/send", nil, true)
	waitFor(t, "resumed after restart", func() bool { return len(g2.prompts()) == 1 })
	g2.release()
	waitFor(t, "queue file removed", func() bool {
		files, _ := filepath.Glob(filepath.Join(dir, "queues", "*.json"))
		return len(files) == 0 && getStatus(t, base2, sid)["state"] == "idle"
	})
}

func TestWSSnapshotStatusAndQueuedAck(t *testing.T) {
	base, srv, g, done := gatedServer(t, t.TempDir())
	defer done()
	sid := newSession(t, srv)
	wsURL := "ws" + strings.TrimPrefix(base, "http") + "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, http.Header{"Sec-WebSocket-Protocol": []string{"grok.bearer." + testToken}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	read := func(want string) map[string]any {
		t.Helper()
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		for {
			var m map[string]any
			if err := conn.ReadJSON(&m); err != nil {
				t.Fatalf("waiting for %s: %v", want, err)
			}
			if m["type"] == want {
				return m
			}
		}
	}
	read("hello")
	_ = conn.WriteJSON(map[string]any{"type": "chat.send", "session_id": sid, "content": "first"})
	read("assistant_start")
	_ = conn.WriteJSON(map[string]any{"type": "chat.send", "session_id": sid, "content": "second"})
	q := read("queued")
	if q["content"] != "second" {
		t.Fatalf("queued ack: %v", q)
	}
	// A (re)subscribe snapshot carries the authoritative status.
	_ = conn.WriteJSON(map[string]any{"type": "session.subscribe", "session_id": sid})
	snap := read("session.snapshot")
	st, _ := snap["status"].(map[string]any)
	if st == nil || st["state"] != "running" || len(queueContents(st)) != 1 {
		t.Fatalf("snapshot status: %v", snap["status"])
	}
	g.release()
	g.release()
	// turn_status eventually reports idle with an empty queue.
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		var m map[string]any
		if err := conn.ReadJSON(&m); err != nil {
			t.Fatal(err)
		}
		if m["type"] != "turn_status" {
			continue
		}
		st := m["status"].(map[string]any)
		if st["state"] == "idle" && len(queueContents(st)) == 0 {
			break
		}
	}
	if got := strings.Join(g.prompts(), ","); got != "first,second" {
		t.Fatalf("ws order: %s", got)
	}
}

func newHTTPTestServer(h http.Handler) *httptest.Server { return httptest.NewServer(h) }
