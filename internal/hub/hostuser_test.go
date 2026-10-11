package hub

import "testing"

func TestHostUserLabelPrefersEnv(t *testing.T) {
	t.Setenv("GROK_BRIDGE_USER_NAME", "Alice")
	t.Setenv("GROK_BRIDGE_SERVICE_USER", "root")
	if got := HostUserLabel(); got != "Alice" {
		t.Fatalf("got %q", got)
	}
}

func TestHostUserLabelServiceUser(t *testing.T) {
	t.Setenv("GROK_BRIDGE_USER_NAME", "")
	t.Setenv("GROK_BRIDGE_SERVICE_USER", "alice")
	if got := HostUserLabel(); got != "alice" {
		t.Fatalf("got %q", got)
	}
}

func TestStripLogin(t *testing.T) {
	if got := stripLogin("alice@host"); got != "alice" {
		t.Fatalf("at: %q", got)
	}
}
