package restart

import (
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	FlagName      = "restart.requested"
	CooldownSec   = 5.0
	ExitDelaySec  = 0.4
)

type Controller struct {
	Root       string
	FlagPath   string
	OnRestart  func()
	ExitDelay  time.Duration
	Cooldown   time.Duration
	mu         sync.Mutex
	lastReq    time.Time
	scheduled  bool
}

func New(root string, onRestart func()) *Controller {
	return &Controller{
		Root:      root,
		FlagPath:  filepath.Join(root, FlagName),
		OnRestart: onRestart,
		ExitDelay: time.Duration(ExitDelaySec * float64(time.Second)),
		Cooldown:  time.Duration(CooldownSec * float64(time.Second)),
	}
}

func (c *Controller) ClearFlag() {
	_ = os.Remove(c.FlagPath)
}

func (c *Controller) WriteFlag() {
	_ = os.MkdirAll(c.Root, 0o755)
	_ = os.WriteFile(c.FlagPath, []byte(time.Now().Format(time.RFC3339Nano)), 0o644)
}

func (c *Controller) Request() map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if c.scheduled || now.Sub(c.lastReq) < c.Cooldown {
		return map[string]any{"ok": true, "action": "restart_already_scheduled"}
	}
	c.lastReq = now
	c.scheduled = true

	if c.OnRestart != nil {
		c.OnRestart()
		return map[string]any{"ok": true, "action": "restart_scheduled"}
	}

	c.WriteFlag()
	go func() {
		time.Sleep(c.ExitDelay)
		log.Printf("Restart scheduled — exiting hub process for supervisor re-exec")
		p, _ := os.FindProcess(os.Getpid())
		_ = p.Signal(os.Interrupt)
	}()
	return map[string]any{"ok": true, "action": "restart_scheduled"}
}

func ConsumeFlag(root string) bool {
	p := filepath.Join(root, FlagName)
	if _, err := os.Stat(p); err != nil {
		return false
	}
	_ = os.Remove(p)
	return true
}
