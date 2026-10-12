// Package selfcheck verifies the runtime the Bridge needs for Grok Build:
// the grok binary, grok auth for this user, the sessions dir, the agent port,
// and that the hub does not run as root (root resolves ~/.grok to /root/.grok,
// which has neither the user's login nor sessions -> FS_NOT_FOUND on load).
package selfcheck

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"grok-bridge/internal/buildsessions"
	"grok-bridge/internal/grokacp"
)

// Check is one self-check line.
type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
	Fatal  bool   `json:"fatal,omitempty"`
}

// AuthProbe runs `grok models` (or equivalent) and reports login state.
type AuthProbe func(ctx context.Context, bin string) (ok bool, detail string)

// Options tune Run; zero values use the live environment.
type Options struct {
	Bind     string    // agent bind host:port (default from env)
	AgentPID int       // pid the hub auto-started, if any
	Auth     AuthProbe // default: DefaultAuthProbe
}

// Run executes all checks. It never fails hard; callers decide on Fatal.
func Run(ctx context.Context, o Options) []Check {
	if o.Bind == "" {
		o.Bind = grokacp.ConfigFromEnv().Bind
	}
	if o.Auth == nil {
		o.Auth = DefaultAuthProbe
	}
	var out []Check

	bin, err := exec.LookPath("grok")
	if err != nil {
		out = append(out, Check{Name: "grok_binary", OK: false, Fatal: true,
			Detail: "grok not on PATH (expected ~/.grok/bin/grok); PATH=" + os.Getenv("PATH")})
	} else {
		out = append(out, Check{Name: "grok_binary", OK: true, Detail: bin})
		ok, detail := o.Auth(ctx, bin)
		out = append(out, Check{Name: "grok_auth", OK: ok, Detail: detail})
	}

	home := buildsessions.GrokHome()
	sess := filepath.Join(home, "sessions")
	if st, err := os.Stat(sess); home != "" && err == nil && st.IsDir() {
		out = append(out, Check{Name: "sessions_dir", OK: true, Detail: sess})
	} else {
		out = append(out, Check{Name: "sessions_dir", OK: false,
			Detail: fmt.Sprintf("sessions dir missing: %q (set GROK_BRIDGE_GROK_HOME)", sess)})
	}

	out = append(out, portCheck(o.Bind, o.AgentPID))
	out = append(out, userCheck(home))
	return out
}

// DefaultAuthProbe runs `grok models` with a timeout and looks for the login line.
func DefaultAuthProbe(ctx context.Context, bin string) (bool, string) {
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	b, err := exec.CommandContext(cctx, bin, "models").CombinedOutput()
	s := strings.ToLower(string(b))
	switch {
	case strings.Contains(s, "not authenticated"):
		return false, "grok is not logged in for this user — run: grok login"
	case strings.Contains(s, "logged in"):
		return true, "logged in"
	case err != nil:
		return false, "grok models failed: " + err.Error()
	default:
		return false, "could not confirm grok login"
	}
}

func portCheck(bind string, agentPID int) Check {
	c := Check{Name: "agent_port"}
	if !grokacp.PortListening(bind) {
		c.OK, c.Detail = true, bind+" free (agent starts on demand)"
		return c
	}
	pid := grokacp.ListenerPID(bind)
	switch {
	case pid > 0 && pid == agentPID:
		c.OK, c.Detail = true, fmt.Sprintf("%s owned by our agent pid %d", bind, pid)
	case pid > 0 && grokacp.ProcessUser(pid) == os.Getuid():
		c.OK, c.Detail = true, fmt.Sprintf("%s held by pid %d (same user)", bind, pid)
	case pid > 0:
		c.OK, c.Detail = false, fmt.Sprintf("%s held by pid %d of uid %d — stop it (stale root agent?)", bind, pid, grokacp.ProcessUser(pid))
	default:
		c.OK, c.Detail = false, bind+" held by a process of another user (stale root agent?) — stop it"
	}
	return c
}

func userCheck(grokHome string) Check {
	c := Check{Name: "run_user"}
	name := os.Getenv("USER")
	if u, err := user.Current(); err == nil {
		name = u.Username
	}
	home, _ := os.UserHomeDir()
	c.Detail = fmt.Sprintf("user=%s HOME=%s grok_home=%s", name, home, grokHome)
	if os.Getuid() == 0 {
		c.Detail += " — running as root: ~/.grok resolves to /root/.grok (no login/sessions)"
		return c
	}
	if grokHome != "" && home != "" && !strings.HasPrefix(grokHome, home+string(os.PathSeparator)) {
		c.Detail += " — grok home is outside HOME; agent may use a different ~/.grok"
		return c
	}
	c.OK = true
	return c
}

// Failed reports whether any check is a fatal failure.
func Failed(cs []Check) bool {
	for _, c := range cs {
		if c.Fatal && !c.OK {
			return true
		}
	}
	return false
}
