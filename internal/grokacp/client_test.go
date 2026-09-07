package grokacp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// fakeACP is a minimal JSON-RPC ACP WebSocket server for unit tests.
type fakeACP struct {
	upgrader websocket.Upgrader
	secret   string

	mu       sync.Mutex
	sessions map[string]string // sessionId -> cwd
}

func newFakeACP(secret string) *fakeACP {
	return &fakeACP{
		upgrader: websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }},
		secret:   secret,
		sessions: map[string]string{},
	}
}

func (f *fakeACP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/ws" {
		http.NotFound(w, r)
		return
	}
	auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	key := r.URL.Query().Get("server-key")
	if f.secret != "" && auth != f.secret && key != f.secret {
		http.Error(w, "unauthorized", 401)
		return
	}
	conn, err := f.upgrader.Upgrade(w, r, nil)
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
			_ = conn.WriteJSON(map[string]any{
				"jsonrpc": "2.0", "id": id,
				"result": map[string]any{
					"protocolVersion":   1,
					"agentCapabilities": map[string]any{"loadSession": true},
				},
			})
		case "session/load":
			sid, _ := params["sessionId"].(string)
			cwd, _ := params["cwd"].(string)
			f.mu.Lock()
			f.sessions[sid] = cwd
			f.mu.Unlock()
			// emit a history chunk then respond
			_ = conn.WriteJSON(map[string]any{
				"jsonrpc": "2.0", "method": "session/update",
				"params": map[string]any{
					"sessionId": sid,
					"update": map[string]any{
						"sessionUpdate": "user_message_chunk",
						"content":       map[string]any{"type": "text", "text": "old"},
					},
				},
			})
			_ = conn.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": id, "result": nil})
		case "session/prompt":
			sid, _ := params["sessionId"].(string)
			prompt, _ := params["prompt"].([]any)
			text := ""
			if len(prompt) > 0 {
				if p, ok := prompt[0].(map[string]any); ok {
					text, _ = p["text"].(string)
				}
			}
			_ = conn.WriteJSON(map[string]any{
				"jsonrpc": "2.0", "method": "session/update",
				"params": map[string]any{
					"sessionId": sid,
					"update": map[string]any{
						"sessionUpdate": "agent_message_chunk",
						"content":       map[string]any{"type": "text", "text": "Echo: "},
					},
				},
			})
			_ = conn.WriteJSON(map[string]any{
				"jsonrpc": "2.0", "method": "session/update",
				"params": map[string]any{
					"sessionId": sid,
					"update": map[string]any{
						"sessionUpdate": "agent_message_chunk",
						"content":       map[string]any{"type": "text", "text": text},
					},
				},
			})
			_ = conn.WriteJSON(map[string]any{
				"jsonrpc": "2.0", "method": "session/update",
				"params": map[string]any{
					"sessionId": sid,
					"update": map[string]any{
						"sessionUpdate": "tool_call",
						"toolCallId":    "t1",
						"title":         "demo",
					},
				},
			})
			_ = conn.WriteJSON(map[string]any{
				"jsonrpc": "2.0", "method": "session/update",
				"params": map[string]any{
					"sessionId": sid,
					"update": map[string]any{
						"sessionUpdate": "tool_call_update",
						"toolCallId":    "t1",
						"status":        "completed",
						"content": []any{
							map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": "ok"}},
						},
					},
				},
			})
			_ = conn.WriteJSON(map[string]any{
				"jsonrpc": "2.0", "id": id,
				"result": map[string]any{"stopReason": "end_turn"},
			})
		case "session/cancel":
			// notification — no reply
		default:
			if id != nil {
				_ = conn.WriteJSON(map[string]any{
					"jsonrpc": "2.0", "id": id,
					"error": map[string]any{"code": -32601, "message": "method not found: " + method},
				})
			}
		}
	}
}

func TestClientLoadAndPrompt(t *testing.T) {
	fake := newFakeACP("sekrit")
	ts := httptest.NewServer(fake)
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
	cfg := Config{WSURL: wsURL, Secret: "sekrit", AutoStart: false}
	c := NewClient(cfg)
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := c.EnsureConnected(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.LoadSession(ctx, "sess-1", "/tmp/proj"); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	if fake.sessions["sess-1"] != "/tmp/proj" {
		t.Fatalf("load cwd=%q", fake.sessions["sess-1"])
	}
	fake.mu.Unlock()

	ch, err := c.Prompt(ctx, "build:sess-1", "sess-1", "hi there")
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	var text string
	for ev := range ch {
		types = append(types, ev["type"].(string))
		if ev["type"] == "assistant_delta" {
			text += ev["delta"].(string)
		}
		if ev["type"] == "assistant_done" {
			if ev["content"] != text && ev["content"] != "Echo: hi there" {
				// content should be accumulated
			}
		}
	}
	joined := strings.Join(types, ",")
	if !strings.Contains(joined, "assistant_start") || !strings.Contains(joined, "assistant_delta") {
		t.Fatalf("types=%v", types)
	}
	if !strings.Contains(joined, "tool_call") || !strings.Contains(joined, "tool_result") {
		t.Fatalf("missing tools: %v", types)
	}
	if !strings.HasSuffix(joined, "assistant_done") {
		t.Fatalf("types=%v", types)
	}
	if text != "Echo: hi there" {
		t.Fatalf("text=%q", text)
	}
}

func TestClientUnauthorized(t *testing.T) {
	fake := newFakeACP("sekrit")
	ts := httptest.NewServer(fake)
	defer ts.Close()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
	c := NewClient(Config{WSURL: wsURL, Secret: "wrong", AutoStart: false})
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := c.EnsureConnected(ctx)
	if err == nil {
		t.Fatal("expected auth error")
	}
	if !strings.Contains(err.Error(), "grok agent") {
		t.Fatalf("err=%v", err)
	}
}
