package hub

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"grok-bridge/internal/agent"
	"grok-bridge/internal/auth"
	"grok-bridge/internal/bridge"
	"grok-bridge/internal/buildsessions"
	"grok-bridge/internal/endpoint"
	"grok-bridge/internal/grokacp"
	"grok-bridge/internal/grokusage"
	"grok-bridge/internal/restart"
	"grok-bridge/internal/sessions"
)

const (
	Version        = "0.1.0"
	DefaultHost    = "127.0.0.1"
	DefaultPort    = 4020
	WSBearerPrefix = "grok.bearer."
)

var upgrader = websocket.Upgrader{
	CheckOrigin:  func(r *http.Request) bool { return true },
	Subprotocols: []string{},
}

type Hub struct {
	Store   *sessions.Store
	Agent   agent.Adapter
	Auth    *auth.Store
	clients map[*websocket.Conn]map[string]struct{}
	mu      sync.Mutex
	locks   map[string]*sync.Mutex
}

func NewHub(store *sessions.Store, ag agent.Adapter, a *auth.Store) *Hub {
	return &Hub{
		Store: store, Agent: ag, Auth: a,
		clients: make(map[*websocket.Conn]map[string]struct{}),
		locks:   make(map[string]*sync.Mutex),
	}
}

func (h *Hub) lockFor(sid string) *sync.Mutex {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.locks[sid] == nil {
		h.locks[sid] = &sync.Mutex{}
	}
	return h.locks[sid]
}

func (h *Hub) Broadcast(event map[string]any, sessionID string, exclude *websocket.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	sid := sessionID
	if sid == "" {
		if v, ok := event["session_id"].(string); ok {
			sid = v
		}
	}
	raw, _ := json.Marshal(event)
	var dead []*websocket.Conn
	for ws, subs := range h.clients {
		if ws == exclude {
			continue
		}
		if sid != "" && len(subs) > 0 {
			if _, ok := subs[sid]; !ok {
				continue
			}
		}
		_ = ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if err := ws.WriteMessage(websocket.TextMessage, raw); err != nil {
			dead = append(dead, ws)
		}
	}
	for _, ws := range dead {
		delete(h.clients, ws)
		_ = ws.Close()
	}
}

type Options struct {
	Store        *sessions.Store
	Agent        agent.Adapter
	Auth         *auth.Store
	SeedDemo     bool
	Restart      *restart.Controller
	BridgeSecret string
	JobRegistry  *bridge.JobRegistry
	WebFS        fs.FS
	ACP          *grokacp.Manager
}

type Server struct {
	Hub          *Hub
	Restart      *restart.Controller
	PairLimiter  *auth.PairRateLimiter
	JobRegistry  *bridge.JobRegistry
	BridgeSecret string
	WebFS        fs.FS
	Mux          *http.ServeMux
	ACP          *grokacp.Manager
	Usage        *grokusage.Cache
	turnsMu      sync.Mutex
	turns        map[string]context.CancelFunc // sessionID -> cancel active turn
	buildRawMu   sync.Mutex
	buildRaw     map[string]string // bridge build:id -> raw ACP session UUID (active turn)
	createdMu    sync.Mutex
	createdBuild map[string]*sessions.Session // build:id overlay until disk appears
	createdCwd   map[string]string            // raw or build:id -> cwd from session/new
}

func NewServer(opts Options) (*Server, error) {
	store := opts.Store
	if store == nil {
		var err error
		store, err = sessions.NewStore("")
		if err != nil {
			return nil, err
		}
	}
	a := opts.Auth
	if a == nil {
		var err error
		a, err = auth.NewStore(store.Root)
		if err != nil {
			return nil, err
		}
	}
	rst := opts.Restart
	if rst == nil {
		rst = restart.New(store.Root, func() {})
	}
	secret := opts.BridgeSecret
	if secret == "" {
		secret = os.Getenv("GROK_BRIDGE_SECRET")
	}
	reg := opts.JobRegistry
	if reg == nil {
		if vb, ok := opts.Agent.(*bridge.AgentBridge); ok {
			reg = vb.Registry
		} else {
			reg = bridge.NewJobRegistry()
		}
	}
	ag := opts.Agent
	if ag == nil {
		var err error
		ag, err = bridge.BuildAgentFromEnv(reg, secret)
		if err != nil {
			ag = &agent.DemoAgent{}
		}
	}
	if vb, ok := ag.(*bridge.AgentBridge); ok {
		reg = vb.Registry
		if vb.BridgeSecret != "" && secret == "" {
			secret = vb.BridgeSecret
		}
	}
	h := NewHub(store, ag, a)
	if opts.SeedDemo {
		_, _ = store.EnsureDemoSessions()
	}
	acpMgr := opts.ACP
	if acpMgr == nil {
		acpMgr = grokacp.NewManagerFromEnv()
	}
	s := &Server{
		Hub: h, Restart: rst,
		PairLimiter:  auth.NewPairRateLimiter(5, 60),
		JobRegistry:  reg,
		BridgeSecret: secret,
		WebFS:        opts.WebFS,
		Mux:          http.NewServeMux(),
		ACP:          acpMgr,
		Usage:        grokusage.NewCache(),
		turns:        make(map[string]context.CancelFunc),
		buildRaw:     make(map[string]string),
		createdBuild: make(map[string]*sessions.Session),
		createdCwd:   make(map[string]string),
	}
	s.routes()
	return s, nil
}

func (s *Server) Handler() http.Handler {
	return s.authMiddleware(s.Mux)
}

func redactToken(token string) string {
	if token == "" {
		return "(none)"
	}
	if len(token) <= 8 {
		return "***"
	}
	return token[:4] + "…" + token[len(token)-2:]
}

func tokenFromWSProtocols(header string) string {
	for _, part := range strings.Split(header, ",") {
		proto := strings.TrimSpace(part)
		if strings.HasPrefix(proto, WSBearerPrefix) {
			return proto[len(WSBearerPrefix):]
		}
		if strings.HasPrefix(proto, "bearer.") && !strings.HasPrefix(proto, WSBearerPrefix) {
			return proto[len("bearer."):]
		}
	}
	return ""
}

func allowQueryToken() bool {
	return os.Getenv("GROK_BRIDGE_ALLOW_QUERY_TOKEN") == "1"
}

func extractToken(r *http.Request) string {
	authz := r.Header.Get("Authorization")
	if len(authz) > 7 && strings.EqualFold(authz[:7], "Bearer ") {
		return strings.TrimSpace(authz[7:])
	}
	if t := tokenFromWSProtocols(r.Header.Get("Sec-WebSocket-Protocol")); t != "" {
		return t
	}
	if allowQueryToken() {
		return r.URL.Query().Get("token")
	}
	return ""
}

func selectedWSProtocol(r *http.Request) string {
	for _, part := range strings.Split(r.Header.Get("Sec-WebSocket-Protocol"), ",") {
		proto := strings.TrimSpace(part)
		if strings.HasPrefix(proto, WSBearerPrefix) || (strings.HasPrefix(proto, "bearer.") && !strings.HasPrefix(proto, WSBearerPrefix)) {
			return proto
		}
	}
	return ""
}

var publicPaths = map[string]bool{
	"/health": true, "/": true,
	"/api/auth/status": true, "/api/auth/pair": true, "/api/endpoint": true,
}

func isStaticAsset(p string) bool {
	for _, ext := range []string{".js", ".css", ".svg", ".png", ".ico", ".woff", ".woff2"} {
		if strings.HasSuffix(p, ext) {
			return true
		}
	}
	return false
}

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if publicPaths[p] || isStaticAsset(p) {
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(p, "/bridge/") {
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(p, "/api/") || p == "/ws" {
			token := extractToken(r)
			if !s.Hub.Auth.CheckToken(token) {
				log.Printf("auth failed path=%s token=%s", p, redactToken(token))
				writeJSON(w, http.StatusUnauthorized, map[string]any{
					"error": "unauthorized", "hint": "pair with POST /api/auth/pair",
				})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func readJSON(r *http.Request) map[string]any {
	defer r.Body.Close()
	var body map[string]any
	b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil || len(b) == 0 {
		return map[string]any{}
	}
	if json.Unmarshal(b, &body) != nil {
		return map[string]any{}
	}
	if body == nil {
		return map[string]any{}
	}
	return body
}

func (s *Server) routes() {
	s.Mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true})
	})
	s.Mux.HandleFunc("/api/endpoint", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, endpoint.PublicView(s.Hub.Store.Root))
	})
	s.Mux.HandleFunc("/api/auth/status", func(w http.ResponseWriter, r *http.Request) {
		body := s.Hub.Auth.Status()
		body["endpoint"] = endpoint.PublicView(s.Hub.Store.Root)
		if name := HostUserLabel(); name != "" {
			body["user_name"] = name
		}
		writeJSON(w, 200, body)
	})
	s.Mux.HandleFunc("/api/auth/pair", s.handlePair)
	s.Mux.HandleFunc("/api/auth/rotate", s.handleRotate)
	s.Mux.HandleFunc("/api/auth/rotate-pairing", s.handleRotatePairing)
	s.Mux.HandleFunc("/api/usage", s.handleUsage)
	s.Mux.HandleFunc("/api/sessions", s.handleSessions)
	s.Mux.HandleFunc("/api/sessions/", s.handleSessionByID)
	s.Mux.HandleFunc("/api/control/restart", s.handleRestart)
	s.Mux.HandleFunc("/ws", s.handleWS)
	s.Mux.HandleFunc("/bridge/v1/jobs", s.handleBridgeJobs)
	s.Mux.HandleFunc("/bridge/v1/jobs/", s.handleBridgeJobByID)
	s.Mux.HandleFunc("/", s.handleStatic)
}

func (s *Server) handlePair(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	if ip == "" {
		ip = r.RemoteAddr
	}
	if !s.PairLimiter.Allow(ip) {
		log.Printf("pairing rate-limited ip=%s", ip)
		writeJSON(w, 429, map[string]any{"error": "too many pairing attempts; try again later"})
		return
	}
	body := readJSON(r)
	code, _ := body["code"].(string)
	ok, token, errMsg := s.Hub.Auth.Pair(code)
	if !ok {
		writeJSON(w, 403, map[string]any{"error": errMsg})
		return
	}
	writeJSON(w, 200, map[string]any{"token": token, "ok": true})
}

func (s *Server) handleRotate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	tok := s.Hub.Auth.RotateToken()
	log.Printf("token rotated (new token not logged)")
	writeJSON(w, 200, map[string]any{"token": tok, "ok": true})
}

func (s *Server) handleRotatePairing(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	code := s.Hub.Auth.RotatePairingCode()
	writeJSON(w, 200, map[string]any{"ok": true, "pairing_code": code})
}


func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	if s.Usage == nil {
		writeJSON(w, 200, grokusage.Snapshot{Available: false, Source: "none", Reason: "usage cache not configured"})
		return
	}
	writeJSON(w, 200, s.Usage.Snapshot(r.Context()))
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		body := map[string]any{"sessions": s.listMergedSessions(r)}
		if s.Usage != nil {
			body["usage"] = s.Usage.Snapshot(r.Context())
		}
		writeJSON(w, 200, body)
	case http.MethodPost:
		body := readJSON(r)
		title, _ := body["title"].(string)
		if wantDemoSession(body, r) {
			sess, err := s.Hub.Store.Create(title)
			if err != nil {
				writeJSON(w, 500, map[string]any{"error": err.Error()})
				return
			}
			sess.Source = "bridge"
			summary := map[string]any{
				"id": sess.ID, "title": sess.Title,
				"created_at": sess.CreatedAt, "updated_at": sess.UpdatedAt, "message_count": 0,
				"source": "bridge",
			}
			s.Hub.Broadcast(map[string]any{"type": "session_created", "session": summary}, "", nil)
			writeJSON(w, 201, sess)
			return
		}
		sess, err := s.createBuildSession(r.Context(), body, title)
		if err != nil {
			status := 500
			if isAgentUnavailable(err) {
				status = 503
			}
			writeJSON(w, status, map[string]any{"error": err.Error(), "hint": agentHint(err)})
			return
		}
		summary := map[string]any{
			"id": sess.ID, "title": sess.Title,
			"created_at": sess.CreatedAt, "updated_at": sess.UpdatedAt, "message_count": 0,
			"source": "build",
		}
		s.Hub.Broadcast(map[string]any{"type": "session_created", "session": summary}, "", nil)
		writeJSON(w, 201, sess)
	default:
		http.Error(w, "method not allowed", 405)
	}
}

func (s *Server) listMergedSessions(r *http.Request) []sessions.SessionSummary {
	bridge := s.Hub.Store.ListSessions()
	out := make([]sessions.SessionSummary, 0, len(bridge)+8)
	for _, sum := range bridge {
		sum.Source = "bridge"
		out = append(out, sum)
	}
	includeGrokOnly := false
	if r != nil {
		includeGrokOnly = buildsessions.QueryIncludesGrokOnly(r.URL.Query().Get("include"))
	}
	out = append(out, buildsessions.ListIncluding(includeGrokOnly)...)
	seen := map[string]struct{}{}
	for _, sum := range out {
		seen[sum.ID] = struct{}{}
	}
	s.createdMu.Lock()
	for id, sess := range s.createdBuild {
		if sess == nil {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		out = append(out, sessions.SessionSummary{
			ID: sess.ID, Title: sess.Title,
			CreatedAt: sess.CreatedAt, UpdatedAt: sess.UpdatedAt,
			MessageCount: len(sess.Messages), Source: "build",
		})
	}
	s.createdMu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		return out[i].UpdatedAt > out[j].UpdatedAt
	})
	return out
}

func wantDemoSession(body map[string]any, r *http.Request) bool {
	if r != nil && r.URL.Query().Get("demo") == "1" {
		return true
	}
	v, ok := body["demo"].(bool)
	return ok && v
}

func resolveNewSessionCWD(body map[string]any) (string, error) {
	if body != nil {
		if v, _ := body["cwd"].(string); strings.TrimSpace(v) != "" {
			cwd := strings.TrimSpace(v)
			if !filepath.IsAbs(cwd) {
				abs, err := filepath.Abs(cwd)
				if err != nil {
					return "", fmt.Errorf("cwd must be an absolute path")
				}
				cwd = abs
			}
			return cwd, nil
		}
	}
	if v := strings.TrimSpace(os.Getenv("GROK_BRIDGE_CWD")); v != "" {
		if !filepath.IsAbs(v) {
			if abs, err := filepath.Abs(v); err == nil {
				v = abs
			}
		}
		return filepath.Clean(v), nil
	}
	if wd, err := os.Getwd(); err == nil && wd != "" {
		return wd, nil
	}
	if h := buildsessions.GrokHome(); h != "" {
		return h, nil
	}
	return "", fmt.Errorf("no working directory for new Grok Build session")
}

func (s *Server) createBuildSession(ctx context.Context, body map[string]any, title string) (*sessions.Session, error) {
	if s.ACP == nil {
		return nil, fmt.Errorf("cannot reach grok agent: ACP manager not configured")
	}
	cwd, err := resolveNewSessionCWD(body)
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	client := s.ACP.Client()
	rawID, err := client.NewSession(ctx, cwd)
	if err != nil {
		return nil, err
	}
	bridgeID := buildsessions.WithPrefix(rawID)
	sess, _ := buildsessions.Get(bridgeID)
	if sess == nil {
		if title == "" {
			title = "New chat"
		}
		now := float64(time.Now().UnixNano()) / 1e9
		sess = &sessions.Session{
			ID:        bridgeID,
			Title:     title,
			CreatedAt: now,
			UpdatedAt: now,
			Messages:  []sessions.Message{},
			Source:    "build",
		}
	}
	s.rememberCreated(sess, cwd)
	return sess, nil
}

func (s *Server) rememberCreated(sess *sessions.Session, cwd string) {
	if sess == nil {
		return
	}
	s.createdMu.Lock()
	defer s.createdMu.Unlock()
	if s.createdBuild == nil {
		s.createdBuild = make(map[string]*sessions.Session)
	}
	if s.createdCwd == nil {
		s.createdCwd = make(map[string]string)
	}
	s.createdBuild[sess.ID] = sess
	if cwd == "" {
		return
	}
	s.createdCwd[sess.ID] = cwd
	if raw, ok := buildsessions.StripPrefix(sess.ID); ok {
		s.createdCwd[raw] = cwd
	}
}

func (s *Server) getBuildSession(sid string) *sessions.Session {
	if sess, err := buildsessions.Get(sid); err == nil && sess != nil {
		return sess
	}
	s.createdMu.Lock()
	defer s.createdMu.Unlock()
	return s.createdBuild[sid]
}

func (s *Server) getCreatedCwd(sid string) string {
	s.createdMu.Lock()
	defer s.createdMu.Unlock()
	if cwd := s.createdCwd[sid]; cwd != "" {
		return cwd
	}
	if raw, ok := buildsessions.StripPrefix(sid); ok {
		return s.createdCwd[raw]
	}
	return ""
}

func (s *Server) handleSessionByID(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/sessions/")
	parts := strings.Split(rest, "/")
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	// URL path may leave "build%3A..." decoded as "build:..."
	sid := parts[0]
	if len(parts) == 1 {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", 405)
			return
		}
		if _, isBuild := buildsessions.StripPrefix(sid); isBuild {
			sess := s.getBuildSession(sid)
			if sess == nil {
				writeJSON(w, 404, map[string]any{"error": "not found"})
				return
			}
			writeJSON(w, 200, sess)
			return
		}
		sess, err := s.Hub.Store.Get(sid)
		if err != nil || sess == nil {
			writeJSON(w, 404, map[string]any{"error": "not found"})
			return
		}
		if sess.Source == "" {
			sess.Source = "bridge"
		}
		writeJSON(w, 200, sess)
		return
	}
	if parts[1] == "messages" && r.Method == http.MethodPost {
		s.handlePostMessage(w, r, sid)
		return
	}
	if parts[1] == "cancel" && r.Method == http.MethodPost {
		s.handleCancelSession(w, r, sid)
		return
	}
	if parts[1] == "models" && r.Method == http.MethodGet {
		s.handleSessionModels(w, r, sid)
		return
	}
	if parts[1] == "model" && (r.Method == http.MethodPut || r.Method == http.MethodPost) {
		s.handleSetSessionModel(w, r, sid)
		return
	}
	http.NotFound(w, r)
}

func (s *Server) setTurnCancel(sessionID string, cancel context.CancelFunc) {
	s.turnsMu.Lock()
	defer s.turnsMu.Unlock()
	if prev, ok := s.turns[sessionID]; ok && prev != nil {
		prev()
	}
	s.turns[sessionID] = cancel
}

func (s *Server) clearTurnCancel(sessionID string, cancel context.CancelFunc) {
	s.turnsMu.Lock()
	defer s.turnsMu.Unlock()
	// CancelFuncs are not comparable; clear the slot if present (turn finished).
	if _, ok := s.turns[sessionID]; ok {
		delete(s.turns, sessionID)
	}
	_ = cancel
}

func (s *Server) cancelTurn(sessionID string) bool {
	s.turnsMu.Lock()
	cancel, ok := s.turns[sessionID]
	if ok {
		delete(s.turns, sessionID)
	}
	s.turnsMu.Unlock()
	if ok && cancel != nil {
		cancel()
		return true
	}
	return false
}

func (s *Server) resolveRawACPSessionID(sid string) (string, bool) {
	if raw, ok := buildsessions.StripPrefix(sid); ok {
		return raw, true
	}
	s.buildRawMu.Lock()
	raw := s.buildRaw[sid]
	s.buildRawMu.Unlock()
	if raw != "" {
		return raw, true
	}
	return "", false
}

func (s *Server) ensureBuildLoaded(ctx context.Context, sid string) (string, error) {
	raw, ok := s.resolveRawACPSessionID(sid)
	if !ok {
		return "", fmt.Errorf("not a Grok Build session")
	}
	if s.ACP == nil {
		return "", fmt.Errorf("ACP manager not configured")
	}
	client := s.ACP.Client()
	if len(client.ConfigOptions(raw)) > 0 {
		return raw, nil
	}
	cwd := s.getCreatedCwd(sid)
	if cwd == "" {
		if _, c, err := buildsessions.ResolveMeta(sid); err == nil {
			cwd = c
		}
	}
	if cwd == "" {
		return raw, nil // return stored/empty; caller reports unavailable
	}
	if err := client.LoadSession(ctx, raw, cwd); err != nil {
		return raw, err
	}
	return raw, nil
}

func (s *Server) handleSessionModels(w http.ResponseWriter, r *http.Request, sid string) {
	if _, isBuild := buildsessions.StripPrefix(sid); !isBuild {
		writeJSON(w, 200, grokacp.ModelConfig{Available: false, Reason: "demo/bridge sessions have no ACP models"})
		return
	}
	if s.getBuildSession(sid) == nil {
		writeJSON(w, 404, map[string]any{"error": "not found"})
		return
	}
	if s.ACP == nil {
		writeJSON(w, 503, map[string]any{"error": "ACP unavailable", "available": false})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	raw, err := s.ensureBuildLoaded(ctx, sid)
	if err != nil && raw == "" {
		writeJSON(w, 503, map[string]any{"error": err.Error(), "available": false})
		return
	}
	mc := s.ACP.Client().ModelConfig(raw)
	if err != nil && !mc.Available {
		mc.Reason = err.Error()
	}
	writeJSON(w, 200, mc)
}

func (s *Server) handleSetSessionModel(w http.ResponseWriter, r *http.Request, sid string) {
	if _, isBuild := buildsessions.StripPrefix(sid); !isBuild {
		writeJSON(w, 400, map[string]any{"error": "model selection requires a Grok Build session"})
		return
	}
	if s.getBuildSession(sid) == nil {
		writeJSON(w, 404, map[string]any{"error": "not found"})
		return
	}
	if s.ACP == nil {
		writeJSON(w, 503, map[string]any{"error": "ACP unavailable"})
		return
	}
	body := readJSON(r)
	value, _ := body["value"].(string)
	if value == "" {
		value, _ = body["model"].(string)
	}
	value = strings.TrimSpace(value)
	if value == "" {
		writeJSON(w, 400, map[string]any{"error": "value required"})
		return
	}
	configID, _ := body["configId"].(string)
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	raw, err := s.ensureBuildLoaded(ctx, sid)
	if err != nil && raw == "" {
		status := 500
		if isAgentUnavailable(err) {
			status = 503
		}
		writeJSON(w, status, map[string]any{"error": err.Error()})
		return
	}
	client := s.ACP.Client()
	if strings.TrimSpace(configID) == "" {
		mc := client.ModelConfig(raw)
		configID = mc.ConfigID
	}
	if configID == "" {
		configID = "model"
	}
	opts, err := client.SetConfigOption(ctx, raw, configID, "id", value)
	if err != nil {
		status := 500
		if isAgentUnavailable(err) {
			status = 503
		}
		writeJSON(w, status, map[string]any{"error": err.Error()})
		return
	}
	mc := grokacp.ModelConfigFromOptions(opts)
	s.Hub.Broadcast(map[string]any{
		"type": "session_models", "session_id": sid, "models": mc,
	}, sid, nil)
	writeJSON(w, 200, mc)
}

func (s *Server) handleCancelSession(w http.ResponseWriter, r *http.Request, sid string) {
	if _, isBuild := buildsessions.StripPrefix(sid); isBuild {
		if s.getBuildSession(sid) == nil {
			writeJSON(w, 404, map[string]any{"error": "not found"})
			return
		}
		hadTurn := s.cancelTurn(sid)
		raw := s.takeBuildRaw(sid)
		if raw == "" {
			if r2, _, err := buildsessions.ResolveMeta(sid); err == nil {
				raw = r2
			}
		}
		if raw != "" && s.ACP != nil {
			_ = s.ACP.Client().Cancel(raw)
		}
		writeJSON(w, 200, map[string]any{
			"ok": true, "session_id": sid,
			"turn_cancelled": hadTurn, "jobs_cancelled": 0,
		})
		return
	}
	sess, _ := s.Hub.Store.Get(sid)
	if sess == nil {
		writeJSON(w, 404, map[string]any{"error": "not found"})
		return
	}
	hadTurn := s.cancelTurn(sid)
	n := 0
	if s.JobRegistry != nil {
		n = s.JobRegistry.CancelBySession(sid)
	}
	writeJSON(w, 200, map[string]any{
		"ok": true, "session_id": sid,
		"turn_cancelled": hadTurn, "jobs_cancelled": n,
	})
}

func (s *Server) handlePostMessage(w http.ResponseWriter, r *http.Request, sid string) {
	body := readJSON(r)
	content, _ := body["content"].(string)
	content = strings.TrimSpace(content)
	if content == "" {
		writeJSON(w, 400, map[string]any{"error": "content required"})
		return
	}

	run := s.runChat
	if _, isBuild := buildsessions.StripPrefix(sid); isBuild {
		if s.getBuildSession(sid) == nil {
			writeJSON(w, 404, map[string]any{"error": "not found"})
			return
		}
		run = s.runBuildChat
	} else {
		sess, _ := s.Hub.Store.Get(sid)
		if sess == nil {
			writeJSON(w, 404, map[string]any{"error": "not found"})
			return
		}
	}

	idCh := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		mid, err := run(context.Background(), sid, content, func(id string) {
			select {
			case idCh <- id:
			default:
			}
		})
		if err != nil {
			errCh <- err
			return
		}
		select {
		case idCh <- mid:
		default:
		}
	}()
	select {
	case mid := <-idCh:
		writeJSON(w, 200, map[string]any{"ok": true, "message_id": mid})
	case err := <-errCh:
		status := 500
		if isAgentUnavailable(err) {
			status = 503
		}
		writeJSON(w, status, map[string]any{"error": err.Error(), "hint": agentHint(err)})
	case <-time.After(10 * time.Second):
		writeJSON(w, 500, map[string]any{"error": "send failed"})
	}
}

func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	writeJSON(w, 200, s.Restart.Request())
}

func (s *Server) runChat(parent context.Context, sessionID, content string, onUserSaved func(string)) (string, error) {
	lk := s.Hub.lockFor(sessionID)
	lk.Lock()
	defer lk.Unlock()

	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	s.setTurnCancel(sessionID, cancel)
	defer func() {
		cancel()
		s.clearTurnCancel(sessionID, cancel)
	}()

	data, err := s.Hub.Store.AppendMessage(sessionID, "user", content, nil)
	if err != nil {
		return "", err
	}
	messageID := data.Messages[len(data.Messages)-1].ID
	if onUserSaved != nil {
		onUserSaved(messageID)
	}
	s.Hub.Broadcast(map[string]any{
		"type": "user_message", "session_id": sessionID,
		"content": content, "message_id": messageID,
	}, sessionID, nil)

	sess, _ := s.Hub.Store.Get(sessionID)
	var history []map[string]any
	if sess != nil {
		for _, m := range sess.Messages {
			hm := map[string]any{"id": m.ID, "role": m.Role, "content": m.Content, "ts": m.TS}
			if m.Tools != nil {
				hm["tools"] = m.Tools
			}
			history = append(history, hm)
		}
	}
	var toolsAcc []map[string]any
	fullText := ""
	gotDone := false
	cancelled := false

	ch, err := s.Hub.Agent.StreamReply(ctx, sessionID, content, history)
	if err != nil {
		return messageID, err
	}
	for event := range ch {
		et, _ := event["type"].(string)
		switch et {
		case "assistant_delta":
			if d, ok := event["delta"].(string); ok {
				fullText += d
			}
		case "tool_call", "tool_result", "tool_card":
			if tool, ok := event["tool"].(map[string]any); ok {
				toolsAcc = agent.ApplyToolEvent(toolsAcc, et, tool)
			}
		case "assistant_done":
			gotDone = true
			if c, ok := event["content"].(string); ok && c != "" {
				fullText = c
			}
			if errStr, ok := event["error"].(string); ok && errStr != "" && errStr != "cancelled" && s.Usage != nil {
				s.Usage.NoteLimitError(fmt.Errorf("%s", errStr))
			}
			if event["cancelled"] == true {
				cancelled = true
			}
			if errStr, ok := event["error"].(string); ok && errStr == "cancelled" {
				cancelled = true
			}
		}
		s.Hub.Broadcast(event, sessionID, nil)
	}
	if !gotDone {
		cancelled = true
		doneEvt := map[string]any{
			"type": "assistant_done", "session_id": sessionID,
			"content": fullText, "cancelled": true, "error": "cancelled",
		}
		s.Hub.Broadcast(doneEvt, sessionID, nil)
	}
	extra := map[string]any{}
	if len(toolsAcc) > 0 {
		extra["tools"] = toolsAcc
	}
	if cancelled {
		extra["cancelled"] = true
		if fullText == "" {
			fullText = "(cancelled)"
		}
	}
	_, _ = s.Hub.Store.AppendMessage(sessionID, "assistant", fullText, extra)
	return messageID, nil
}

func (s *Server) setBuildRaw(bridgeID, rawID string) {
	s.buildRawMu.Lock()
	defer s.buildRawMu.Unlock()
	if s.buildRaw == nil {
		s.buildRaw = make(map[string]string)
	}
	s.buildRaw[bridgeID] = rawID
}

func (s *Server) takeBuildRaw(bridgeID string) string {
	s.buildRawMu.Lock()
	defer s.buildRawMu.Unlock()
	raw := s.buildRaw[bridgeID]
	delete(s.buildRaw, bridgeID)
	return raw
}

func isAgentUnavailable(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "cannot reach grok agent") ||
		strings.Contains(msg, "not connected") ||
		strings.Contains(msg, "auto-start failed") ||
		strings.Contains(msg, "not found on path")
}

func agentHint(err error) string {
	if err == nil {
		return ""
	}
	if isAgentUnavailable(err) {
		return "start grok agent serve: grok agent --always-approve --no-leader serve --bind 127.0.0.1:2419 --secret <token> (or set GROK_BRIDGE_GROK_AGENT_AUTO_START=1)"
	}
	return ""
}

// runBuildChat resumes a Grok Build session via ACP and streams Bridge events.
func (s *Server) runBuildChat(parent context.Context, sessionID, content string, onUserSaved func(string)) (string, error) {
	lk := s.Hub.lockFor(sessionID)
	lk.Lock()
	defer lk.Unlock()

	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	s.setTurnCancel(sessionID, cancel)
	defer func() {
		cancel()
		s.clearTurnCancel(sessionID, cancel)
		s.takeBuildRaw(sessionID)
	}()

	rawID, cwd, err := buildsessions.ResolveMeta(sessionID)
	if err != nil {
		raw, ok := buildsessions.StripPrefix(sessionID)
		cwd = s.getCreatedCwd(sessionID)
		if ok && cwd == "" && s.ACP != nil {
			if loaded, lok := s.ACP.Client().LoadedCwd(raw); lok {
				cwd = loaded
			}
		}
		if !ok || cwd == "" {
			return "", err
		}
		rawID = raw
	}
	s.setBuildRaw(sessionID, rawID)

	if s.ACP == nil {
		return "", fmt.Errorf("ACP manager not configured")
	}
	client := s.ACP.Client()
	if err := client.EnsureConnected(ctx); err != nil {
		return "", err
	}
	if err := client.LoadSession(ctx, rawID, cwd); err != nil {
		return "", fmt.Errorf("session/load: %w", err)
	}

	messageID := "build-live-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if onUserSaved != nil {
		onUserSaved(messageID)
	}
	s.Hub.Broadcast(map[string]any{
		"type": "user_message", "session_id": sessionID,
		"content": content, "message_id": messageID,
	}, sessionID, nil)

	ch, err := client.Prompt(ctx, sessionID, rawID, content)
	if err != nil {
		if s.Usage != nil {
			s.Usage.NoteLimitError(err)
		}
		s.Hub.Broadcast(map[string]any{
			"type": "assistant_done", "session_id": sessionID,
			"content": "", "error": err.Error(), "hint": agentHint(err),
		}, sessionID, nil)
		return messageID, err
	}

	var fullText string
	gotDone := false
	for event := range ch {
		et, _ := event["type"].(string)
		switch et {
		case "assistant_delta":
			if d, ok := event["delta"].(string); ok {
				fullText += d
			}
		case "assistant_done":
			gotDone = true
			if c, ok := event["content"].(string); ok && c != "" {
				fullText = c
			}
			if errStr, ok := event["error"].(string); ok && errStr != "" && errStr != "cancelled" && s.Usage != nil {
				s.Usage.NoteLimitError(fmt.Errorf("%s", errStr))
			}
		}
		s.Hub.Broadcast(event, sessionID, nil)
	}
	if !gotDone {
		s.Hub.Broadcast(map[string]any{
			"type": "assistant_done", "session_id": sessionID,
			"content": fullText, "cancelled": true, "error": "cancelled",
		}, sessionID, nil)
	}

	// Refresh transcript from disk if the agent persisted the turn.
	if sess, err := buildsessions.Get(sessionID); err == nil && sess != nil {
		s.Hub.Broadcast(map[string]any{
			"type": "session.snapshot", "session_id": sessionID, "session": sess,
		}, sessionID, nil)
	}
	return messageID, nil
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	selected := selectedWSProtocol(r)
	up := upgrader
	if selected != "" {
		up.Subprotocols = []string{selected}
	}
	conn, err := up.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	s.Hub.mu.Lock()
	s.Hub.clients[conn] = map[string]struct{}{}
	s.Hub.mu.Unlock()

	_ = conn.WriteJSON(map[string]any{"type": "hello", "ok": true, "version": Version})

	defer func() {
		s.Hub.mu.Lock()
		delete(s.Hub.clients, conn)
		s.Hub.mu.Unlock()
		_ = conn.Close()
	}()

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var msg map[string]any
		if json.Unmarshal(data, &msg) != nil {
			_ = conn.WriteJSON(map[string]any{"type": "error", "error": "invalid json"})
			continue
		}
		s.handleWSMessage(conn, msg)
	}
}

func (s *Server) handleWSMessage(conn *websocket.Conn, data map[string]any) {
	mtype, _ := data["type"].(string)
	switch mtype {
	case "ping":
		_ = conn.WriteJSON(map[string]any{"type": "pong"})
	case "session.subscribe":
		sid, _ := data["session_id"].(string)
		if sid == "" {
			_ = conn.WriteJSON(map[string]any{"type": "error", "error": "session_id required"})
			return
		}
		var sess *sessions.Session
		if _, isBuild := buildsessions.StripPrefix(sid); isBuild {
			sess = s.getBuildSession(sid)
		} else {
			sess, _ = s.Hub.Store.Get(sid)
			if sess != nil && sess.Source == "" {
				sess.Source = "bridge"
			}
		}
		if sess == nil {
			_ = conn.WriteJSON(map[string]any{"type": "error", "error": "session not found"})
			return
		}
		s.Hub.mu.Lock()
		if s.Hub.clients[conn] == nil {
			s.Hub.clients[conn] = map[string]struct{}{}
		}
		s.Hub.clients[conn][sid] = struct{}{}
		s.Hub.mu.Unlock()
		_ = conn.WriteJSON(map[string]any{"type": "session.snapshot", "session_id": sid, "session": sess})
	case "chat.send":
		sid, _ := data["session_id"].(string)
		content, _ := data["content"].(string)
		content = strings.TrimSpace(content)
		if sid == "" || content == "" {
			_ = conn.WriteJSON(map[string]any{"type": "error", "error": "session_id and content required"})
			return
		}
		var isBuild bool
		if _, isBuild = buildsessions.StripPrefix(sid); isBuild {
			if s.getBuildSession(sid) == nil {
				_ = conn.WriteJSON(map[string]any{"type": "error", "error": "session not found"})
				return
			}
		} else {
			sess, _ := s.Hub.Store.Get(sid)
			if sess == nil {
				_ = conn.WriteJSON(map[string]any{"type": "error", "error": "session not found"})
				return
			}
		}
		s.Hub.mu.Lock()
		if s.Hub.clients[conn] == nil {
			s.Hub.clients[conn] = map[string]struct{}{}
		}
		s.Hub.clients[conn][sid] = struct{}{}
		s.Hub.mu.Unlock()
		go func() {
			if isBuild {
				_, err := s.runBuildChat(context.Background(), sid, content, nil)
				if err != nil {
					s.Hub.Broadcast(map[string]any{
						"type": "assistant_done", "session_id": sid,
						"content": "", "error": err.Error(), "hint": agentHint(err),
					}, sid, nil)
				}
				return
			}
			_, _ = s.runChat(context.Background(), sid, content, nil)
		}()
	default:
		_ = conn.WriteJSON(map[string]any{"type": "error", "error": "unknown type: " + mtype})
	}
}

func (s *Server) requireBridge(w http.ResponseWriter, r *http.Request) bool {
	if !bridge.CheckBridgeSecret(r.Header, s.BridgeSecret) {
		writeJSON(w, 401, map[string]any{"error": "unauthorized", "hint": "bridge routes require GROK_BRIDGE_SECRET"})
		return false
	}
	return true
}

func (s *Server) handleBridgeJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	if !s.requireBridge(w, r) {
		return
	}
	body := readJSON(r)
	sid, _ := body["session_id"].(string)
	sid = strings.TrimSpace(sid)
	userMsg, _ := body["user_message"].(string)
	if userMsg == "" {
		userMsg, _ = body["content"].(string)
	}
	if sid == "" {
		writeJSON(w, 400, map[string]any{"error": "session_id required"})
		return
	}
	var history []map[string]any
	if h, ok := body["history"].([]any); ok {
		for _, item := range h {
			if m, ok := item.(map[string]any); ok {
				history = append(history, m)
			}
		}
	}
	job := s.JobRegistry.Create(sid, userMsg, history, "")
	job.Status = "pending"
	writeJSON(w, 201, job.ToPublic())
}

func (s *Server) handleBridgeJobByID(w http.ResponseWriter, r *http.Request) {
	if !s.requireBridge(w, r) {
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/bridge/v1/jobs/")
	parts := strings.Split(rest, "/")
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	jid := parts[0]
	job := s.JobRegistry.Get(jid)
	if job == nil {
		writeJSON(w, 404, map[string]any{"error": "not found"})
		return
	}
	if len(parts) == 1 && r.Method == http.MethodGet {
		writeJSON(w, 200, job.ToPublic())
		return
	}
	if len(parts) == 2 && parts[1] == "events" && r.Method == http.MethodPost {
		body := readJSON(r)
		if body["type"] == nil || body["type"] == "" {
			writeJSON(w, 400, map[string]any{"error": "type required"})
			return
		}
		ok := s.JobRegistry.EnqueueEvent(jid, body)
		if !ok {
			writeJSON(w, 409, map[string]any{"error": "job not accepting events"})
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "job_id": jid, "status": job.Status})
		return
	}
	if len(parts) == 2 && parts[1] == "complete" && r.Method == http.MethodPost {
		body := readJSON(r)
		var content, errMsg *string
		if c, ok := body["content"].(string); ok {
			content = &c
		}
		if e, ok := body["error"].(string); ok {
			errMsg = &e
		}
		ok := s.JobRegistry.Complete(jid, content, errMsg)
		job = s.JobRegistry.Get(jid)
		var pub any
		if job != nil {
			pub = job.ToPublic()
		}
		writeJSON(w, 200, map[string]any{"ok": ok, "job": pub})
		return
	}
	if len(parts) == 2 && parts[1] == "cancel" && r.Method == http.MethodPost {
		ok := s.JobRegistry.Cancel(jid)
		// Also cancel hub turn for this job's session if active
		if job != nil && job.SessionID != "" {
			s.cancelTurn(job.SessionID)
		}
		job = s.JobRegistry.Get(jid)
		var pub any
		if job != nil {
			pub = job.ToPublic()
		}
		writeJSON(w, 200, map[string]any{"ok": ok, "job": pub})
		return
	}
	http.NotFound(w, r)
}

func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" {
		s.serveFile(w, r, "index.html")
		return
	}
	name := path.Clean(strings.TrimPrefix(r.URL.Path, "/"))
	if name == "." || strings.Contains(name, "..") {
		http.NotFound(w, r)
		return
	}
	if !isStaticAsset("/"+name) && name != "index.html" {
		http.NotFound(w, r)
		return
	}
	s.serveFile(w, r, name)
}

func (s *Server) serveFile(w http.ResponseWriter, r *http.Request, name string) {
	if s.WebFS != nil {
		b, err := fs.ReadFile(s.WebFS, name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		ctype := "application/octet-stream"
		switch {
		case strings.HasSuffix(name, ".html"):
			ctype = "text/html; charset=utf-8"
		case strings.HasSuffix(name, ".js"):
			ctype = "application/javascript; charset=utf-8"
		case strings.HasSuffix(name, ".css"):
			ctype = "text/css; charset=utf-8"
		case strings.HasSuffix(name, ".svg"):
			ctype = "image/svg+xml"
		case strings.HasSuffix(name, ".png"):
			ctype = "image/png"
		case strings.HasSuffix(name, ".ico"):
			ctype = "image/x-icon"
		}
		w.Header().Set("Content-Type", ctype)
		w.WriteHeader(200)
		_, _ = w.Write(b)
		return
	}
	http.NotFound(w, r)
}

// ListenAndServe starts the HTTP(S) server.
func ListenAndServe(addr string, handler http.Handler, tlsCfg *tls.Config) error {
	srv := &http.Server{Addr: addr, Handler: handler, TLSConfig: tlsCfg}
	if tlsCfg != nil {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return err
		}
		return srv.Serve(tls.NewListener(ln, tlsCfg))
	}
	return srv.ListenAndServe()
}

// ResolveWebDir finds the web/ directory relative to executable or CWD.
func ResolveWebDir() string {
	candidates := []string{}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(wd, "web"))
	}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "web"))
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "..", "web"))
	}
	for _, c := range candidates {
		if st, err := os.Stat(filepath.Join(c, "index.html")); err == nil && !st.IsDir() {
			abs, _ := filepath.Abs(c)
			return abs
		}
	}
	return "web"
}
