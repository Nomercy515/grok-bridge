package buildsessions

import (
	"os"
	"path/filepath"
	"strings"
)

// IDPrefix namespaces Build session IDs in the hub API so they never collide
// with hub-native UUIDs under data/sessions/.
const IDPrefix = "build:"

// GrokHome resolves the on-disk Grok home directory, environment-agnostic.
// Order:
//  1. GROK_BRIDGE_GROK_HOME
//  2. GROK_HOME
//  3. filepath.Join(os.UserHomeDir(), ".grok")
//
// Returns "" only if UserHomeDir fails and no env override is set.
func GrokHome() string {
	if v := strings.TrimSpace(os.Getenv("GROK_BRIDGE_GROK_HOME")); v != "" {
		return filepath.Clean(v)
	}
	if v := strings.TrimSpace(os.Getenv("GROK_HOME")); v != "" {
		return filepath.Clean(v)
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".grok")
}

func sessionsRoot() string {
	h := GrokHome()
	if h == "" {
		return ""
	}
	return filepath.Join(h, "sessions")
}

// StripPrefix returns the raw Build session id if s has the build: prefix,
// otherwise ("", false).
func StripPrefix(s string) (string, bool) {
	if strings.HasPrefix(s, IDPrefix) {
		raw := strings.TrimPrefix(s, IDPrefix)
		if raw == "" {
			return "", false
		}
		return raw, true
	}
	return "", false
}

// WithPrefix adds the build: API id prefix.
func WithPrefix(rawID string) string {
	if rawID == "" {
		return ""
	}
	if _, ok := StripPrefix(rawID); ok {
		return rawID
	}
	return IDPrefix + rawID
}
