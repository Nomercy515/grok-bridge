package hub_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"grok-bridge/internal/agent"
	"grok-bridge/internal/auth"
	"grok-bridge/internal/bridge"
	"grok-bridge/internal/grokacp"
	"grok-bridge/internal/hub"
	"grok-bridge/internal/restart"
	"grok-bridge/internal/sessions"
)

const (
	testCode  = "123456"
	testToken = "test-token-grok-bridge-mvp"
)

func testServer(t *testing.T, opts ...func(*hub.Options)) (*httptest.Server, *hub.Server, string) {
	t.Helper()
	dir := t.TempDir()
	store, err := sessions.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	a, err := auth.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	a.RotateForTests(testCode, testToken)
	demo := &agent.DemoAgent{Delay: time.Millisecond}
	webDir := findWeb(t)
	o := hub.Options{
		Store: store, Agent: demo, Auth: a, SeedDemo: true,
		Restart: restart.New(dir, func() {}),
		WebFS:   os.DirFS(webDir),
	}
	for _, f := range opts {
		f(&o)
	}
	srv, err := hub.NewServer(o)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, srv, dir
}

func findWeb(t *testing.T) string {
	t.Helper()
	candidates := []string{"web", "../web", "../../web"}
	wd, _ := os.Getwd()
	for _, c := range candidates {
		p := filepath.Join(wd, c)
		if _, err := os.Stat(filepath.Join(p, "index.html")); err == nil {
			abs, _ := filepath.Abs(p)
			return abs
		}
	}
	// walk up from module
	for _, c := range []string{
		filepath.Join(wd, "..", "..", "web"),
		"/workspace/grok-bridge/web",
	} {
		if _, err := os.Stat(filepath.Join(c, "index.html")); err == nil {
			return c
		}
	}
	t.Fatal("web dir not found")
	return ""
}

func authHeader() http.Header {
	h := http.Header{}
	h.Set("Authorization", "Bearer "+testToken)
	return h
}

func TestHealth(t *testing.T) {
	ts, _, _ := testServer(t)
	res, err := http.Get(ts.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatal(res.Status)
	}
	var body map[string]any
	_ = json.NewDecoder(res.Body).Decode(&body)
	if body["ok"] != true {
		t.Fatalf("%v", body)
	}
}

func TestIndex(t *testing.T) {
	ts, _, _ := testServer(t)
	res, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || !bytes.Contains(b, []byte("Grok Bridge")) {
		t.Fatalf("status=%d body=%s", res.StatusCode, b[:min(200, len(b))])
	}
}

func TestPairAndSessions(t *testing.T) {
	ts, _, _ := testServer(t)
	res, err := http.Post(ts.URL+"/api/auth/pair", "application/json", strings.NewReader(`{"code":"123456"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(res.Body).Decode(&body)
	if res.StatusCode != 200 || body["token"] != testToken {
		t.Fatalf("%d %v", res.StatusCode, body)
	}
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/sessions", nil)
	req.Header = authHeader()
	res2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res2.Body.Close()
	if res2.StatusCode != 200 {
		t.Fatal(res2.Status)
	}
}

func TestUnauthorized(t *testing.T) {
	ts, _, _ := testServer(t)
	res, err := http.Get(ts.URL + "/api/sessions")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatal(res.Status)
	}
}

func TestRotatePairing(t *testing.T) {
	ts, srv, _ := testServer(t)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/auth/rotate-pairing", nil)
	req.Header = authHeader()
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(res.Body).Decode(&body)
	if res.StatusCode != 200 || body["pairing_code"] == nil {
		t.Fatalf("%v", body)
	}
	_ = srv
}

func TestRotateToken(t *testing.T) {
	ts, _, _ := testServer(t)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/auth/rotate", nil)
	req.Header = authHeader()
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(res.Body).Decode(&body)
	newTok, _ := body["token"].(string)
	if res.StatusCode != 200 || newTok == "" || newTok == testToken {
		t.Fatalf("%v", body)
	}
	req2, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/sessions", nil)
	req2.Header.Set("Authorization", "Bearer "+testToken)
	res2, _ := http.DefaultClient.Do(req2)
	defer res2.Body.Close()
	if res2.StatusCode != 401 {
		t.Fatal("old token should fail")
	}
}

func TestRestart(t *testing.T) {
	called := false
	ts, _, _ := testServer(t, func(o *hub.Options) {
		o.Restart = restart.New(o.Store.Root, func() { called = true })
	})
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/control/restart", nil)
	req.Header = authHeader()
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 || !called {
		t.Fatalf("status=%d called=%v", res.StatusCode, called)
	}
}

func TestHTTPMessageFallback(t *testing.T) {
	ts, _, _ := testServer(t)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/sessions", strings.NewReader(`{"title":"t"}`))
	req.Header = authHeader()
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var sess map[string]any
	_ = json.NewDecoder(res.Body).Decode(&sess)
	res.Body.Close()
	sid, _ := sess["id"].(string)

	req2, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/sessions/"+sid+"/messages", strings.NewReader(`{"content":"hello hub"}`))
	req2.Header = authHeader()
	req2.Header.Set("Content-Type", "application/json")
	res2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer res2.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(res2.Body).Decode(&body)
	if res2.StatusCode != 200 || body["ok"] != true {
		t.Fatalf("%d %v", res2.StatusCode, body)
	}
	// wait for assistant persist
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		req3, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/sessions/"+sid, nil)
		req3.Header = authHeader()
		res3, _ := http.DefaultClient.Do(req3)
		var full map[string]any
		_ = json.NewDecoder(res3.Body).Decode(&full)
		res3.Body.Close()
		msgs, _ := full["messages"].([]any)
		if len(msgs) >= 2 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("assistant message not persisted")
}

func TestWSChat(t *testing.T) {
	ts, _, _ := testServer(t)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/sessions", strings.NewReader(`{"title":"ws"}`))
	req.Header = authHeader()
	req.Header.Set("Content-Type", "application/json")
	res, _ := http.DefaultClient.Do(req)
	var sess map[string]any
	_ = json.NewDecoder(res.Body).Decode(&sess)
	res.Body.Close()
	sid := sess["id"].(string)

	u := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
	hdr := http.Header{}
	hdr.Set("Sec-WebSocket-Protocol", hub.WSBearerPrefix+testToken)
	conn, _, err := websocket.DefaultDialer.Dial(u, hdr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	var hello map[string]any
	_ = json.Unmarshal(msg, &hello)
	if hello["type"] != "hello" {
		t.Fatalf("%v", hello)
	}
	_ = conn.WriteJSON(map[string]any{"type": "session.subscribe", "session_id": sid})
	_, msg, _ = conn.ReadMessage()
	var snap map[string]any
	_ = json.Unmarshal(msg, &snap)
	if snap["type"] != "session.snapshot" {
		t.Fatalf("%v", snap)
	}
	_ = conn.WriteJSON(map[string]any{"type": "chat.send", "session_id": sid, "content": "ping"})
	deadline := time.Now().Add(5 * time.Second)
	gotDone := false
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, msg, err := conn.ReadMessage()
		if err != nil {
			continue
		}
		var ev map[string]any
		_ = json.Unmarshal(msg, &ev)
		if ev["type"] == "assistant_done" {
			gotDone = true
			break
		}
	}
	if !gotDone {
		t.Fatal("no assistant_done")
	}
}

func TestAgentMockBridge(t *testing.T) {
	secret := "test-bridge-secret-xyz"
	reg := bridge.NewJobRegistry()
	vb := bridge.NewAgentBridge("mock", "", secret, 10*time.Second, reg)
	ts, _, _ := testServer(t, func(o *hub.Options) {
		o.Agent = vb
		o.BridgeSecret = secret
		o.JobRegistry = reg
	})

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/sessions", strings.NewReader(`{"title":"v"}`))
	req.Header = authHeader()
	req.Header.Set("Content-Type", "application/json")
	res, _ := http.DefaultClient.Do(req)
	var sess map[string]any
	_ = json.NewDecoder(res.Body).Decode(&sess)
	res.Body.Close()
	sid := sess["id"].(string)

	req2, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/sessions/"+sid+"/messages", strings.NewReader(`{"content":"hello agent"}`))
	req2.Header = authHeader()
	req2.Header.Set("Content-Type", "application/json")
	res2, _ := http.DefaultClient.Do(req2)
	defer res2.Body.Close()
	if res2.StatusCode != 200 {
		t.Fatal(res2.Status)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		req3, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/sessions/"+sid, nil)
		req3.Header = authHeader()
		res3, _ := http.DefaultClient.Do(req3)
		var full map[string]any
		_ = json.NewDecoder(res3.Body).Decode(&full)
		res3.Body.Close()
		for _, m := range full["messages"].([]any) {
			mm := m.(map[string]any)
			if mm["role"] == "assistant" && strings.Contains(mm["content"].(string), "MockAgent") {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("MockAgent reply missing")
}

func TestBridgeRoutesAuth(t *testing.T) {
	secret := "bridge-sec-1"
	reg := bridge.NewJobRegistry()
	ts, _, _ := testServer(t, func(o *hub.Options) {
		o.BridgeSecret = secret
		o.JobRegistry = reg
	})
	job := reg.Create("s1", "hi", nil, "")
	res, _ := http.Post(ts.URL+"/bridge/v1/jobs/"+job.ID+"/events", "application/json", strings.NewReader(`{"type":"assistant_delta","delta":"x"}`))
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatal(res.Status)
	}
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/bridge/v1/jobs/"+job.ID+"/events", strings.NewReader(`{"type":"assistant_delta","delta":"x"}`))
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", "application/json")
	res2, _ := http.DefaultClient.Do(req)
	defer res2.Body.Close()
	if res2.StatusCode != 200 {
		b, _ := io.ReadAll(res2.Body)
		t.Fatalf("%d %s", res2.StatusCode, b)
	}
}

func TestEndpointAPI(t *testing.T) {
	ts, _, dir := testServer(t)
	_ = os.WriteFile(filepath.Join(dir, "endpoint.json"), []byte(`{"source":"tailscale","url":"https://x.ts.net:4020/","magicdns":"x.ts.net","tailscale_ipv4":"100.1.1.1","port":4020}`), 0644)
	res, _ := http.Get(ts.URL + "/api/endpoint")
	defer res.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(res.Body).Decode(&body)
	if body["configured"] != true {
		t.Fatalf("%v", body)
	}
}

func TestAuthFileMode(t *testing.T) {
	_, _, dir := testServer(t)
	info, err := os.Stat(filepath.Join(dir, "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("auth.json perms too open: %o", info.Mode().Perm())
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// silence unused import

func TestToolEventsRoundTripWithID(t *testing.T) {
	secret := "test-bridge-secret-tools"
	reg := bridge.NewJobRegistry()
	vb := bridge.NewAgentBridge("mock", "", secret, 10*time.Second, reg)
	ts, _, _ := testServer(t, func(o *hub.Options) {
		o.Agent = vb
		o.BridgeSecret = secret
		o.JobRegistry = reg
	})

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/sessions", strings.NewReader(`{"title":"tools"}`))
	req.Header = authHeader()
	req.Header.Set("Content-Type", "application/json")
	res, _ := http.DefaultClient.Do(req)
	var sess map[string]any
	_ = json.NewDecoder(res.Body).Decode(&sess)
	res.Body.Close()
	sid := sess["id"].(string)

	req2, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/sessions/"+sid+"/messages", strings.NewReader(`{"content":"show tools"}`))
	req2.Header = authHeader()
	req2.Header.Set("Content-Type", "application/json")
	res2, _ := http.DefaultClient.Do(req2)
	res2.Body.Close()
	if res2.StatusCode != 200 {
		t.Fatal(res2.Status)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		req3, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/sessions/"+sid, nil)
		req3.Header = authHeader()
		res3, _ := http.DefaultClient.Do(req3)
		var full map[string]any
		_ = json.NewDecoder(res3.Body).Decode(&full)
		res3.Body.Close()
		msgs, _ := full["messages"].([]any)
		for _, m := range msgs {
			mm := m.(map[string]any)
			if mm["role"] != "assistant" {
				continue
			}
			tools, _ := mm["tools"].([]any)
			if len(tools) == 0 {
				continue
			}
			tool := tools[0].(map[string]any)
			id, _ := tool["id"].(string)
			status, _ := tool["status"].(string)
			if id == "" {
				t.Fatalf("expected tool.id in persisted tools: %v", tool)
			}
			if status != "done" {
				t.Fatalf("expected status=done, got %q in %v", status, tool)
			}
			if tool["result"] == nil {
				t.Fatalf("expected result: %v", tool)
			}
			if tool["arguments"] == nil {
				t.Fatalf("expected arguments: %v", tool)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("assistant tools with id/status not persisted")
}

func TestToolCardAliasPersists(t *testing.T) {
	ag := &scriptedToolAgent{}
	ts, _, _ := testServer(t, func(o *hub.Options) {
		o.Agent = ag
	})

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/sessions", strings.NewReader(`{"title":"alias"}`))
	req.Header = authHeader()
	req.Header.Set("Content-Type", "application/json")
	res, _ := http.DefaultClient.Do(req)
	var sess map[string]any
	_ = json.NewDecoder(res.Body).Decode(&sess)
	res.Body.Close()
	sid := sess["id"].(string)

	req2, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/sessions/"+sid+"/messages", strings.NewReader(`{"content":"alias"}`))
	req2.Header = authHeader()
	req2.Header.Set("Content-Type", "application/json")
	res2, _ := http.DefaultClient.Do(req2)
	res2.Body.Close()
	if res2.StatusCode != 200 {
		t.Fatal(res2.Status)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		req3, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/sessions/"+sid, nil)
		req3.Header = authHeader()
		res3, _ := http.DefaultClient.Do(req3)
		var full map[string]any
		_ = json.NewDecoder(res3.Body).Decode(&full)
		res3.Body.Close()
		msgs, _ := full["messages"].([]any)
		for _, m := range msgs {
			mm := m.(map[string]any)
			if mm["role"] != "assistant" {
				continue
			}
			tools, _ := mm["tools"].([]any)
			if len(tools) != 1 {
				continue
			}
			tool := tools[0].(map[string]any)
			if tool["id"] != "card-1" || tool["status"] != "done" {
				t.Fatalf("%v", tool)
			}
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatal("tool_card alias not persisted")
}

type scriptedToolAgent struct{}

func (s *scriptedToolAgent) StreamReply(ctx context.Context, sessionID, userMessage string, history []map[string]any) (<-chan agent.Event, error) {
	ch := make(chan agent.Event, 8)
	go func() {
		defer close(ch)
		ch <- agent.Event{"type": "assistant_start", "session_id": sessionID}
		ch <- agent.Event{
			"type": "tool_card", "session_id": sessionID,
			"tool": map[string]any{
				"id": "card-1", "name": "alias_tool",
				"arguments": map[string]any{"n": 1}, "status": "running",
			},
		}
		ch <- agent.Event{
			"type": "tool_card", "session_id": sessionID,
			"tool": map[string]any{
				"id": "card-1", "name": "alias_tool",
				"result": map[string]any{"ok": true}, "status": "done",
			},
		}
		ch <- agent.Event{"type": "assistant_done", "session_id": sessionID, "content": "alias ok"}
	}()
	return ch, nil
}

func TestSessionCancelUnblocksMock(t *testing.T) {
	secret := "test-bridge-secret-cancel"
	reg := bridge.NewJobRegistry()
	vb := bridge.NewAgentBridge("webhook", "", secret, 10*time.Second, reg)
	ts, _, _ := testServer(t, func(o *hub.Options) {
		o.Agent = vb
		o.BridgeSecret = secret
		o.JobRegistry = reg
	})

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/sessions", strings.NewReader(`{"title":"cancel"}`))
	req.Header = authHeader()
	req.Header.Set("Content-Type", "application/json")
	res, _ := http.DefaultClient.Do(req)
	var sess map[string]any
	_ = json.NewDecoder(res.Body).Decode(&sess)
	res.Body.Close()
	sid := sess["id"].(string)

	u := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
	hdr := http.Header{}
	hdr.Set("Sec-WebSocket-Protocol", hub.WSBearerPrefix+testToken)
	conn, _, err := websocket.DefaultDialer.Dial(u, hdr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _, _ = conn.ReadMessage() // hello
	_ = conn.WriteJSON(map[string]any{"type": "session.subscribe", "session_id": sid})
	_, _, _ = conn.ReadMessage() // snapshot

	_ = conn.WriteJSON(map[string]any{"type": "chat.send", "session_id": sid, "content": "please wait"})

	// wait until job is active
	var jobID string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if j := reg.ActiveJobForSession(sid); j != nil {
			jobID = j.ID
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if jobID == "" {
		t.Fatal("no active job")
	}

	reqC, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/sessions/"+sid+"/cancel", strings.NewReader("{}"))
	reqC.Header = authHeader()
	reqC.Header.Set("Content-Type", "application/json")
	resC, err := http.DefaultClient.Do(reqC)
	if err != nil {
		t.Fatal(err)
	}
	defer resC.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(resC.Body).Decode(&body)
	if resC.StatusCode != 200 || body["ok"] != true {
		t.Fatalf("%d %v", resC.StatusCode, body)
	}

	gotCancel := false
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		_, msg, err := conn.ReadMessage()
		if err != nil {
			continue
		}
		var ev map[string]any
		_ = json.Unmarshal(msg, &ev)
		if ev["type"] == "assistant_done" && (ev["cancelled"] == true || ev["error"] == "cancelled") {
			gotCancel = true
			break
		}
	}
	if !gotCancel {
		t.Fatal("no cancelled assistant_done on WS")
	}
	job := reg.Get(jobID)
	if job == nil || job.Status != "cancelled" {
		t.Fatalf("job status=%v", job)
	}
}

func TestBridgeJobCancelRoute(t *testing.T) {
	secret := "bridge-cancel-sec"
	reg := bridge.NewJobRegistry()
	ts, _, _ := testServer(t, func(o *hub.Options) {
		o.BridgeSecret = secret
		o.JobRegistry = reg
	})
	job := reg.Create("s1", "hi", nil, "")
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/bridge/v1/jobs/"+job.ID+"/cancel", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(res.Body).Decode(&body)
	if res.StatusCode != 200 || body["ok"] != true {
		t.Fatalf("%d %v", res.StatusCode, body)
	}
	job = reg.Get(job.ID)
	if job.Status != "cancelled" {
		t.Fatal(job.Status)
	}
	// GET shows cancelled
	reqG, _ := http.NewRequest(http.MethodGet, ts.URL+"/bridge/v1/jobs/"+job.ID, nil)
	reqG.Header.Set("Authorization", "Bearer "+secret)
	resG, _ := http.DefaultClient.Do(reqG)
	defer resG.Body.Close()
	var pub map[string]any
	_ = json.NewDecoder(resG.Body).Decode(&pub)
	if pub["status"] != "cancelled" {
		t.Fatalf("%v", pub)
	}
}

func TestDemoAgentCancelViaAPI(t *testing.T) {
	slow := &agent.DemoAgent{Delay: 80 * time.Millisecond}
	ts, _, _ := testServer(t, func(o *hub.Options) {
		o.Agent = slow
	})
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/sessions", strings.NewReader(`{"title":"demo-cancel"}`))
	req.Header = authHeader()
	req.Header.Set("Content-Type", "application/json")
	res, _ := http.DefaultClient.Do(req)
	var sess map[string]any
	_ = json.NewDecoder(res.Body).Decode(&sess)
	res.Body.Close()
	sid := sess["id"].(string)

	u := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
	hdr := http.Header{}
	hdr.Set("Sec-WebSocket-Protocol", hub.WSBearerPrefix+testToken)
	conn, _, err := websocket.DefaultDialer.Dial(u, hdr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _, _ = conn.ReadMessage()
	_ = conn.WriteJSON(map[string]any{"type": "session.subscribe", "session_id": sid})
	_, _, _ = conn.ReadMessage()
	_ = conn.WriteJSON(map[string]any{"type": "chat.send", "session_id": sid, "content": "slow demo"})

	time.Sleep(50 * time.Millisecond)
	reqC, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/sessions/"+sid+"/cancel", strings.NewReader("{}"))
	reqC.Header = authHeader()
	reqC.Header.Set("Content-Type", "application/json")
	resC, _ := http.DefaultClient.Do(reqC)
	resC.Body.Close()
	if resC.StatusCode != 200 {
		t.Fatal(resC.Status)
	}

	got := false
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		_, msg, err := conn.ReadMessage()
		if err != nil {
			continue
		}
		var ev map[string]any
		_ = json.Unmarshal(msg, &ev)
		if ev["type"] == "assistant_done" && (ev["cancelled"] == true || ev["error"] == "cancelled") {
			got = true
			break
		}
	}
	if !got {
		t.Fatal("expected cancelled assistant_done")
	}
}

func TestBuildSessionsMerged(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_BRIDGE_GROK_HOME", home)
	t.Setenv("GROK_HOME", "")

	sid := "build-sess-1"
	dir := filepath.Join(home, "sessions", "proj", sid)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	summary := map[string]any{
		"info":              map[string]any{"id": sid, "cwd": "/proj"},
		"generated_title":   "From disk",
		"updated_at":        9999999999.0, // force to top of list
		"num_chat_messages": 2,
	}
	b, _ := json.Marshal(summary)
	_ = os.WriteFile(filepath.Join(dir, "summary.json"), b, 0o644)
	_ = os.WriteFile(filepath.Join(dir, "chat_history.jsonl"), []byte(
		`{"role":"user","content":"hello build"}`+"\n"+
			`{"role":"assistant","content":"hi from disk"}`+"\n",
	), 0o644)

	ts, _, _ := testServer(t, func(o *hub.Options) { o.SeedDemo = false })

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/sessions", nil)
	req.Header = authHeader()
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var body struct {
		Sessions []map[string]any `json:"sessions"`
	}
	_ = json.NewDecoder(res.Body).Decode(&body)
	if res.StatusCode != 200 {
		t.Fatal(res.Status)
	}
	var found map[string]any
	for _, s := range body.Sessions {
		if s["id"] == "build:"+sid {
			found = s
			break
		}
	}
	if found == nil {
		t.Fatalf("build session missing: %+v", body.Sessions)
	}
	if found["source"] != "build" || found["title"] != "From disk" {
		t.Fatalf("%+v", found)
	}

	req2, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/sessions/build%3A"+sid, nil)
	req2.Header = authHeader()
	res2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer res2.Body.Close()
	var sess map[string]any
	_ = json.NewDecoder(res2.Body).Decode(&sess)
	if res2.StatusCode != 200 {
		t.Fatalf("%d %v", res2.StatusCode, sess)
	}
	if sess["readonly"] == true {
		t.Fatalf("build sessions should not be readonly: %v", sess)
	}
	if sess["source"] != "build" {
		t.Fatalf("%v", sess)
	}
	msgs, _ := sess["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("msgs=%v", sess["messages"])
	}

	// Without ACP agent: clear 503 (not silent read-only 403)
	req3, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/sessions/build%3A"+sid+"/messages",
		strings.NewReader(`{"content":"nope"}`))
	req3.Header = authHeader()
	res3, _ := http.DefaultClient.Do(req3)
	defer res3.Body.Close()
	if res3.StatusCode == 403 {
		t.Fatal("build send must not return 403 read-only")
	}
	if res3.StatusCode != 503 && res3.StatusCode != 500 {
		body, _ := io.ReadAll(res3.Body)
		t.Fatalf("want 503/500 when agent missing, got %d %s", res3.StatusCode, body)
	}
}

func TestNoGrokHomeBridgeOnly(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent")
	t.Setenv("GROK_BRIDGE_GROK_HOME", missing)
	t.Setenv("GROK_HOME", missing)
	ts, _, _ := testServer(t, func(o *hub.Options) { o.SeedDemo = false })
	// Create one bridge session
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/sessions", strings.NewReader(`{"title":"only"}`))
	req.Header = authHeader()
	res, _ := http.DefaultClient.Do(req)
	res.Body.Close()

	req2, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/sessions", nil)
	req2.Header = authHeader()
	res2, _ := http.DefaultClient.Do(req2)
	defer res2.Body.Close()
	var body struct {
		Sessions []map[string]any `json:"sessions"`
	}
	_ = json.NewDecoder(res2.Body).Decode(&body)
	for _, s := range body.Sessions {
		if s["source"] == "build" {
			t.Fatalf("unexpected build session: %+v", s)
		}
		if s["source"] != "bridge" && s["source"] != nil && s["source"] != "" {
			t.Fatalf("unexpected source %+v", s)
		}
	}
	if len(body.Sessions) < 1 {
		t.Fatal("expected bridge session")
	}
}

// fakeBuildACP serves a minimal ACP WebSocket for hub integration tests.
type fakeBuildACP struct {
	up websocket.Upgrader
}

func (f *fakeBuildACP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/ws" {
		http.NotFound(w, r)
		return
	}
	conn, err := f.up.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var msg map[string]any
		if json.Unmarshal(data, &msg) != nil {
			continue
		}
		method, _ := msg["method"].(string)
		id := msg["id"]
		params, _ := msg["params"].(map[string]any)
		switch method {
		case "initialize":
			_ = conn.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"protocolVersion": 1}})
		case "session/load":
			_ = conn.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": id, "result": nil})
		case "session/prompt":
			sid, _ := params["sessionId"].(string)
			_ = conn.WriteJSON(map[string]any{
				"jsonrpc": "2.0", "method": "session/update",
				"params": map[string]any{
					"sessionId": sid,
					"update": map[string]any{
						"sessionUpdate": "agent_message_chunk",
						"content":       map[string]any{"type": "text", "text": "live-ok"},
					},
				},
			})
			_ = conn.WriteJSON(map[string]any{
				"jsonrpc": "2.0", "id": id,
				"result": map[string]any{"stopReason": "end_turn"},
			})
		case "session/cancel":
		default:
			if id != nil {
				_ = conn.WriteJSON(map[string]any{
					"jsonrpc": "2.0", "id": id,
					"error": map[string]any{"code": -32601, "message": "unknown"},
				})
			}
		}
	}
}

func TestBuildSessionLiveSendViaACP(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_BRIDGE_GROK_HOME", home)
	t.Setenv("GROK_HOME", "")

	sid := "live-build-1"
	dir := filepath.Join(home, "sessions", "proj", sid)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	summary := map[string]any{
		"info":            map[string]any{"id": sid, "cwd": "/proj"},
		"generated_title": "Live",
		"updated_at":      9999999999.0,
	}
	b, _ := json.Marshal(summary)
	_ = os.WriteFile(filepath.Join(dir, "summary.json"), b, 0o644)
	_ = os.WriteFile(filepath.Join(dir, "chat_history.jsonl"), []byte(
		`{"role":"user","content":"prior"}`+"\n",
	), 0o644)

	fake := &fakeBuildACP{up: websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}}
	acpSrv := httptest.NewServer(fake)
	t.Cleanup(acpSrv.Close)
	wsURL := "ws" + strings.TrimPrefix(acpSrv.URL, "http") + "/ws"

	mgr := grokacp.NewManager(grokacp.Config{WSURL: wsURL, Secret: "t", AutoStart: false})
	ts, _, _ := testServer(t, func(o *hub.Options) {
		o.SeedDemo = false
		o.ACP = mgr
	})

	// WS subscribe + send
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer "+testToken)
	wsURLHub := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURLHub, hdr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _, _ = conn.ReadMessage() // hello

	buildID := "build:" + sid
	_ = conn.WriteJSON(map[string]any{"type": "session.subscribe", "session_id": buildID})
	_ = conn.WriteJSON(map[string]any{"type": "chat.send", "session_id": buildID, "content": "ping"})

	deadline := time.Now().Add(5 * time.Second)
	sawDelta := false
	sawDone := false
	for time.Now().Before(deadline) && !(sawDelta && sawDone) {
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, data, err := conn.ReadMessage()
		if err != nil {
			break
		}
		var ev map[string]any
		if json.Unmarshal(data, &ev) != nil {
			continue
		}
		switch ev["type"] {
		case "assistant_delta":
			if ev["delta"] == "live-ok" {
				sawDelta = true
			}
		case "assistant_done":
			sawDone = true
		}
	}
	if !sawDelta || !sawDone {
		t.Fatalf("sawDelta=%v sawDone=%v", sawDelta, sawDone)
	}

	// HTTP POST also accepted (not 403)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/sessions/build%3A"+sid+"/messages",
		strings.NewReader(`{"content":"via http"}`))
	req.Header = authHeader()
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("POST messages: %d %s", res.StatusCode, body)
	}
}
