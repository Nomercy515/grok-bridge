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
//  3. $HOME/.grok when that tree contains a sessions/summary.json
//  4. Walk parents of GROK_BRIDGE_DATA (then the process cwd) for
//     <parent>/.grok/sessions — so a root systemd unit still finds the
//     repo owner's ~/.grok instead of /root/.grok
//  5. filepath.Join(os.UserHomeDir(), ".grok") even if sessions/ is missing
//
// Returns "" only if no env override is set, UserHomeDir fails, and
// inference finds nothing.
func GrokHome() string {
	if v := strings.TrimSpace(os.Getenv("GROK_BRIDGE_GROK_HOME")); v != "" {
		return filepath.Clean(v)
	}
	if v := strings.TrimSpace(os.Getenv("GROK_HOME")); v != "" {
		return filepath.Clean(v)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		cand := filepath.Join(home, ".grok")
		if hasSummaryJSON(cand) {
			return cand
		}
	}
	if inferred := inferGrokHome(); inferred != "" {
		return inferred
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

func hasSessionsDir(grokHome string) bool {
	if grokHome == "" {
		return false
	}
	st, err := os.Stat(filepath.Join(grokHome, "sessions"))
	return err == nil && st.IsDir()
}

func hasSummaryJSON(grokHome string) bool {
	root := filepath.Join(grokHome, "sessions")
	st, err := os.Stat(root)
	if err != nil || !st.IsDir() {
		return false
	}
	found := false
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d == nil {
			return nil
		}
		if !d.IsDir() && d.Name() == "summary.json" {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

func inferGrokHome() string {
	var starts []string
	if v := strings.TrimSpace(os.Getenv("GROK_BRIDGE_DATA")); v != "" {
		starts = append(starts, v)
	}
	if wd, err := os.Getwd(); err == nil && wd != "" {
		starts = append(starts, wd)
	}
	for _, start := range starts {
		if found := walkParentsForGrok(start); found != "" {
			return found
		}
	}
	return ""
}

func walkParentsForGrok(start string) string {
	p := filepath.Clean(start)
	for i := 0; i < 16; i++ {
		if p == "" || p == string(filepath.Separator) {
			break
		}
		cand := filepath.Join(p, ".grok")
		if hasSessionsDir(cand) {
			return cand
		}
		next := filepath.Dir(p)
		if next == p {
			break
		}
		p = next
	}
	return ""
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
