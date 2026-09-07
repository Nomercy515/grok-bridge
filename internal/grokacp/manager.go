package grokacp

import (
	"context"
	"sync"
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
