package grokacp

import (
	"context"
	"sync"
	"time"
)

// Manager owns a shared ACP client for the hub (lazy connect).
type Manager struct {
	mu     sync.Mutex
	client *Client
	cfg    Config
}

// NewManagerFromEnv builds a manager using ConfigFromEnv.
func NewManagerFromEnv() *Manager {
	return &Manager{cfg: ConfigFromEnv()}
}

// NewManager with an explicit config (tests).
func NewManager(cfg Config) *Manager {
	return &Manager{cfg: cfg}
}

// Client returns the shared client, creating it if needed.
func (m *Manager) Client() *Client {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.client == nil {
		m.client = NewClient(m.cfg)
	}
	return m.client
}

// SetClient injects a client (tests).
func (m *Manager) SetClient(c *Client) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.client = c
	if c != nil {
		m.cfg = c.cfg
	}
}

// EnsureConnected proxies to the shared client.
func (m *Manager) EnsureConnected(ctx context.Context) error {
	return m.Client().EnsureConnected(ctx)
}

// Close shuts down the shared client.
func (m *Manager) Close() error {
	m.mu.Lock()
	c := m.client
	m.client = nil
	m.mu.Unlock()
	if c != nil {
		return c.Close()
	}
	return nil
}

// Config returns the manager's agent config.
func (m *Manager) Config() Config {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg
}

// ResetResult describes one agent reset.
type ResetResult struct {
	StoppedPID int    `json:"stoppedPid,omitempty"`
	AgentPID   int    `json:"agentPid"`
	Connected  bool   `json:"connected"`
	Error      string `json:"error,omitempty"`
}

// Reset drops the shared ACP connection, stops the agent this hub auto-started
// (plus a stray `grok agent serve` on the bind port owned by this same user),
// relaunches it when auto-start is enabled, and reconnects.
func (m *Manager) Reset(ctx context.Context) ResetResult {
	cfg := m.Config()
	_ = m.Close()
	res := ResetResult{StoppedPID: StopAutoStarted(5 * time.Second)}
	if pid := ListenerPID(cfg.Bind); pid > 0 && pid != res.StoppedPID && IsOwnGrokAgent(pid) {
		if killPID(pid) == nil {
			res.StoppedPID = pid
		}
	}
	waitPortFree(cfg.Bind, 5*time.Second)
	if cfg.AutoStart {
		if err := AutoStart(ctx, cfg); err != nil {
			res.Error = err.Error()
		}
	}
	if err := m.EnsureConnected(ctx); err != nil {
		if res.Error == "" {
			res.Error = err.Error()
		}
	} else {
		res.Connected = true
	}
	res.AgentPID = AgentPID()
	if res.AgentPID == 0 {
		res.AgentPID = ListenerPID(cfg.Bind)
	}
	return res
}
