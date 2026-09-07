package grokacp

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

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
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = os.Environ()
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("spawn grok agent serve: %w", err)
	}
	// Detach: don't wait; process keeps running for the hub lifetime.
	go func() { _ = cmd.Wait() }()

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
