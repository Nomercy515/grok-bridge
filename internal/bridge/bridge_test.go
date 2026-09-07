package bridge

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestMockAgentStream(t *testing.T) {
	reg := NewJobRegistry()
	vb := NewAgentBridge("mock", "", "secret", 5*time.Second, reg)
	ch, err := vb.StreamReply(context.Background(), "sess1", "hello", nil)
	if err != nil {
		t.Fatal(err)
	}
	var done map[string]any
	var toolCall, toolResult map[string]any
	for e := range ch {
		switch e["type"] {
		case "assistant_done":
			done = e
		case "tool_call":
			toolCall = e
		case "tool_result":
			toolResult = e
		}
	}
	if done == nil {
		t.Fatal("no done")
	}
	content, _ := done["content"].(string)
	if !strings.Contains(content, "MockAgent") {
		t.Fatalf("content=%q", content)
	}
	if toolCall == nil || toolResult == nil {
		t.Fatal("expected tool_call and tool_result")
	}
	tc, _ := toolCall["tool"].(map[string]any)
	tr, _ := toolResult["tool"].(map[string]any)
	if tc["id"] == nil || tc["id"] == "" || tc["id"] != tr["id"] {
		t.Fatalf("tool ids mismatch call=%v result=%v", tc, tr)
	}
	if tc["status"] != "running" || tr["status"] != "done" {
		t.Fatalf("status call=%v result=%v", tc["status"], tr["status"])
	}
}

func TestCheckBridgeSecret(t *testing.T) {
	h := http.Header{}
	h.Set("Authorization", "Bearer good-secret")
	if !CheckBridgeSecret(h, "good-secret") {
		t.Fatal("bearer should match")
	}
	h2 := http.Header{}
	h2.Set("X-Bridge-Secret", "good-secret")
	if !CheckBridgeSecret(h2, "good-secret") {
		t.Fatal("x header should match")
	}
	if CheckBridgeSecret(http.Header{}, "good-secret") {
		t.Fatal("empty should fail")
	}
	if CheckBridgeSecret(h, "") {
		t.Fatal("empty expected should fail")
	}
}

func TestJobRegistryInbound(t *testing.T) {
	reg := NewJobRegistry()
	job := reg.Create("s1", "hi", nil, "")
	ok := reg.EnqueueEvent(job.ID, map[string]any{"type": "assistant_delta", "delta": "x"})
	if !ok {
		t.Fatal("enqueue")
	}
	content := "final"
	ok = reg.Complete(job.ID, &content, nil)
	if !ok || job.Status != "completed" {
		t.Fatalf("complete status=%s", job.Status)
	}
}

func TestResolveCallbackBase(t *testing.T) {
	t.Setenv("GROK_BRIDGE_CALLBACK_BASE", "https://box.ts.net:4020")
	v := NewAgentBridge("webhook", "http://example/hook", "secret", time.Second, NewJobRegistry())
	if got := v.ResolveCallbackBase(); got != "https://box.ts.net:4020" {
		t.Fatalf("got %q", got)
	}
}

func TestCancelUnblocksWebhookWait(t *testing.T) {
	reg := NewJobRegistry()
	vb := NewAgentBridge("webhook", "", "secret", 5*time.Second, reg)
	ctx := context.Background()
	ch, err := vb.StreamReply(ctx, "sess-w", "hi", nil)
	if err != nil {
		t.Fatal(err)
	}
	job := reg.ActiveJobForSession("sess-w")
	if job == nil {
		t.Fatal("expected active job")
	}
	go func() {
		time.Sleep(20 * time.Millisecond)
		if !reg.Cancel(job.ID) {
			t.Errorf("cancel returned false")
		}
	}()
	var done map[string]any
	deadline := time.After(2 * time.Second)
	for done == nil {
		select {
		case e, ok := <-ch:
			if !ok {
				goto ended
			}
			if e["type"] == "assistant_done" {
				done = e
			}
		case <-deadline:
			t.Fatal("timeout waiting for cancel done")
		}
	}
ended:
	if done == nil {
		t.Fatal("expected assistant_done after cancel")
	}
	if done["cancelled"] != true {
		t.Fatalf("expected cancelled=true, got %v", done)
	}
	job = reg.Get(job.ID)
	if job.Status != "cancelled" {
		t.Fatalf("status=%s", job.Status)
	}
	pub := job.ToPublic()
	if pub["status"] != "cancelled" {
		t.Fatalf("public status=%v", pub["status"])
	}
}

func TestCancelBySessionAndMockRespects(t *testing.T) {
	reg := NewJobRegistry()
	job := reg.Create("s-mock", "slow please", nil, "")
	mock := &MockAgent{Delay: 50 * time.Millisecond}
	go mock.Run(job)
	time.Sleep(40 * time.Millisecond)
	n := reg.CancelBySession("s-mock")
	if n != 1 {
		t.Fatalf("cancelled jobs=%d status=%s", n, job.Status)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if job.Status == "cancelled" {
			// mock should stop; no further panic / hang
			time.Sleep(80 * time.Millisecond)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("status=%s", job.Status)
}

func TestCancelViaContext(t *testing.T) {
	reg := NewJobRegistry()
	vb := NewAgentBridge("webhook", "", "secret", 5*time.Second, reg)
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := vb.StreamReply(ctx, "sess-ctx", "hello", nil)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(15 * time.Millisecond)
	cancel()
	var done map[string]any
	for e := range ch {
		if e["type"] == "assistant_done" {
			done = e
		}
	}
	if done == nil || done["cancelled"] != true {
		t.Fatalf("expected cancelled done, got %v", done)
	}
	reg.mu.RLock()
	var found *Job
	for _, j := range reg.jobs {
		if j.SessionID == "sess-ctx" {
			found = j
		}
	}
	reg.mu.RUnlock()
	if found == nil || found.Status != "cancelled" {
		t.Fatalf("job=%v", found)
	}
}

func TestCancelIdempotent(t *testing.T) {
	reg := NewJobRegistry()
	job := reg.Create("s1", "x", nil, "")
	if !reg.Cancel(job.ID) {
		t.Fatal("first cancel")
	}
	if !reg.Cancel(job.ID) {
		t.Fatal("second cancel should be idempotent true")
	}
	if job.Status != "cancelled" {
		t.Fatal(job.Status)
	}
	// completed job cannot cancel
	job2 := reg.Create("s2", "y", nil, "")
	c := "done"
	reg.Complete(job2.ID, &c, nil)
	if reg.Cancel(job2.ID) {
		t.Fatal("cancel on completed should be false")
	}
}
