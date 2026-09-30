package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"grok-bridge/internal/auth"
	"grok-bridge/internal/bridge"
	"grok-bridge/internal/buildsessions"
	"grok-bridge/internal/endpoint"
	"grok-bridge/internal/hub"
	"grok-bridge/internal/restart"
	"grok-bridge/internal/sessions"
	"grok-bridge/internal/tlsutil"
)

func main() {
	host := flag.String("host", envOr("GROK_BRIDGE_HOST", hub.DefaultHost), "Bind address")
	port := flag.Int("port", envInt("GROK_BRIDGE_PORT", hub.DefaultPort), "Listen port")
	dataDir := flag.String("data-dir", "", "Session + auth data directory")
	sslCert := flag.String("ssl-cert", "", "PEM certificate path")
	sslKey := flag.String("ssl-key", "", "PEM private key path")
	verbose := flag.Bool("v", false, "Verbose logging")
	flag.Parse()

	if *verbose {
		log.SetFlags(log.LstdFlags | log.Lshortfile)
	}

	// Resolve data dir and apply data/grok-bridge.env before TLS so
	// GROK_BRIDGE_SSL_CERT/KEY take effect on restart.requested even when the
	// supervisor process was started without those vars in its environment.
	root := *dataDir
	if root == "" {
		root = sessions.DefaultDataDir()
	}
	loadProjectEnv(root)

	cert, key := tlsutil.ResolvePaths(*sslCert, *sslKey, os.Getenv("GROK_BRIDGE_SSL_CERT"), os.Getenv("GROK_BRIDGE_SSL_KEY"))
	tlsCfg, err := tlsutil.Load(cert, key)
	if err != nil {
		log.Fatalf("TLS: %v", err)
	}

	store, err := sessions.NewStore(root)
	if err != nil {
		log.Fatalf("data dir: %v", err)
	}
	authStore, err := auth.NewStore(store.Root)
	if err != nil {
		log.Fatalf("auth: %v", err)
	}
	rst := restart.New(store.Root, nil)
	rst.ClearFlag()

	jobReg := bridge.NewJobRegistry()
	bridgeSecret := os.Getenv("GROK_BRIDGE_SECRET")
	agentKind := strings.ToLower(strings.TrimSpace(envOr("GROK_BRIDGE_AGENT", "demo")))
	ag, err := bridge.BuildAgentFromEnv(jobReg, bridgeSecret)
	if err != nil {
		log.Fatalf("%v", err)
	}

	webDir := hub.ResolveWebDir()
	webFS := os.DirFS(webDir)

	srv, err := hub.NewServer(hub.Options{
		Store: store, Agent: ag, Auth: authStore, SeedDemo: true,
		Restart: rst, BridgeSecret: bridgeSecret, JobRegistry: jobReg,
		WebFS: webFS,
	})
	if err != nil {
		log.Fatalf("server: %v", err)
	}

	scheme := "http"
	if tlsCfg != nil {
		scheme = "https"
	}
	addr := fmt.Sprintf("%s:%d", *host, *port)
	log.Printf("Grok Bridge listening on %s://%s  (demo: %s://127.0.0.1:%d/?demo=1)", scheme, addr, scheme, *port)
	log.Printf("Agent backend: %s", agentKind)
	if agentKind == "valentine" || agentKind == "bot" || agentKind == "agent" {
		mode := envPrefer("GROK_BRIDGE_AGENT_MODE", "GROK_BRIDGE_VALENTINE_MODE", "mock")
		log.Printf("agent bridge mode: %s", mode)
	}
	if *host == "0.0.0.0" || *host == "::" {
		if tlsCfg == nil {
			log.Printf("WARNING: bound to %s without TLS — prefer Tailscale + HTTPS", *host)
		} else {
			log.Printf("LAN bind with TLS enabled — still do not port-forward to the public internet")
		}
	} else {
		log.Printf("Localhost-safe bind (%s). For phone: Tailscale MagicDNS/IP + TLS", *host)
	}
	if tlsCfg != nil {
		log.Printf("TLS enabled (cert=%s)", cert)
	}
	log.Printf("Pairing code: %s  (POST /api/auth/pair with {\"code\": \"...\"})", authStore.PairingCode())
	srv.ListenAddr = addr
	maybeRefreshEndpoint(store.Root)
	hostname, _ := os.Hostname()
	ep := endpoint.PublicViewWithRuntime(store.Root, endpoint.Runtime{
		Hostname: hostname, ListenAddr: addr, Version: hub.Version,
	})
	if cfg, _ := ep["configured"].(bool); cfg {
		log.Printf("Phone URL (Tailscale MagicDNS/100.x): %v", ep["url"])
	} else {
		log.Printf("Phone URL: not yet in data/endpoint.json — run scripts/refresh-endpoint.sh after Tailscale is up")
	}
	log.Printf("Hub identity: host=%v listen=%s version=%s", ep["hostname"], addr, hub.Version)
	logBuildStatus()
	log.Printf("Data dir: %s", store.Root)
	log.Printf("Web dir: %s", webDir)
	log.Printf("Restart: POST /api/control/restart (bearer) — prefer scripts/supervise.sh")

	handler := srv.Handler()
	if err := hub.ListenAndServe(addr, handler, tlsCfg); err != nil && err != http.ErrServerClosed {
		log.Fatalf("serve: %v", err)
	}
}

// loadProjectEnv applies KEY=VAL from data/grok-bridge.env when the key is unset.
// Lets GROK_BRIDGE_USER_NAME / GROK_BRIDGE_GROK_HOME / GROK_BRIDGE_SSL_* take
// effect on restart.requested even if systemd still runs the unit as root.
func loadProjectEnv(dataDir string) {
	b, err := os.ReadFile(filepath.Join(dataDir, "grok-bridge.env"))
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if k == "" || os.Getenv(k) != "" {
			continue
		}
		_ = os.Setenv(k, v)
	}
}

func envPrefer(primary, fallback, def string) string {
	if v := os.Getenv(primary); v != "" {
		return v
	}
	if v := os.Getenv(fallback); v != "" {
		return v
	}
	return def
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	v := strings.TrimSpace(os.Getenv(k))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func logBuildStatus() {
	st := buildsessions.ProbeStatus()
	switch st.Reason {
	case "ok":
		log.Printf("Grok Build: %d session(s) under %s", st.SessionCount, st.GrokHome)
	case "empty":
		log.Printf("Grok Build: home %s has sessions/ but no listable chats yet", st.GrokHome)
	case "missing_sessions_dir":
		log.Printf("WARNING: Grok home %s has no sessions/ — Build chats will be hidden on this hub (wrong Tailscale endpoint? see docs/MULTI_HUB.md)", st.GrokHome)
	default:
		log.Printf("WARNING: no Grok home resolved — Build chats unavailable on this hub (docs/MULTI_HUB.md)")
	}
}

// maybeRefreshEndpoint best-effort updates data/endpoint.json so the phone URL
// matches this process host. Never fails hub startup (Tailscale may be down).
func maybeRefreshEndpoint(dataDir string) {
	root := findRepoRoot()
	if root == "" {
		return
	}
	script := filepath.Join(root, "scripts", "refresh-endpoint.sh")
	if st, err := os.Stat(script); err != nil || st.IsDir() {
		return
	}
	cmd := exec.Command(script, "--no-wait", "--no-regen-certs")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GROK_BRIDGE_DATA="+dataDir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("endpoint refresh skipped: %v (%s)", err, strings.TrimSpace(string(out)))
		return
	}
	log.Printf("endpoint.json refreshed for this hub")
}

func findRepoRoot() string {
	var candidates []string
	if v := os.Getenv("GROK_BRIDGE_ROOT"); v != "" {
		candidates = append(candidates, v)
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, wd)
	}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Dir(exe), filepath.Join(filepath.Dir(exe), ".."))
	}
	for _, c := range candidates {
		p := filepath.Clean(c)
		if _, err := os.Stat(filepath.Join(p, "scripts", "refresh-endpoint.sh")); err == nil {
			return p
		}
	}
	return ""
}
