// Package agent defines the AgentAdapter extension point and DemoAgent.
package agent

import (
	"context"
	"strings"
	"time"
)

// Event is a JSON-serializable chat event for hub fan-out.
type Event map[string]any

// Adapter streams reply events for a user message.
type Adapter interface {
	StreamReply(ctx context.Context, sessionID, userMessage string, history []map[string]any) (<-chan Event, error)
}

// DemoAgent streams canned replies with a fake tool card.
type DemoAgent struct {
	Delay time.Duration
}

const (
	demoReply = "Got it. I'm the **DemoAgent** — no real model behind me. Here's a sample streamed answer about your request."
	toolName  = "demo_lookup"
)

func (d *DemoAgent) StreamReply(ctx context.Context, sessionID, userMessage string, history []map[string]any) (<-chan Event, error) {
	delay := d.Delay
	if delay == 0 {
		delay = 40 * time.Millisecond
	}
	ch := make(chan Event, 64)
	go func() {
		defer close(ch)
		cancelledDone := func(partial string) {
			ch <- Event{
				"type": "assistant_done", "session_id": sessionID,
				"content": partial, "cancelled": true, "error": "cancelled",
			}
		}
		var partial string
		send := func(e Event) bool {
			select {
			case <-ctx.Done():
				return false
			case ch <- e:
				return true
			}
		}
		if !send(Event{"type": "assistant_start", "session_id": sessionID}) {
			cancelledDone("")
			return
		}
		msg := strings.TrimSpace(userMessage)
		if msg == "" {
			msg = "(empty)"
		}
		text := "You said: “" + msg + "”.\n\n" + demoReply
		for _, word := range strings.Split(text, " ") {
			delta := word + " "
			if !send(Event{"type": "assistant_delta", "session_id": sessionID, "delta": delta}) {
				cancelledDone(partial)
				return
			}
			partial += delta
			select {
			case <-ctx.Done():
				cancelledDone(partial)
				return
			case <-time.After(delay):
			}
		}
		toolID := "demo-lookup-1"
		if !send(Event{
			"type": "tool_call", "session_id": sessionID,
			"tool": map[string]any{
				"id": toolID, "name": toolName,
				"arguments": map[string]any{"query": "sample", "limit": 3},
				"status":    "running",
			},
		}) {
			cancelledDone(partial)
			return
		}
		select {
		case <-ctx.Done():
			cancelledDone(partial)
			return
		case <-time.After(delay * 2):
		}
		if !send(Event{
			"type": "tool_result", "session_id": sessionID,
			"tool": map[string]any{
				"id": toolID, "name": toolName,
				"result": map[string]any{"items": []any{"alpha", "beta", "gamma"}, "source": "demo"},
				"status": "done",
			},
		}) {
			cancelledDone(partial)
			return
		}
		select {
		case <-ctx.Done():
			cancelledDone(partial)
			return
		case <-time.After(delay):
		}
		closing := " Demo complete — wire a real AgentAdapter when you're ready."
		if !send(Event{"type": "assistant_delta", "session_id": sessionID, "delta": closing}) {
			cancelledDone(partial)
			return
		}
		partial += closing
		send(Event{"type": "assistant_done", "session_id": sessionID, "content": text + closing})
	}()
	return ch, nil
}
