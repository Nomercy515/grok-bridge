package hub

import "testing"

func TestHostUserLabelPrefersEnv(t *testing.T) {
	t.Setenv("GROK_BRIDGE_USER_NAME", "<name>")
	t.Setenv("GROK_BRIDGE_SERVICE_USER", "root")
	if got := HostUserLabel(); got != "<name>" {
		t.Fatalf("got %q", got)
	}
}

func TestHostUserLabelServiceUser(t *testing.T) {
	t.Setenv("GROK_BRIDGE_USER_NAME", "")
	t.Setenv("GROK_BRIDGE_SERVICE_USER", "<user>")
	if got := HostUserLabel(); got != "<user>" {
		t.Fatalf("got %q", got)
	}
}

func TestStripLogin(t *testing.T) {
	if got := stripLogin("<user>@host"); got != "<user>" {
		t.Fatalf("at: %q", got)
	}
}
