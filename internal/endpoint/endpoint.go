package endpoint

import (
	"encoding/json"
	"os"
	"path/filepath"
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

func PublicView(dataRoot string) map[string]any {
	ep := Load(dataRoot)
	if ep == nil {
		return map[string]any{
			"configured":      false,
			"source":          nil,
			"url":             nil,
			"url_ip":          nil,
			"magicdns":        nil,
			"tailscale_ipv4":  nil,
			"port":            nil,
			"updated_at":      nil,
			"hint":            "Run scripts/refresh-endpoint.sh after Tailscale is up (or install via scripts/install.sh).",
		}
	}
	return map[string]any{
		"configured":     true,
		"source":         or(ep["source"], "tailscale"),
		"url":            ep["url"],
		"url_ip":         ep["url_ip"],
		"magicdns":       ep["magicdns"],
		"tailscale_ipv4": ep["tailscale_ipv4"],
		"port":           ep["port"],
		"updated_at":     ep["updated_at"],
		"hint":           "Bookmark url (MagicDNS). Host LAN DHCP IP is not the phone URL of record.",
	}
}

func or(v any, def string) any {
	if v == nil || v == "" {
		return def
	}
	return v
}
