package grokacp

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// The auto-started agent is tracked so /api/admin/reset can stop and relaunch
// it. It is deliberately NOT bound to the request context that triggered the
// start: a CommandContext tied to a chat turn killed the agent as soon as that
// turn ended, which dropped every loaded session.
var (
	procMu   sync.Mutex
	procCmd  *exec.Cmd
	procDone chan struct{}
)

// AgentPID returns the pid of the agent this process auto-started, or 0.
func AgentPID() int {
	procMu.Lock()
	defer procMu.Unlock()
	if procCmd == nil || procCmd.Process == nil {
		return 0
	}
	select {
	case <-procDone:
		return 0
	default:
	}
	return procCmd.Process.Pid
}

// StopAutoStarted kills the agent this process auto-started (if any) and waits
// up to timeout for it to exit. Returns the pid that was stopped (0 if none).
func StopAutoStarted(timeout time.Duration) int {
	procMu.Lock()
	cmd, done := procCmd, procDone
	procCmd, procDone = nil, nil
	procMu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return 0
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Kill()
	if done != nil {
		select {
		case <-done:
		case <-time.After(timeout):
		}
	}
	return pid
}

// AutoStart spawns `grok agent --always-approve --no-leader serve` if `grok` is on PATH.
// Never hardcodes WSL/Windows paths — relies on PATH lookup only.
func AutoStart(ctx context.Context, cfg Config) error {
	bin, err := exec.LookPath("grok")
	if err != nil {
		return fmt.Errorf("grok binary not found on PATH: %w", err)
	}
	secret := cfg.Secret
	if secret == "" {
		secret = "grok-bridge-autostart"
	}
	bind := cfg.Bind
	if bind == "" {
		bind = DefaultBind
	}
	args := []string{
		"agent", "--always-approve", "--no-leader",
		"serve", "--bind", bind, "--secret", secret,
	}
	// Replace a previously tracked agent that is no longer reachable.
	StopAutoStarted(3 * time.Second)
	cmd := exec.Command(bin, args...)
	cmd.Env = os.Environ()
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("spawn grok agent serve: %w", err)
	}
	done := make(chan struct{})
	procMu.Lock()
	procCmd, procDone = cmd, done
	procMu.Unlock()
	// Process keeps running for the hub lifetime (or until reset).
	go func() { _ = cmd.Wait(); close(done) }()

	probeCfg := cfg
	probeCfg.AutoStart = false
	if probeCfg.Secret == "" {
		probeCfg.Secret = secret
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
		probe := NewClient(probeCfg)
		pctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
		err := probe.EnsureConnected(pctx)
		cancel()
		_ = probe.Close()
		if err == nil {
			return nil
		}
	}
	return fmt.Errorf("grok agent serve started (%s) but not reachable at %s", strings.Join(args, " "), cfg.WSURL)
}
