package grokacp

import (
	"net/url"
	"os"
	"strings"
)

const (
	DefaultWSURL = "ws://127.0.0.1:2419/ws"
	DefaultBind  = "127.0.0.1:2419"
)

// Config is env-driven ACP agent connection settings (no OS/user hardcoding).
type Config struct {
	WSURL     string // full WebSocket URL including /ws
	Secret    string
	AutoStart bool
	Bind      string // used when auto-starting serve
}

// ConfigFromEnv reads GROK_BRIDGE_GROK_AGENT_* (and GROK_AGENT_SECRET fallback).
func ConfigFromEnv() Config {
	ws := strings.TrimSpace(os.Getenv("GROK_BRIDGE_GROK_AGENT_WS"))
	if ws == "" {
		ws = DefaultWSURL
	}
	ws = NormalizeWSURL(ws)

	secret := strings.TrimSpace(os.Getenv("GROK_BRIDGE_GROK_AGENT_SECRET"))
	if secret == "" {
		secret = strings.TrimSpace(os.Getenv("GROK_AGENT_SECRET"))
	}

	auto := envTruthy(os.Getenv("GROK_BRIDGE_GROK_AGENT_AUTO_START"))
	bind := strings.TrimSpace(os.Getenv("GROK_BRIDGE_GROK_AGENT_BIND"))
	if bind == "" {
		bind = DefaultBind
	}
	return Config{WSURL: ws, Secret: secret, AutoStart: auto, Bind: bind}
}

// NormalizeWSURL ensures a WebSocket URL ends with /ws (serve's upgrade path).
func NormalizeWSURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultWSURL
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		// bare host:port
		if !strings.Contains(raw, "://") {
			raw = "ws://" + raw
			u, err = url.Parse(raw)
		}
		if err != nil || u == nil {
			return DefaultWSURL
		}
	}
	switch u.Scheme {
	case "ws", "wss", "http", "https":
		if u.Scheme == "http" {
			u.Scheme = "ws"
		}
		if u.Scheme == "https" {
			u.Scheme = "wss"
		}
	default:
		u.Scheme = "ws"
	}
	path := strings.TrimSuffix(u.Path, "/")
	if path == "" || path == "/" {
		u.Path = "/ws"
	} else if !strings.HasSuffix(path, "/ws") {
		u.Path = path + "/ws"
	} else {
		u.Path = path
	}
	return u.String()
}

func envTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// DialURL returns the WS URL with server-key query when secret is set
// (browser-friendly auth; Authorization Bearer is also sent by the client).
func (c Config) DialURL() string {
	u, err := url.Parse(c.WSURL)
	if err != nil {
		return c.WSURL
	}
	if c.Secret != "" {
		q := u.Query()
		if q.Get("server-key") == "" {
			q.Set("server-key", c.Secret)
			u.RawQuery = q.Encode()
		}
	}
	return u.String()
}
