package endpoint

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"

	"grok-bridge/internal/buildsessions"
)

const Filename = "endpoint.json"

func Path(dataRoot string) string {
	if dataRoot != "" {
		return filepath.Join(dataRoot, Filename)
	}
	if env := os.Getenv("GROK_BRIDGE_DATA"); env != "" {
		return filepath.Join(env, Filename)
	}
	return filepath.Join("data", Filename)
}

func Load(dataRoot string) map[string]any {
	b, err := os.ReadFile(Path(dataRoot))
	if err != nil {
		return nil
	}
	var data map[string]any
	if json.Unmarshal(b, &data) != nil {
		return nil
	}
	return data
}

// Runtime is live process identity merged into /api/endpoint so phones can
// tell which Tailscale hub they opened (host / listen / binary / Grok home).
type Runtime struct {
	Hostname   string
	ListenAddr string
	Version    string
	GOOS       string
	GOARCH     string
}

// PublicView returns the phone-facing endpoint bookmark fields only.
func PublicView(dataRoot string) map[string]any {
	return PublicViewWithRuntime(dataRoot, Runtime{})
}

// PublicViewWithRuntime merges bookmark fields with hub identity + Build probe.
func PublicViewWithRuntime(dataRoot string, rt Runtime) map[string]any {
	base := bookmarkView(dataRoot)
	attachRuntime(base, rt)
	attachBuild(base)
	return base
}

func bookmarkView(dataRoot string) map[string]any {
	ep := Load(dataRoot)
	if ep == nil {
		return map[string]any{
			"configured":     false,
			"source":         nil,
			"url":            nil,
			"url_ip":         nil,
			"magicdns":       nil,
			"tailscale_ipv4": nil,
			"port":           nil,
			"updated_at":     nil,
			"hint":           "Run scripts/refresh-endpoint.sh after Tailscale is up (or install via scripts/install.sh). Prefer one phone-facing hub per tailnet.",
		}
	}
	return map[string]any{
		"configured":     true,
		"source":         or(ep["source"], "tailscale"),
		"url":            ep["url"],
		"url_ip":         ep["url_ip"],
		"scheme":         ep["scheme"],
		"magicdns":       ep["magicdns"],
		"tailscale_ipv4": ep["tailscale_ipv4"],
		"port":           ep["port"],
		"updated_at":     ep["updated_at"],
		"hint":           "Bookmark url (MagicDNS). Scheme matches hub SSL. Host LAN DHCP IP is not the phone URL of record. One phone-facing hub — Build chats live only on that host's Grok home.",
	}
}

func attachRuntime(view map[string]any, rt Runtime) {
	host := rt.Hostname
	if host == "" {
		if h, err := os.Hostname(); err == nil {
			host = h
		}
	}
	goos := rt.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	goarch := rt.GOARCH
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	view["hostname"] = host
	if rt.ListenAddr != "" {
		view["listen_addr"] = rt.ListenAddr
	} else {
		view["listen_addr"] = nil
	}
	view["version"] = or(rt.Version, "")
	view["build_identity"] = map[string]any{
		"version": or(rt.Version, ""),
		"goos":    goos,
		"goarch":  goarch,
	}
}

func attachBuild(view map[string]any) {
	st := buildsessions.ProbeStatus()
	entry := map[string]any{
		"grok_home":     nullIfEmpty(st.GrokHome),
		"sessions_dir":  nullIfEmpty(st.SessionsDir),
		"available":     st.Available,
		"session_count": st.SessionCount,
		"reason":        st.Reason,
	}
	switch st.Reason {
	case "missing_home":
		entry["detail"] = "No Grok home resolved on this host. Build chats are local to GROK_BRIDGE_GROK_HOME / GROK_HOME / ~/.grok — you may be on the wrong Tailscale hub."
	case "missing_sessions_dir":
		entry["detail"] = "Grok home is set but sessions/ is missing on this host. Open the hub that runs where Grok Build stores ~/.grok."
	case "empty":
		entry["detail"] = "Grok home is readable but has no listable Build sessions yet."
	default:
		entry["detail"] = nil
	}
	view["build"] = entry
	view["grok_home"] = entry["grok_home"]
	view["build_session_count"] = st.SessionCount
	if !st.Available {
		view["build_session_count"] = nil // "unavailable" signal for clients
		view["build_sessions"] = "unavailable"
	} else {
		view["build_sessions"] = st.SessionCount
	}
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func or(v any, def string) any {
	if v == nil || v == "" {
		return def
	}
	return v
}
