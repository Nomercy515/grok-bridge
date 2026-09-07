package grokacp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

var (
	ErrNotConnected = errors.New("grok agent not connected")
	ErrClosed       = errors.New("grok agent connection closed")
)

// Client is a JSON-RPC 2.0 ACP client over WebSocket to `grok agent serve`.
type Client struct {
	cfg Config

	mu       sync.Mutex
	conn     *websocket.Conn
	nextID   atomic.Int64
	pending  map[int64]chan rpcReply
	subs     map[string][]chan map[string]any // rawACPSessionID -> listeners
	writeMu  sync.Mutex
	initOnce sync.Once
	initErr  error
	closed   bool

	loadedMu sync.Mutex
	loaded   map[string]string // rawSessionID -> cwd used at load
}

type rpcReply struct {
	result json.RawMessage
	err    *rpcError
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	if e == nil {
		return ""
	}
	if len(e.Data) > 0 {
		return fmt.Sprintf("%s (%s)", e.Message, string(e.Data))
	}
	return e.Message
}

// NewClient creates a disconnected client. Call EnsureConnected before use.
func NewClient(cfg Config) *Client {
	if cfg.WSURL == "" {
		cfg = ConfigFromEnv()
	}
	return &Client{
		cfg:     cfg,
		pending: make(map[int64]chan rpcReply),
		subs:    make(map[string][]chan map[string]any),
		loaded:  make(map[string]string),
	}
}

// Config returns a copy of the client config.
func (c *Client) Config() Config { return c.cfg }

// EnsureConnected dials the agent (and optionally auto-starts it) then runs initialize.
func (c *Client) EnsureConnected(ctx context.Context) error {
	c.mu.Lock()
	if c.conn != nil && !c.closed {
		c.mu.Unlock()
		return c.waitInit(ctx)
	}
	c.mu.Unlock()

	if err := c.dial(ctx); err != nil {
		if c.cfg.AutoStart {
			if startErr := AutoStart(ctx, c.cfg); startErr != nil {
				return fmt.Errorf("%w; auto-start failed: %v", err, startErr)
			}
			// brief settle then retry
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(400 * time.Millisecond):
			}
			if err2 := c.dial(ctx); err2 != nil {
				return fmt.Errorf("%w (after auto-start)", err2)
			}
		} else {
			return err
		}
	}
	return c.waitInit(ctx)
}

func (c *Client) waitInit(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		c.initOnce.Do(func() {
			c.initErr = c.doInitialize(context.Background())
		})
		close(done)
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return c.initErr
	}
}

func (c *Client) dial(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil && !c.closed {
		return nil
	}
	dialURL := c.cfg.DialURL()
	hdr := http.Header{}
	if c.cfg.Secret != "" {
		hdr.Set("Authorization", "Bearer "+c.cfg.Secret)
	}
	d := websocket.Dialer{HandshakeTimeout: 8 * time.Second}
	conn, _, err := d.DialContext(ctx, dialURL, hdr)
	if err != nil {
		return fmt.Errorf("cannot reach grok agent at %s: %w — start with: grok agent --always-approve --no-leader serve --bind %s --secret <token>",
			c.cfg.WSURL, err, c.cfg.Bind)
	}
	c.conn = conn
	c.closed = false
	c.pending = make(map[int64]chan rpcReply)
	c.initOnce = sync.Once{}
	c.initErr = nil
	go c.readLoop()
	return nil
}

func (c *Client) doInitialize(ctx context.Context) error {
	params := map[string]any{
		"protocolVersion": 1,
		"clientCapabilities": map[string]any{
			"fs":       map[string]any{"readTextFile": false, "writeTextFile": false},
			"terminal": false,
		},
		"clientInfo": map[string]any{
			"name":    "grok-bridge",
			"version": "0.1.0",
		},
	}
	_, err := c.request(ctx, "initialize", params)
	return err
}

// LoadSession resumes an existing Build session (raw UUID, not build: prefix).
func (c *Client) LoadSession(ctx context.Context, rawSessionID, cwd string) error {
	if err := c.EnsureConnected(ctx); err != nil {
		return err
	}
	c.loadedMu.Lock()
	prev, ok := c.loaded[rawSessionID]
	c.loadedMu.Unlock()
	if ok && prev == cwd {
		return nil
	}
	params := map[string]any{
		"sessionId":  rawSessionID,
		"cwd":        cwd,
		"mcpServers": []any{},
	}
	// Drain history updates during load so they are not mistaken for a live turn.
	drain, unsub := c.subscribe(rawSessionID)
	defer unsub()
	go func() {
		for range drain {
		}
	}()
	_, err := c.request(ctx, "session/load", params)
	if err != nil {
		return fmt.Errorf("session/load: %w", err)
	}
	c.loadedMu.Lock()
	c.loaded[rawSessionID] = cwd
	c.loadedMu.Unlock()
	return nil
}

// Prompt sends session/prompt and returns a channel of Bridge events until the turn ends.
func (c *Client) Prompt(ctx context.Context, bridgeSessionID, rawSessionID, text string) (<-chan BridgeEvent, error) {
	if err := c.EnsureConnected(ctx); err != nil {
		return nil, err
	}
	out := make(chan BridgeEvent, 64)
	rawCh, unsub := c.subscribe(rawSessionID)

	go func() {
		defer close(out)
		defer unsub()

		out <- BridgeEvent{"type": "assistant_start", "session_id": bridgeSessionID}

		params := map[string]any{
			"sessionId": rawSessionID,
			"prompt":    []map[string]any{{"type": "text", "text": text}},
		}
		type reqResult struct {
			raw json.RawMessage
			err error
		}
		resCh := make(chan reqResult, 1)
		go func() {
			raw, err := c.request(ctx, "session/prompt", params)
			resCh <- reqResult{raw, err}
		}()

		var fullText string
		cancelled := false
		done := false

		for !done {
			select {
			case <-ctx.Done():
				cancelled = true
				_ = c.Cancel(rawSessionID)
				done = true
			case upd, ok := <-rawCh:
				if !ok {
					done = true
					break
				}
				for _, ev := range MapUpdate(bridgeSessionID, upd) {
					if d, ok := ev["delta"].(string); ok {
						fullText += d
					}
					select {
					case out <- ev:
					case <-ctx.Done():
						cancelled = true
						_ = c.Cancel(rawSessionID)
						done = true
					}
				}
			case rr := <-resCh:
				if rr.err != nil {
					if ctx.Err() != nil || errors.Is(rr.err, context.Canceled) {
						cancelled = true
					} else {
						out <- BridgeEvent{
							"type": "assistant_done", "session_id": bridgeSessionID,
							"content": fullText, "error": rr.err.Error(),
						}
						return
					}
				} else {
					var result struct {
						StopReason string `json:"stopReason"`
					}
					_ = json.Unmarshal(rr.raw, &result)
					if StopReasonCancelled(result.StopReason) {
						cancelled = true
					}
				}
				done = true
			}
		}

		// Drain any remaining updates briefly
		deadline := time.After(50 * time.Millisecond)
	drain:
		for {
			select {
			case upd, ok := <-rawCh:
				if !ok {
					break drain
				}
				for _, ev := range MapUpdate(bridgeSessionID, upd) {
					if d, ok := ev["delta"].(string); ok {
						fullText += d
					}
					select {
					case out <- ev:
					default:
					}
				}
			case <-deadline:
				break drain
			}
		}

		doneEv := BridgeEvent{
			"type": "assistant_done", "session_id": bridgeSessionID,
			"content": fullText,
		}
		if cancelled {
			doneEv["cancelled"] = true
			doneEv["error"] = "cancelled"
			if fullText == "" {
				doneEv["content"] = "(cancelled)"
			}
		}
		out <- doneEv
	}()
	return out, nil
}

// Cancel notifies the agent to stop the current turn (ACP notification, no reply).
func (c *Client) Cancel(rawSessionID string) error {
	c.mu.Lock()
	conn := c.conn
	closed := c.closed
	c.mu.Unlock()
	if conn == nil || closed {
		return ErrNotConnected
	}
	msg := map[string]any{
		"jsonrpc": "2.0",
		"method":  "session/cancel",
		"params":  map[string]any{"sessionId": rawSessionID},
	}
	return c.writeJSON(msg)
}

// Close shuts down the WebSocket.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	if c.conn != nil {
		err := c.conn.Close()
		c.conn = nil
		return err
	}
	return nil
}

func (c *Client) subscribe(rawSessionID string) (<-chan map[string]any, func()) {
	ch := make(chan map[string]any, 128)
	c.mu.Lock()
	c.subs[rawSessionID] = append(c.subs[rawSessionID], ch)
	c.mu.Unlock()
	unsub := func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		list := c.subs[rawSessionID]
		for i, s := range list {
			if s == ch {
				c.subs[rawSessionID] = append(list[:i], list[i+1:]...)
				break
			}
		}
		close(ch)
	}
	return ch, unsub
}

func (c *Client) publishUpdate(rawSessionID string, update map[string]any) {
	c.mu.Lock()
	list := append([]chan map[string]any{}, c.subs[rawSessionID]...)
	c.mu.Unlock()
	for _, ch := range list {
		select {
		case ch <- update:
		default:
			// drop if slow consumer
		}
	}
}

func (c *Client) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := c.nextID.Add(1)
	ch := make(chan rpcReply, 1)
	c.mu.Lock()
	if c.conn == nil || c.closed {
		c.mu.Unlock()
		return nil, ErrNotConnected
	}
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	msg := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}
	if err := c.writeJSON(msg); err != nil {
		return nil, err
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case rep := <-ch:
		if rep.err != nil {
			return nil, rep.err
		}
		return rep.result, nil
	}
}

func (c *Client) writeJSON(v any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return ErrNotConnected
	}
	_ = conn.SetWriteDeadline(time.Now().Add(15 * time.Second))
	return conn.WriteJSON(v)
}

func (c *Client) readLoop() {
	for {
		c.mu.Lock()
		conn := c.conn
		c.mu.Unlock()
		if conn == nil {
			return
		}
		_, data, err := conn.ReadMessage()
		if err != nil {
			c.failAll(err)
			return
		}
		var msg map[string]any
		if json.Unmarshal(data, &msg) != nil {
			continue
		}
		// Notification
		if method, _ := msg["method"].(string); method != "" && msg["id"] == nil {
			c.handleNotification(method, msg["params"])
			continue
		}
		// Response
		id := jsonID(msg["id"])
		if id == 0 {
			continue
		}
		c.mu.Lock()
		ch := c.pending[id]
		c.mu.Unlock()
		if ch == nil {
			continue
		}
		var rep rpcReply
		if errObj, ok := msg["error"].(map[string]any); ok {
			rep.err = &rpcError{
				Code:    intFromAny(errObj["code"]),
				Message: strAny(errObj["message"]),
			}
			if d, ok := errObj["data"]; ok {
				rep.err.Data, _ = json.Marshal(d)
			}
		} else if r, ok := msg["result"]; ok {
			rep.result, _ = json.Marshal(r)
		} else {
			rep.result = json.RawMessage("null")
		}
		select {
		case ch <- rep:
		default:
		}
	}
}

func (c *Client) handleNotification(method string, params any) {
	pm, _ := params.(map[string]any)
	if pm == nil {
		return
	}
	switch method {
	case "session/update":
		sid := strAny(pm["sessionId"])
		if sid == "" {
			sid = strAny(pm["session_id"])
		}
		upd, _ := pm["update"].(map[string]any)
		if upd == nil {
			return
		}
		c.publishUpdate(sid, upd)
	default:
		// ignore _x.ai/* and other extensions
	}
}

func (c *Client) failAll(err error) {
	c.mu.Lock()
	c.closed = true
	c.conn = nil
	pending := c.pending
	c.pending = make(map[int64]chan rpcReply)
	subs := c.subs
	c.subs = make(map[string][]chan map[string]any)
	c.mu.Unlock()

	c.loadedMu.Lock()
	c.loaded = make(map[string]string)
	c.loadedMu.Unlock()

	rpcErr := &rpcError{Code: -32000, Message: err.Error()}
	for _, ch := range pending {
		select {
		case ch <- rpcReply{err: rpcErr}:
		default:
		}
	}
	// Do not close subscriber chans here — unsub() owns close (avoids double-close).
	_ = subs
	log.Printf("grokacp: connection closed: %v", err)
}

func jsonID(v any) int64 {
	switch t := v.(type) {
	case float64:
		return int64(t)
	case int64:
		return t
	case int:
		return int64(t)
	case json.Number:
		n, _ := t.Int64()
		return n
	default:
		return 0
	}
}

func intFromAny(v any) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	case int64:
		return int(t)
	case json.Number:
		n, _ := t.Int64()
		return int(n)
	default:
		return 0
	}
}
