package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	"grok-bridge/internal/auth"
	"grok-bridge/internal/bridge"
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

	cert, key := tlsutil.ResolvePaths(*sslCert, *sslKey, os.Getenv("GROK_BRIDGE_SSL_CERT"), os.Getenv("GROK_BRIDGE_SSL_KEY"))
	tlsCfg, err := tlsutil.Load(cert, key)
	if err != nil {
		log.Fatalf("TLS: %v", err)
	}

	root := *dataDir
	if root == "" {
		root = sessions.DefaultDataDir()
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
	ep := endpoint.PublicView(store.Root)
	if cfg, _ := ep["configured"].(bool); cfg {
		log.Printf("Phone URL (Tailscale MagicDNS/100.x): %v", ep["url"])
	} else {
		log.Printf("Phone URL: not yet in data/endpoint.json — run scripts/refresh-endpoint.sh after Tailscale is up")
	}
	log.Printf("Data dir: %s", store.Root)
	log.Printf("Web dir: %s", webDir)
	log.Printf("Restart: POST /api/control/restart (bearer) — prefer scripts/supervise.sh")

	handler := srv.Handler()
	if err := hub.ListenAndServe(addr, handler, tlsCfg); err != nil && err != http.ErrServerClosed {
		log.Fatalf("serve: %v", err)
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
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
