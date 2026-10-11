package grokacp

import (
	"net"
	"os"
	"runtime"
	"testing"
)

func TestListenerPIDFindsSelf(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("procfs only")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	addr := ln.Addr().String()
	if !PortListening(addr) {
		t.Fatal("expected listening")
	}
	if pid := ListenerPID(addr); pid != os.Getpid() {
		t.Fatalf("ListenerPID=%d want %d", pid, os.Getpid())
	}
	if IsOwnGrokAgent(os.Getpid()) {
		t.Fatal("test binary is not a grok agent")
	}
}

func TestStopAutoStartedNoop(t *testing.T) {
	if pid := StopAutoStarted(0); pid != 0 {
		t.Fatalf("want 0, got %d", pid)
	}
	if AgentPID() != 0 {
		t.Fatal("no agent expected")
	}
}
