// Package bridge implements AgentBridge + MockAgent + job registry (Phase 1–4).
package bridge

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"grok-bridge/internal/agent"
)

const DefaultTimeout = 120 * time.Second

type Job struct {
	ID          string           `json:"id"`
	SessionID   string           `json:"session_id"`
	UserMessage string           `json:"user_message"`
	HistoryRef  string           `json:"history_ref"`
	History     []map[string]any `json:"history"`
	Status      string           `json:"status"`
	CreatedAt   float64          `json:"created_at"`
	CompletedAt *float64         `json:"completed_at"`
	Error       *string          `json:"error"`
	queue       chan agent.Event
	done        chan struct{}
	doneOnce    sync.Once
}

func (j *Job) ToPublic() map[string]any {
	return map[string]any{
		"id": j.ID, "session_id": j.SessionID, "user_message": j.UserMessage,
		"history_ref": j.HistoryRef, "history": j.History, "status": j.Status,
		"created_at": j.CreatedAt, "completed_at": j.CompletedAt, "error": j.Error,
	}
}

func (j *Job) markDone() {
	j.doneOnce.Do(func() { close(j.done) })
}

type JobRegistry struct {
	mu   sync.RWMutex
	jobs map[string]*Job
}

func NewJobRegistry() *JobRegistry {
	return &JobRegistry{jobs: make(map[string]*Job)}
}

func (r *JobRegistry) Create(sessionID, userMessage string, history []map[string]any, jobID string) *Job {
	r.mu.Lock()
	defer r.mu.Unlock()
	if jobID == "" {
		jobID = uuid.NewString()
	}
	hist := history
	if hist == nil {
		hist = []map[string]any{}
	}
	j := &Job{
		ID: jobID, SessionID: sessionID, UserMessage: userMessage,
		HistoryRef: fmt.Sprintf("session:%s:n=%d", sessionID, len(hist)),
		History:    hist, Status: "pending",
		CreatedAt: float64(time.Now().UnixNano()) / 1e9,
		queue:     make(chan agent.Event, 128),
		done:      make(chan struct{}),
	}
	r.jobs[jobID] = j
	return j
}

func (r *JobRegistry) Get(jobID string) *Job {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.jobs[jobID]
}

func (r *JobRegistry) EnqueueEvent(jobID string, event agent.Event) bool {
	r.mu.Lock()
	job := r.jobs[jobID]
	r.mu.Unlock()
	if job == nil {
		return false
	}
	if jobTerminal(job.Status) {
		return false
	}
	job.Status = "running"
	if _, ok := event["session_id"]; !ok {
		event["session_id"] = job.SessionID
	}
	select {
	case job.queue <- event:
	default:
		return false
	}
	if event["type"] == "assistant_done" {
		now := float64(time.Now().UnixNano()) / 1e9
		job.Status = "completed"
		job.CompletedAt = &now
		job.markDone()
	}
	return true
}

func (r *JobRegistry) Complete(jobID string, content, errMsg *string) bool {
	r.mu.Lock()
	job := r.jobs[jobID]
	r.mu.Unlock()
	if job == nil {
		return false
	}
	if job.Status == "completed" || job.Status == "cancelled" {
		return true
	}
	now := float64(time.Now().UnixNano()) / 1e9
	evt := agent.Event{"type": "assistant_done", "session_id": job.SessionID}
	if errMsg != nil && *errMsg != "" {
		job.Status = "error"
		job.Error = errMsg
		job.CompletedAt = &now
		c := "(bridge error: " + *errMsg + ")"
		if content != nil {
			c = *content
		}
		evt["content"] = c
		evt["error"] = *errMsg
	} else {
		job.Status = "completed"
		job.CompletedAt = &now
		if content != nil {
			evt["content"] = *content
		} else {
			evt["content"] = ""
		}
	}
	select {
	case job.queue <- evt:
	default:
	}
	job.markDone()
	return true
}

// terminalStatuses are jobs that no longer accept events.
func jobTerminal(status string) bool {
	switch status {
	case "completed", "cancelled", "timeout", "error":
		return true
	default:
		return false
	}
}

// Cancel marks the job cancelled, unblocks StreamReply waiters, and pushes
// an assistant_done event with cancelled=true. Idempotent.
func (r *JobRegistry) Cancel(jobID string) bool {
	r.mu.Lock()
	job := r.jobs[jobID]
	r.mu.Unlock()
	if job == nil {
		return false
	}
	if job.Status == "cancelled" {
		return true
	}
	if jobTerminal(job.Status) {
		return false
	}
	now := float64(time.Now().UnixNano()) / 1e9
	errMsg := "cancelled"
	job.Status = "cancelled"
	job.Error = &errMsg
	job.CompletedAt = &now
	evt := agent.Event{
		"type": "assistant_done", "session_id": job.SessionID,
		"content": "", "cancelled": true, "error": "cancelled",
	}
	select {
	case job.queue <- evt:
	default:
	}
	job.markDone()
	return true
}

// CancelBySession cancels all pending/running jobs for a session.
func (r *JobRegistry) CancelBySession(sessionID string) int {
	r.mu.RLock()
	var ids []string
	for id, j := range r.jobs {
		if j.SessionID == sessionID && !jobTerminal(j.Status) {
			ids = append(ids, id)
		}
	}
	r.mu.RUnlock()
	n := 0
	for _, id := range ids {
		if r.Cancel(id) {
			n++
		}
	}
	return n
}

// ActiveJobForSession returns the newest non-terminal job for a session, if any.
func (r *JobRegistry) ActiveJobForSession(sessionID string) *Job {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var best *Job
	for _, j := range r.jobs {
		if j.SessionID != sessionID || jobTerminal(j.Status) {
			continue
		}
		if best == nil || j.CreatedAt > best.CreatedAt {
			best = j
		}
	}
	return best
}

type MockAgent struct {
	Delay    time.Duration
	ToolName string
}

func (m *MockAgent) push(job *Job, evt agent.Event) bool {
	select {
	case <-job.done:
		return false
	default:
	}
	select {
	case <-job.done:
		return false
	case job.queue <- evt:
		return true
	}
}

func (m *MockAgent) sleepOrCancel(job *Job, d time.Duration) bool {
	if d <= 0 {
		select {
		case <-job.done:
			return false
		default:
			return true
		}
	}
	select {
	case <-job.done:
		return false
	case <-time.After(d):
		return true
	}
}

func (m *MockAgent) Run(job *Job) {
	if m.ToolName == "" {
		m.ToolName = "mock_agent_echo"
	}
	if jobTerminal(job.Status) {
		return
	}
	job.Status = "running"
	sid := job.SessionID
	if !m.push(job, agent.Event{"type": "assistant_start", "session_id": sid}) {
		return
	}
	if !m.sleepOrCancel(job, m.Delay) {
		return
	}
	msg := strings.TrimSpace(job.UserMessage)
	if msg == "" {
		msg = "(empty)"
	}
	text := "MockAgent here. You said: “" + msg + "”."
	for _, word := range strings.Split(text, " ") {
		if !m.push(job, agent.Event{"type": "assistant_delta", "session_id": sid, "delta": word + " "}) {
			return
		}
		if !m.sleepOrCancel(job, m.Delay) {
			return
		}
	}
	toolID := "mock-" + job.ID
	if len(job.ID) > 8 {
		toolID = "mock-" + job.ID[:8]
	}
	if !m.push(job, agent.Event{
		"type": "tool_call", "session_id": sid,
		"tool": map[string]any{
			"id": toolID, "name": m.ToolName,
			"arguments": map[string]any{
				"echo": job.UserMessage, "phase": 3,
			},
			"status": "running",
		},
	}) {
		return
	}
	if !m.sleepOrCancel(job, m.Delay) {
		return
	}
	if !m.push(job, agent.Event{
		"type": "tool_result", "session_id": sid,
		"tool": map[string]any{
			"id": toolID, "name": m.ToolName,
			"result": map[string]any{
				"ok": true, "echoed": job.UserMessage, "cards": "phase3",
			},
			"status": "done",
		},
	}) {
		return
	}
	closing := " Phase 3 mock complete — tool cards with id."
	if !m.push(job, agent.Event{"type": "assistant_delta", "session_id": sid, "delta": closing}) {
		return
	}
	full := text + closing
	if !m.push(job, agent.Event{"type": "assistant_done", "session_id": sid, "content": full}) {
		return
	}
	now := float64(time.Now().UnixNano()) / 1e9
	job.Status = "completed"
	job.CompletedAt = &now
	job.markDone()
}

type AgentBridge struct {
	Mode         string
	WebhookURL   string
	BridgeSecret string
	WebhookAuth  string // Bearer for outbound Cursor webhook (sender key); falls back to BridgeSecret
	CallbackBase string // hub public base URL (MagicDNS), no trailing slash
	DataRoot     string // for endpoint.json lookup when CallbackBase unset
	Timeout      time.Duration
	Registry     *JobRegistry
}

func NewAgentBridge(mode, webhookURL, secret string, timeout time.Duration, reg *JobRegistry) *AgentBridge {
	if mode == "" {
		mode = envPrefer("GROK_BRIDGE_AGENT_MODE", "GROK_BRIDGE_VALENTINE_MODE", "mock")
	}
	if webhookURL == "" {
		webhookURL = envPrefer("GROK_BRIDGE_AGENT_WEBHOOK_URL", "GROK_BRIDGE_VALENTINE_WEBHOOK_URL", "")
	}
	if secret == "" {
		secret = os.Getenv("GROK_BRIDGE_SECRET")
	}
	if timeout == 0 {
		if t := envPrefer("GROK_BRIDGE_AGENT_TIMEOUT", "GROK_BRIDGE_VALENTINE_TIMEOUT", ""); t != "" {
			if d, err := time.ParseDuration(t + "s"); err == nil {
				timeout = d
			} else if d, err := time.ParseDuration(t); err == nil {
				timeout = d
			}
		}
		if timeout == 0 {
			timeout = DefaultTimeout
		}
	}
	if reg == nil {
		reg = NewJobRegistry()
	}
	webhookAuth := strings.TrimSpace(envPrefer("GROK_BRIDGE_AGENT_WEBHOOK_AUTH", "GROK_BRIDGE_VALENTINE_WEBHOOK_AUTH", ""))
	if webhookAuth == "" {
		webhookAuth = secret // local relay may reuse bridge secret; live Cursor needs sender key
	}
	// Accept a pasted "Authorization: Bearer <key>" or "Bearer <key>" value.
	if i := strings.LastIndex(strings.ToLower(webhookAuth), "bearer "); i >= 0 {
		webhookAuth = strings.TrimSpace(webhookAuth[i+len("bearer "):])
	}
	v := &AgentBridge{
		Mode:       strings.ToLower(strings.TrimSpace(mode)),
		WebhookURL: webhookURL, BridgeSecret: secret, WebhookAuth: webhookAuth,
		CallbackBase: strings.TrimRight(strings.TrimSpace(os.Getenv("GROK_BRIDGE_CALLBACK_BASE")), "/"),
		DataRoot:     os.Getenv("GROK_BRIDGE_DATA"),
		Timeout:      timeout, Registry: reg,
	}
	return v
}

// ResolveCallbackBase returns the hub public base URL the agent should POST events to.
func (v *AgentBridge) ResolveCallbackBase() string {
	if v.CallbackBase != "" {
		return v.CallbackBase
	}
	if env := strings.TrimRight(strings.TrimSpace(os.Getenv("GROK_BRIDGE_CALLBACK_BASE")), "/"); env != "" {
		return env
	}
	// Prefer Tailscale MagicDNS URL from endpoint.json
	root := v.DataRoot
	if root == "" {
		root = os.Getenv("GROK_BRIDGE_DATA")
	}
	if root == "" {
		root = "data"
	}
	path := root + "/endpoint.json"
	b, err := os.ReadFile(path)
	if err == nil {
		var ep map[string]any
		if json.Unmarshal(b, &ep) == nil {
			if u, ok := ep["url"].(string); ok {
				u = strings.TrimRight(strings.TrimSpace(u), "/")
				if u != "" {
					return u
				}
			}
		}
	}
	host := envOr("GROK_BRIDGE_HOST", "127.0.0.1")
	port := envOr("GROK_BRIDGE_PORT", "4020")
	scheme := "http"
	if os.Getenv("GROK_BRIDGE_SSL_CERT") != "" {
		scheme = "https"
	}
	if host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return fmt.Sprintf("%s://%s:%s", scheme, host, port)
}

func (v *AgentBridge) StreamReply(ctx context.Context, sessionID, userMessage string, history []map[string]any) (<-chan agent.Event, error) {
	job := v.Registry.Create(sessionID, userMessage, history, "")
	log.Printf("bridge job created id=%s mode=%s session=%s", job.ID, v.Mode, sessionID)
	out := make(chan agent.Event, 128)

	go func() {
		defer close(out)
		switch v.Mode {
		case "mock":
			go (&MockAgent{}).Run(job)
		case "webhook":
			go v.dispatchWebhook(job)
		default:
			job.Status = "error"
			errMsg := fmt.Sprintf("unknown GROK_BRIDGE_AGENT_MODE=%q", v.Mode)
			job.Error = &errMsg
			out <- agent.Event{"type": "assistant_start", "session_id": sessionID}
			out <- agent.Event{"type": "assistant_done", "session_id": sessionID,
				"content": "(bridge misconfigured: " + errMsg + ")", "error": errMsg}
			return
		}

		deadline := time.Now().Add(v.Timeout)
		emitCancel := func() {
			// Prefer registry cancel so GET job shows cancelled; forward its event if queued.
			v.Registry.Cancel(job.ID)
			select {
			case evt := <-job.queue:
				out <- evt
			default:
				out <- agent.Event{
					"type": "assistant_done", "session_id": sessionID,
					"content": "", "cancelled": true, "error": "cancelled",
				}
			}
		}
		for {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				if !jobTerminal(job.Status) {
					job.Status = "timeout"
					now := float64(time.Now().UnixNano()) / 1e9
					job.CompletedAt = &now
					errMsg := "bridge timeout waiting for agent events"
					job.Error = &errMsg
					job.markDone()
					out <- agent.Event{"type": "assistant_done", "session_id": sessionID,
						"content": "(agent bridge timed out)", "error": errMsg}
				}
				return
			}
			select {
			case <-ctx.Done():
				if !jobTerminal(job.Status) {
					emitCancel()
				} else if job.Status == "cancelled" {
					// drain cancel event if still buffered
					select {
					case evt := <-job.queue:
						out <- evt
					default:
						out <- agent.Event{
							"type": "assistant_done", "session_id": sessionID,
							"content": "", "cancelled": true, "error": "cancelled",
						}
					}
				}
				return
			case evt, ok := <-job.queue:
				if !ok {
					return
				}
				out <- evt
				if evt["type"] == "assistant_done" {
					return
				}
			case <-time.After(remaining):
				if !jobTerminal(job.Status) {
					job.Status = "timeout"
					now := float64(time.Now().UnixNano()) / 1e9
					job.CompletedAt = &now
					errMsg := "bridge timeout waiting for agent events"
					job.Error = &errMsg
					job.markDone()
					out <- agent.Event{"type": "assistant_done", "session_id": sessionID,
						"content": "(agent bridge timed out)", "error": errMsg}
				}
				return
			}
		}
	}()
	return out, nil
}

func (v *AgentBridge) dispatchWebhook(job *Job) {
	url := strings.TrimSpace(v.WebhookURL)
	if url == "" {
		log.Printf("webhook mode but GROK_BRIDGE_AGENT_WEBHOOK_URL unset; waiting for inbound only job=%s", job.ID)
		return
	}
	base := v.ResolveCallbackBase()
	eventsPath := "/bridge/v1/jobs/" + job.ID + "/events"
	completePath := "/bridge/v1/jobs/" + job.ID + "/complete"
	statusPath := "/bridge/v1/jobs/" + job.ID
	cancelPath := "/bridge/v1/jobs/" + job.ID + "/cancel"
	payload := map[string]any{
		"job_id": job.ID, "session_id": job.SessionID,
		"user_message": job.UserMessage, "history_ref": job.HistoryRef,
		"history":       job.History,
		"callback_base": base,
		"callback_hint": eventsPath,
		"events_url":    strings.TrimRight(base, "/") + eventsPath,
		"complete_url":  strings.TrimRight(base, "/") + completePath,
		"status_url":    strings.TrimRight(base, "/") + statusPath,
		"cancel_url":    strings.TrimRight(base, "/") + cancelPath,
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		log.Printf("webhook POST error job=%s: %v", job.ID, err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if v.WebhookAuth != "" {
		req.Header.Set("Authorization", "Bearer "+v.WebhookAuth)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("webhook POST error job=%s: %v", job.ID, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		log.Printf("webhook POST failed status=%d job=%s", resp.StatusCode, job.ID)
	} else {
		log.Printf("webhook POST ok job=%s status=%d", job.ID, resp.StatusCode)
		job.Status = "running"
	}
}

func BuildAgentFromEnv(reg *JobRegistry, bridgeSecret string) (agent.Adapter, error) {
	kind := strings.ToLower(strings.TrimSpace(envOr("GROK_BRIDGE_AGENT", "demo")))
	switch kind {
	case "", "demo":
		return &agent.DemoAgent{}, nil
	case "bot", "agent", "valentine":
		secret := bridgeSecret
		if secret == "" {
			secret = os.Getenv("GROK_BRIDGE_SECRET")
		}
		if secret == "" {
			return nil, fmt.Errorf("GROK_BRIDGE_SECRET is required when GROK_BRIDGE_AGENT=%s", kind)
		}
		return NewAgentBridge(
			envPrefer("GROK_BRIDGE_AGENT_MODE", "GROK_BRIDGE_VALENTINE_MODE", ""),
			envPrefer("GROK_BRIDGE_AGENT_WEBHOOK_URL", "GROK_BRIDGE_VALENTINE_WEBHOOK_URL", ""),
			secret, 0, reg,
		), nil
	default:
		return nil, fmt.Errorf("unknown GROK_BRIDGE_AGENT=%q (use demo|bot|agent)", kind)
	}
}
func CheckBridgeSecret(headers http.Header, expected string) bool {
	if expected == "" {
		return false
	}
	auth := headers.Get("Authorization")
	xSecret := headers.Get("X-Bridge-Secret")
	var presented string
	if len(auth) > 7 && strings.EqualFold(auth[:7], "Bearer ") {
		presented = strings.TrimSpace(auth[7:])
	}
	if presented != "" && subtle.ConstantTimeCompare([]byte(presented), []byte(expected)) == 1 {
		return true
	}
	if xSecret != "" && subtle.ConstantTimeCompare([]byte(xSecret), []byte(expected)) == 1 {
		return true
	}
	return false
}

// envPrefer returns primary env if set, else fallback, else def.
func envPrefer(primary, fallback, def string) string {
	if v := os.Getenv(primary); v != "" {
		return v
	}
	if v := os.Getenv(fallback); v != "" {
		return v
	}
	return def
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
