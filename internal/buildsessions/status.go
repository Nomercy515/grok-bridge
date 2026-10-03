package buildsessions

import (
	"os"
	"path/filepath"
)

// Status describes whether this hub process can see Grok Build sessions.
// Build chats are always local to the process host (see docs/MULTI_HUB.md);
// Status makes that visible so a wrong Tailscale endpoint is not mistaken
// for a silent sync failure.
type Status struct {
	GrokHome     string `json:"grok_home"`
	SessionsDir  string `json:"sessions_dir,omitempty"`
	Available    bool   `json:"available"`
	SessionCount int    `json:"session_count"`
	Reason       string `json:"reason"` // ok | missing_home | missing_sessions_dir | empty
}

// ProbeStatus reports resolved Grok home and Build session visibility.
// SessionCount matches List() (Grok-only rows omitted unless env opts in).
func ProbeStatus() Status {
	home := GrokHome()
	if home == "" {
		return Status{
			Available:    false,
			SessionCount: 0,
			Reason:       "missing_home",
		}
	}
	root := filepath.Join(home, "sessions")
	st, err := os.Stat(root)
	if err != nil || !st.IsDir() {
		return Status{
			GrokHome:     home,
			SessionsDir:  root,
			Available:    false,
			SessionCount: 0,
			Reason:       "missing_sessions_dir",
		}
	}
	n := len(List())
	reason := "ok"
	if n == 0 {
		reason = "empty"
	}
	return Status{
		GrokHome:     home,
		SessionsDir:  root,
		Available:    true,
		SessionCount: n,
		Reason:       reason,
	}
}
