package hub

import "testing"

func TestHostUserLabelPrefersEnv(t *testing.T) {
	t.Setenv("GROK_BRIDGE_USER_NAME", "Etienne")
	t.Setenv("GROK_BRIDGE_SERVICE_USER", "root")
	if got := HostUserLabel(); got != "Etienne" {
		t.Fatalf("got %q", got)
	}
}

func TestHostUserLabelServiceUser(t *testing.T) {
	t.Setenv("GROK_BRIDGE_USER_NAME", "")
	t.Setenv("GROK_BRIDGE_SERVICE_USER", "eti_enne1")
	if got := HostUserLabel(); got != "eti_enne1" {
		t.Fatalf("got %q", got)
	}
}

func TestStripLogin(t *testing.T) {
	if got := stripLogin("eti_enne1@host"); got != "eti_enne1" {
		t.Fatalf("at: %q", got)
	}
}
