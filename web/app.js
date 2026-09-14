/**
 * Grok Bridge UI — desktop + phone cockpit with session handoff.
 * Server is source of truth; token from pairing is shared across devices
 * (paste or pair again with the same code).
 *
 * Mobile resilience: if WebSocket is down, POST /api/sessions/{id}/messages;
 * poll transcript while WS off or tab hidden; backoff reconnect when hidden.
 */
(function () {
  const params = new URLSearchParams(location.search);
  const DEMO = params.get("demo") === "1";
  const TOKEN_KEY = "grok_bridge_token";
  const GROK_ONLY_KEY = "grok_bridge_include_grok_only";
  const LIVE_MS = 4000;
  const RECONNECT_BASE_MS = 1500;
  const RECONNECT_MAX_HIDDEN_MS = 30000;
  const POLL_MS = 4000;

  const $ = (id) => document.getElementById(id);
  const els = {
    sidebar: $("sidebar"),
    backdrop: $("backdrop"),
    sessionList: $("sessionList"),
    feed: $("feed"),
    chatTitle: $("chatTitle"),
    input: $("input"),
    composer: $("composer"),
    btnSend: $("btnSend"),
    btnNew: $("btnNew"),
    btnMenu: $("btnMenu"),
    ctxMeter: $("ctxMeter"),
    ctxSub: $("ctxSub"),
    modeBadge: $("modeBadge"),
    btnSettings: $("btnSettings"),
    settingsModal: $("settingsModal"),
    demoToggle: $("demoToggle"),
    btnRestart: $("btnRestart"),
    grokOnlyToggle: $("grokOnlyToggle"),
    btnJumpBottom: $("btnJumpBottom"),
  };

  let token = localStorage.getItem(TOKEN_KEY) || "";
  let sessions = [];
  let includeGrokOnly = localStorage.getItem(GROK_ONLY_KEY) === "1";
  let activeId = null;
  let ws = null;
  let streamingEl = null;
  let streamingTools = null;
  let reconnectDelay = RECONNECT_BASE_MS;
  let reconnectTimer = null;
  let pollTimer = null;
  let catchingUp = false;
  let sending = false;
  let turnActive = false;
  let liveTimer = null;
  let countsPrimed = false;
  const unreadIds = new Set();
  const seenCounts = Object.create(null);
  const selfSendAt = Object.create(null);

  if (DEMO) {
    document.title = "Grok Bridge (demo)";
  }

  function setSessionOwner(name) {
    const label = String(name || "").trim();
    if (!els.modeBadge) return;
    els.modeBadge.textContent = label ? label + "'s sessions list" : "Sessions";
  }

  /** Allowlist endpoint URLs before setting href (XSS-safe). */
  function parseAllowedEndpointUrl(raw) {
    try {
      const u = new URL(String(raw || ""));
      const host = u.hostname;
      const isLocal = host === "localhost" || host === "127.0.0.1";
      const isTailscale =
        /^100\.\d{1,3}\.\d{1,3}\.\d{1,3}$/.test(host) || /\.ts\.net$/i.test(host);
      const okHost = isLocal || isTailscale;
      if (!okHost) return null;
      // http is valid for Tailscale when hub has no TLS; https always ok for allowlisted hosts.
      if (u.protocol !== "https:" && u.protocol !== "http:") return null;
      return u;
    } catch (_) {
      return null;
    }
  }

  /** Show Tailscale MagicDNS phone URL from /api/endpoint (DHCP-resilient). */
  async function showEndpointBanner() {
    try {
      const ep = await fetch("/api/endpoint").then((r) => r.json());
      if (!ep || !ep.configured || !ep.url) return;
      const parsed = parseAllowedEndpointUrl(ep.url);
      if (!parsed) return;
      let el = document.getElementById("endpointBanner");
      if (!el) {
        el = document.createElement("div");
        el.id = "endpointBanner";
        el.className = "endpoint-banner";
        const foot = document.querySelector(".sidebar-foot");
        if (foot) foot.parentNode.insertBefore(el, foot);
        else els.sidebar?.appendChild(el);
      }
      el.textContent = "";
      const strong = document.createElement("strong");
      strong.textContent = "Phone URL (Tailscale)";
      const a = document.createElement("a");
      a.href = parsed.href;
      a.textContent = parsed.href;
      el.appendChild(strong);
      el.appendChild(a);
      if (ep.magicdns) {
        const note = document.createElement("div");
        note.className = "muted";
        note.textContent = "Bookmark MagicDNS — survives host DHCP changes.";
        el.appendChild(note);
      }
    } catch (_) { /* offline / no endpoint yet */ }
  }

  function authHeaders() {
    return token ? { Authorization: "Bearer " + token, "Content-Type": "application/json" } : { "Content-Type": "application/json" };
  }

  async function api(path, opts = {}) {
    const res = await fetch(path, {
      ...opts,
      headers: { ...authHeaders(), ...(opts.headers || {}) },
    });
    if (res.status === 401) {
      token = "";
      localStorage.removeItem(TOKEN_KEY);
      await ensurePaired(true);
      throw new Error("unauthorized");
    }
    return res;
  }

  function orbitMarkSvg() {
    return (
      '<span class="pair-mark" aria-hidden="true">' +
      '<svg viewBox="0 0 32 32" width="20" height="20" fill="none">' +
      '<circle cx="16" cy="16" r="11" stroke="currentColor" stroke-width="1.5" opacity="0.35"/>' +
      '<circle cx="16" cy="16" r="5.5" stroke="currentColor" stroke-width="1.5"/>' +
      '<circle cx="16" cy="5" r="1.6" fill="currentColor"/>' +
      "</svg></span>"
    );
  }

  async function ensurePaired(force) {
    if (token && !force) return true;
    const st = await fetch("/api/auth/status").then((r) => r.json());
    let overlay = document.getElementById("pairOverlay");
    if (!overlay) {
      overlay = document.createElement("div");
      overlay.id = "pairOverlay";
      overlay.setAttribute("role", "dialog");
      overlay.setAttribute("aria-modal", "true");
      overlay.setAttribute("aria-labelledby", "pairTitle");
      overlay.innerHTML =
        '<div class="pair-card">' +
        orbitMarkSvg() +
        '<h2 id="pairTitle">Pair this device</h2>' +
        "<p>Enter the 6-digit code from the hub terminal (or <code>data/auth.json</code>). " +
        "Same bearer token unlocks desktop and phone. Pairing codes rotate after each successful pair — use the latest code from the hub journal or paste an existing token. Prefer the Tailscale MagicDNS URL from the sidebar (not a LAN DHCP IP).</p>" +
        '<form id="pairForm"><input id="pairCode" inputmode="numeric" pattern="[0-9]*" maxlength="8" placeholder="000000" autocomplete="one-time-code" aria-label="Pairing code" />' +
        '<button type="submit" class="btn primary">Pair</button></form>' +
        '<p class="muted" id="pairErr" role="alert"></p></div>';
      document.body.appendChild(overlay);
    }
    overlay.style.display = "flex";
    return new Promise((resolve) => {
      const form = document.getElementById("pairForm");
      const err = document.getElementById("pairErr");
      form.onsubmit = async (e) => {
        e.preventDefault();
        err.textContent = "";
        const code = document.getElementById("pairCode").value.trim();
        const res = await fetch("/api/auth/pair", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ code }),
        });
        const data = await res.json();
        if (!res.ok) {
          err.textContent = data.error || "Pairing failed";
          return;
        }
        token = data.token;
        localStorage.setItem(TOKEN_KEY, token);
        overlay.style.display = "none";
        resolve(true);
      };
      if (!st.needs_pairing && DEMO) {
        err.textContent = "Hub already paired — enter the same pairing code to unlock this browser.";
      }
      setTimeout(() => {
        const input = document.getElementById("pairCode");
        if (input) input.focus();
      }, 50);
    });
  }

  function syncPrimaryButton() {
    const btn = els.btnSend;
    if (!btn) return;
    const label = btn.querySelector(".btn-label");
    const iconSend = btn.querySelector(".icon-send");
    const iconStop = btn.querySelector(".icon-stop");
    if (turnActive) {
      btn.type = "button";
      btn.classList.add("is-stop");
      if (label) label.textContent = "Stop";
      btn.setAttribute("aria-label", "Stop reply");
      btn.title = "Stop";
      if (iconSend) iconSend.hidden = true;
      if (iconStop) iconStop.hidden = false;
      btn.disabled = false;
    } else {
      btn.type = "submit";
      btn.classList.remove("is-stop");
      if (label) label.textContent = "Send";
      btn.setAttribute("aria-label", "Send message");
      btn.title = "Send";
      if (iconSend) iconSend.hidden = false;
      if (iconStop) iconStop.hidden = true;
      btn.disabled = !activeId || sending;
    }
  }

  function setTurnActive(on) {
    turnActive = !!on;
    syncPrimaryButton();
  }

  function markCancelled(el) {
    if (!el) return;
    el.classList.add("cancelled");
    if (!el.querySelector(".cancel-note")) {
      const note = document.createElement("div");
      note.className = "cancel-note";
      note.textContent = "Cancelled";
      el.appendChild(note);
    }
  }

  function wsIsOpen() {
    return !!(ws && ws.readyState === WebSocket.OPEN);
  }

  /** No-op: Live/hub pills removed; connection health uses banners only. */
  function setWsPill(_state) {}

  function statusForDegraded() {
    if (typeof navigator !== "undefined" && navigator.onLine === false) return "offline";
    return "sync";
  }

  function formatCtxTokens(n) {
    const v = Number(n);
    if (!Number.isFinite(v) || v < 0) return null;
    if (v < 1000) return String(Math.round(v));
    const k = v / 1000;
    if (k < 10) return (Math.round(k * 10) / 10) + "k";
    return Math.round(k) + "k";
  }

  function clearCtxMeter() {
    if (!els.ctxMeter) return;
    els.ctxMeter.textContent = "—";
    els.ctxMeter.title = "Session context";
    els.ctxMeter.classList.add("empty");
  }

  function updateCtxMeter(sess) {
    if (!els.ctxMeter) return;
    if (!sess) {
      clearCtxMeter();
      return;
    }
    const used = sess.context_tokens_used;
    const limit = sess.context_window_tokens;
    let pct = sess.context_window_usage;
    if (used == null || limit == null || !(Number(limit) > 0)) {
      clearCtxMeter();
      return;
    }
    const usedN = Number(used);
    const limitN = Number(limit);
    if (!Number.isFinite(usedN) || !Number.isFinite(limitN)) {
      clearCtxMeter();
      return;
    }
    if (pct == null || !Number.isFinite(Number(pct))) {
      pct = Math.round((usedN / limitN) * 100);
    } else {
      pct = Math.round(Number(pct));
    }
    const usedLabel = formatCtxTokens(usedN);
    const limitLabel = formatCtxTokens(limitN);
    els.ctxMeter.classList.remove("empty");
    els.ctxMeter.textContent = "";
    els.ctxMeter.appendChild(document.createTextNode(usedLabel + " / " + limitLabel + " "));
    const pctEl = document.createElement("span");
    pctEl.className = "ctx-pct";
    pctEl.textContent = pct + "%";
    els.ctxMeter.appendChild(pctEl);
    els.ctxMeter.title =
      "Context " + usedN.toLocaleString() + " / " + limitN.toLocaleString() + " tokens (" + pct + "%)";
  }

  function showInsecureBanner() {
    if (location.protocol === "https:") return;
    let ban = document.getElementById("insecureBanner");
    if (!ban) {
      ban = document.createElement("div");
      ban.id = "insecureBanner";
      ban.className = "insecure-banner";
      ban.setAttribute("role", "status");
      ban.textContent = "Connection not encrypted — prefer HTTPS / Tailscale for phone access.";
      document.body.prepend(ban);
    }
  }

  function clearReconnectTimer() {
    if (reconnectTimer) {
      clearTimeout(reconnectTimer);
      reconnectTimer = null;
    }
  }

  function scheduleReconnect() {
    clearReconnectTimer();
    const hidden = typeof document !== "undefined" && document.hidden;
    const delay = hidden
      ? Math.min(Math.max(reconnectDelay, RECONNECT_BASE_MS), RECONNECT_MAX_HIDDEN_MS)
      : RECONNECT_BASE_MS;
    reconnectTimer = setTimeout(connectWs, delay);
    if (hidden) {
      reconnectDelay = Math.min(reconnectDelay * 2, RECONNECT_MAX_HIDDEN_MS);
    } else {
      reconnectDelay = RECONNECT_BASE_MS;
    }
  }

  function needsPoll() {
    return !wsIsOpen() || (typeof document !== "undefined" && document.hidden);
  }

  function ensurePoll() {
    if (pollTimer) return;
    pollTimer = setInterval(() => {
      if (!token || !activeId) return;
      if (needsPoll()) {
        if (!wsIsOpen()) setWsPill(statusForDegraded());
        catchUp();
      } else {
        stopPoll();
      }
    }, POLL_MS);
  }

  function stopPoll() {
    if (pollTimer) {
      clearInterval(pollTimer);
      pollTimer = null;
    }
  }

  function refreshPollState() {
    if (needsPoll()) ensurePoll();
    else stopPoll();
  }

  function connectWs() {
    if (!token) return;
    clearReconnectTimer();
    const proto = location.protocol === "https:" ? "wss:" : "ws:";
    // Prefer Sec-WebSocket-Protocol (grok.bearer.<token>) over ?token= query
    const url = proto + "//" + location.host + "/ws";
    const subproto = "grok.bearer." + token;
    if (ws) {
      try {
        ws.onclose = null;
        ws.onerror = null;
        ws.onmessage = null;
        ws.close();
      } catch (_) {}
      ws = null;
    }
    // Subprotocol only — never put bearer in ?token= (proxy/access-log leakage).
    ws = new WebSocket(url, [subproto]);
    setWsPill(document.hidden ? statusForDegraded() : "…");
    ws.onopen = () => {
      setWsPill("live");
      reconnectDelay = RECONNECT_BASE_MS;
      if (els.btnRestart) els.btnRestart.disabled = false;
      refreshPollState();
      if (activeId) subscribeSession(activeId);
      catchUp();
    };
    ws.onclose = () => {
      setWsPill(statusForDegraded());
      refreshPollState();
      scheduleReconnect();
    };
    ws.onerror = () => setWsPill(statusForDegraded());
    ws.onmessage = (ev) => {
      let data;
      try { data = JSON.parse(ev.data); } catch (_) { return; }
      handleEvent(data);
    };
  }

  function forceReconnectAndCatchUp() {
    reconnectDelay = RECONNECT_BASE_MS;
    connectWs();
    catchUp();
  }

  function subscribeSession(sessionId) {
    if (wsIsOpen()) {
      ws.send(JSON.stringify({ type: "session.subscribe", session_id: sessionId }));
    }
  }

  function handleEvent(data) {
    const t = data.type;
    if (t === "hello") return;
    if (t === "session_created") {
      refreshSessions();
      return;
    }
    if (t === "session.snapshot") {
      if (data.session_id === activeId && data.session) {
        if (els.feed.querySelector(".msg")) mergeTranscript(data.session);
        else renderTranscript(data.session);
        updateCtxMeter(data.session);
        if (isLooking(activeId)) clearUnread(activeId);
      }
      return;
    }
    if (data.session_id && activeId && data.session_id !== activeId) {
      if (t === "assistant_start" || t === "assistant_delta" || t === "assistant_done") {
        markUnread(data.session_id);
        refreshSessions().catch(() => {});
      }
      return;
    }

    if (t === "user_message") {
      const mid = data.message_id;
      if (mid && els.feed.querySelector('.msg[data-id="' + CSS.escape(mid) + '"]')) return;
      if (isGrokScaffolding(data.content || "")) {
        const existingScaf = findScaffoldingMsgByContent(data.content || "");
        if (existingScaf) {
          if (mid) existingScaf.dataset.id = mid;
          return;
        }
        const elScaf = appendMsg("user", data.content);
        if (mid) elScaf.dataset.id = mid;
        return;
      }
      if (adoptOptimisticUser(data.content, mid)) return;
      // Optimistic bubble may already carry a disk id from an early catchUp/merge.
      // If the newest You line already shows this content, do not append a second bubble.
      // Prefer keeping a real id over stamping build-live-*.
      const userNodes = els.feed.querySelectorAll(".msg.user");
      const lastUser = userNodes.length ? userNodes[userNodes.length - 1] : null;
      if (lastUser) {
        const body = lastUser.querySelector(".body");
        const raw = body && (body.dataset.raw != null ? body.dataset.raw : body.textContent);
        if (normalizeUserContent(raw || "") === normalizeUserContent(data.content || "")) {
          const prevId = lastUser.dataset.id || "";
          if (mid && (!prevId || prevId.indexOf("build-live-") === 0)) {
            lastUser.dataset.id = mid;
          }
          return;
        }
      }
      const el = appendMsg("user", data.content);
      if (mid) el.dataset.id = mid;
      return;
    }
    if (t === "assistant_start") {
      setTurnActive(true);
      streamingTools = [];
      streamingEl = appendMsg("assistant", "", true);
      return;
    }
    if (t === "assistant_delta") {
      const follow = nearBottom();
      if (!streamingEl) streamingEl = appendMsg("assistant", "", true);
      const body = streamingEl.querySelector(".body");
      setMsgBody(body, (body.dataset.raw || "") + (data.delta || ""));
      if (follow) scrollFeed();
      else updateJumpBottom();
      return;
    }
    if (t === "tool_call" || t === "tool_result" || t === "tool_card") {
      ensureStreaming();
      applyToolEvent(streamingTools, t, data.tool || {});
      renderTools(streamingEl, streamingTools);
      return;
    }
    if (t === "assistant_done") {
      if (streamingEl) {
        streamingEl.classList.remove("streaming");
        if (data.content) setMsgBody(streamingEl, data.content);
        // Synthetic id until catchUp/snapshot stamps the disk id (dedupe path).
        if (!streamingEl.dataset.id) {
          streamingEl.dataset.id = "build-live-asst-" + Date.now().toString(36);
        }
        if (data.cancelled || data.error === "cancelled") markCancelled(streamingEl);
        if (data.error && data.error !== "cancelled") {
          const note = document.createElement("div");
          note.className = "cancel-note";
          note.textContent = data.hint || data.error;
          streamingEl.appendChild(note);
        }
      } else if (data.error && data.error !== "cancelled") {
        const el = appendMsg("assistant", data.error);
        if (data.hint) {
          const note = document.createElement("div");
          note.className = "cancel-note";
          note.textContent = data.hint;
          el.appendChild(note);
        }
      }
      streamingEl = null;
      streamingTools = null;
      setTurnActive(false);
      if (isLooking(activeId)) clearUnread(activeId);
      else if (activeId) markUnread(activeId);
      refreshSessions().catch(() => {});
      catchUp();
      return;
    }
    if (t === "error") {
      setTurnActive(false);
      const msg = data.error || "error";
      const el = appendMsg("assistant", msg);
      if (data.hint) {
        const note = document.createElement("div");
        note.className = "cancel-note";
        note.textContent = data.hint;
        el.appendChild(note);
      }
      return;
    }
  }

  /** Unwrap complete <user_query>…</user_query> (case-insensitive) to inner text. */
  function unwrapUserQueryText(raw) {
    const s = String(raw == null ? "" : raw);
    const re = /<user_query>([\s\S]*?)<\/user_query>/gi;
    let out = "";
    let last = 0;
    let found = false;
    let m;
    while ((m = re.exec(s)) !== null) {
      found = true;
      if (m.index > last) out += s.slice(last, m.index);
      out += m[1];
      last = m.index + m[0].length;
    }
    if (!found) return s;
    if (last < s.length) out += s.slice(last);
    return out;
  }

  /** Plain text for optimistic-user / merge dedupe.
   *  When <user_query> is present, compare joined inners so wrapped server
   *  content matches the optimistic plain bubble.
   */
  function normalizeUserContent(raw) {
    const s = String(raw == null ? "" : raw);
    const re = /<user_query>([\s\S]*?)<\/user_query>/gi;
    const parts = [];
    let m;
    while ((m = re.exec(s)) !== null) parts.push(m[1]);
    if (parts.length) return parts.join("\n").trim();
    return s.trim();
  }

  function adoptOptimisticUser(content, messageId) {
    const want = normalizeUserContent(content);
    if (!want) return false;
    const nodes = els.feed.querySelectorAll(".msg.user");
    // Newest-first: only retarget synthetic bubbles (no id or build-live-*).
    // Never adopt an older real server id — caller should append a new You bubble.
    for (let i = nodes.length - 1; i >= 0; i--) {
      const el = nodes[i];
      const body = el.querySelector(".body");
      const raw = body && (body.dataset.raw != null ? body.dataset.raw : body.textContent);
      if (normalizeUserContent(raw || "") !== want) continue;
      const id = el.dataset.id || "";
      if (messageId && id === messageId) return true;
      if (!id || id.indexOf("build-live-") === 0) {
        if (messageId) el.dataset.id = messageId;
        return true;
      }
      // Newest content match is a real disk/server id — do not collapse.
      return false;
    }
    return false;
  }

  function normalizeAssistantContent(raw) {
    return String(raw == null ? "" : raw).trim();
  }

  /**
   * Adopt a live/streaming assistant bubble into a disk/server message id.
   * After assistant_done, streamingEl is cleared but the bubble often has no
   * data-id yet — catchUp / session.snapshot must not append a second Grok line
   * with the same text (common when tools/sources rendered under the live turn).
   * Skip .grok-thoughts-msg scaffolding cards (also classed as assistant).
   */
  function adoptLiveAssistant(content, messageId, tools) {
    const want = normalizeAssistantContent(content);
    if (!want) return false;
    if (streamingEl && !streamingEl.dataset.id) {
      streamingEl.classList.remove("streaming");
      setMsgBody(streamingEl, content || "");
      if (messageId) streamingEl.dataset.id = messageId;
      if (tools && tools.length) renderTools(streamingEl, tools);
      streamingEl = null;
      streamingTools = null;
      return true;
    }
    const nodes = els.feed.querySelectorAll(".msg.assistant:not(.grok-thoughts-msg)");
    for (let i = nodes.length - 1; i >= 0; i--) {
      const el = nodes[i];
      const body = el.querySelector(".body");
      const raw = body && (body.dataset.raw != null ? body.dataset.raw : body.textContent);
      if (normalizeAssistantContent(raw || "") !== want) continue;
      const id = el.dataset.id || "";
      if (messageId && id === messageId) {
        if (tools && tools.length) renderTools(el, tools);
        return true;
      }
      if (!id || id.indexOf("build-live-") === 0) {
        if (messageId) el.dataset.id = messageId;
        if (tools && tools.length) renderTools(el, tools);
        return true;
      }
      // Newest content match is a real server id — do not collapse.
      return false;
    }
    return false;
  }

  function ensureStreaming() {
    if (!streamingEl) {
      streamingTools = streamingTools || [];
      streamingEl = appendMsg("assistant", "", true);
    }
  }

  const TOOL_JSON_MAX = 2400;

  function toolCorrId(tool) {
    if (!tool) return "";
    return String(tool.id || tool.call_id || "");
  }

  function toolIsOpen(tool) {
    if (!tool) return false;
    if (tool.result !== undefined) return false;
    const st = tool.status;
    return st !== "done" && st !== "error";
  }

  function classifyToolEvent(type, tool) {
    if (type === "tool_call") return "tool_call";
    if (type === "tool_result") return "tool_result";
    if (type === "tool_card") {
      if (tool && (tool.result !== undefined || tool.status === "done" || tool.status === "error" || tool.error)) {
        return "tool_result";
      }
      return "tool_call";
    }
    return "";
  }

  function applyToolEvent(tools, type, tool) {
    if (!tools || !tool) return tools;
    const kind = classifyToolEvent(type, tool);
    if (!kind) return tools;
    const id = toolCorrId(tool);
    let idx = -1;
    if (id) {
      idx = tools.findIndex((x) => toolCorrId(x) === id);
    }
    // Name fallback: results (or calls without id). Never collapse distinct ids on call.
    if (idx < 0 && tool.name && (kind === "tool_result" || !id)) {
      idx = tools.findIndex((x) => x.name === tool.name && toolIsOpen(x));
    }
    if (kind === "tool_call") {
      const row = Object.assign({}, idx >= 0 ? tools[idx] : {}, tool);
      if (!tool.status) row.status = "running";
      if (idx >= 0) tools[idx] = row;
      else tools.push(row);
      return tools;
    }
    let status = "done";
    if (tool.status === "error" || (tool.error != null && String(tool.error) !== "")) status = "error";
    const row = Object.assign({}, idx >= 0 ? tools[idx] : {}, tool, { status: status });
    if (idx >= 0) tools[idx] = row;
    else tools.push(row);
    return tools;
  }

  function formatToolJSON(value) {
    if (value === undefined) return "";
    let raw;
    try {
      raw = typeof value === "string" ? value : JSON.stringify(value, null, 2);
    } catch (_) {
      raw = String(value);
    }
    if (raw.length > TOOL_JSON_MAX) {
      return raw.slice(0, TOOL_JSON_MAX) + "\n… truncated (" + raw.length + " chars)";
    }
    return raw;
  }

  function toolStatusLabel(tool) {
    const st = tool.status || (tool.result !== undefined ? "done" : "running");
    if (st === "error") return "error";
    if (st === "done") return "done";
    return "running";
  }

  function renderTools(el, tools) {
    let box = el.querySelector(".tools");
    if (!box) {
      box = document.createElement("div");
      box.className = "tools";
      el.appendChild(box);
    }
    const openKeys = new Set();
    box.querySelectorAll("details[open]").forEach((d) => {
      if (d.dataset.key) openKeys.add(d.dataset.key);
    });
    box.textContent = "";
    tools.forEach((tool, i) => {
      const status = toolStatusLabel(tool);
      const card = document.createElement("div");
      card.className = "tool-card status-" + status;
      const tid = toolCorrId(tool);
      if (tid) card.dataset.toolId = tid;

      const header = document.createElement("header");
      const glyph = document.createElement("span");
      glyph.className = "tool-glyph";
      glyph.setAttribute("aria-hidden", "true");
      glyph.textContent = "⚙";
      const nameEl = document.createElement("span");
      nameEl.className = "tool-name";
      nameEl.textContent = tool.name || "tool";
      const badge = document.createElement("span");
      badge.className = "tool-status";
      badge.textContent = status;
      header.appendChild(glyph);
      header.appendChild(nameEl);
      header.appendChild(badge);
      card.appendChild(header);

      if (tool.arguments !== undefined) {
        const key = (tid || tool.name || i) + ":args";
        const det = document.createElement("details");
        det.className = "tool-section";
        det.dataset.key = key;
        if (openKeys.has(key) || (status === "running" && !openKeys.size)) det.open = true;
        const sum = document.createElement("summary");
        sum.textContent = "Arguments";
        const pre = document.createElement("pre");
        pre.textContent = formatToolJSON(tool.arguments);
        det.appendChild(sum);
        det.appendChild(pre);
        card.appendChild(det);
      }

      if (tool.result !== undefined || tool.error !== undefined || status === "done" || status === "error") {
        const key = (tid || tool.name || i) + ":result";
        const det = document.createElement("details");
        det.className = "tool-section";
        det.dataset.key = key;
        if (openKeys.has(key) || status === "error") det.open = true;
        const sum = document.createElement("summary");
        sum.textContent = status === "error" ? "Error" : "Result";
        const pre = document.createElement("pre");
        if (tool.error !== undefined && (tool.result === undefined || status === "error")) {
          pre.textContent = formatToolJSON(tool.error);
        } else if (tool.result !== undefined) {
          pre.textContent = formatToolJSON(tool.result);
        } else {
          pre.textContent = "(no result)";
        }
        det.appendChild(sum);
        det.appendChild(pre);
        card.appendChild(det);
      } else if (status === "running") {
        const hint = document.createElement("div");
        hint.className = "tool-running-hint";
        hint.textContent = "Running…";
        card.appendChild(hint);
      }

      box.appendChild(card);
    });
    if (!suppressAutoScroll && nearBottom()) scrollFeed();
    else updateJumpBottom();
  }

  function escapeHtml(s) {
    return String(s)
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;")
      .replace(/'/g, "&#39;");
  }

  /** Complete **pairs** only → <strong>; unmatched ** stay literal. */
  function formatBold(escaped) {
    return String(escaped || "").replace(/\*\*([\s\S]+?)\*\*/g, "<strong>$1</strong>");
  }

  /**
   * ATX headers (MD-style), line-based, after escapeHtml:
   * optional leading whitespace, then # / ## + space, then rest of line.
   * Markers are stripped; content stays escaped. ## → h2 (required),
   * # → h1 (same path). ###+ left literal.
   */
  function formatAtxHeaders(escaped) {
    return String(escaped || "").replace(
      /^[ \t]*(#{1,2})[ \t]+([^\n]+)/gm,
      function (_m, hashes, content) {
        const level = hashes.length;
        return (
          '<span class="msg-h' +
          level +
          '" role="heading" aria-level="' +
          level +
          '">' +
          content +
          "</span>"
        );
      }
    );
  }


  /** Known Grok Build context-injection wrapper tags (UI presentation only). */
  var GROK_SCAFFOLD_TAGS = [
    "user_info",
    "git_status",
    "rules",
    "user_rules",
    "agent_skills",
    "system_reminder",
    "system-reminder",
    "open_and_recently_viewed_files",
    "agent_transcripts",
    "mcp_file_system",
    "attached_files",
    "browser_content"
  ];

  /**
   * Conservative heuristic: treat content as Grok Build scaffolding when it
   * starts with a known wrapper tag and is primarily those blocks — not a
   * normal human message that merely mentions a tag name in prose.
   * Messages containing <user_query> are real user turns (unwrap path).
   */
  function isGrokScaffolding(raw) {
    const s = String(raw == null ? "" : raw).trim();
    if (s.length < 12) return false;
    if (/<user_query[\s>]/i.test(s)) return false;
    const names = GROK_SCAFFOLD_TAGS.join("|");
    if (!new RegExp("^<(" + names + ")(\\s[^>]*)?>", "i").test(s)) return false;
    let stripped = s;
    for (let i = 0; i < GROK_SCAFFOLD_TAGS.length; i++) {
      const name = GROK_SCAFFOLD_TAGS[i];
      const block = new RegExp(
        "<" + name + "(?:\\s[^>]*)?>[\\s\\S]*?<\\/" + name + ">",
        "gi"
      );
      stripped = stripped.replace(block, "\n");
      stripped = stripped.replace(
        new RegExp("<\\/?" + name + "(?:\\s[^>]*)?>", "gi"),
        "\n"
      );
    }
    const leftover = stripped.replace(/\s+/g, " ").trim();
    // Mostly tag-wrapped, or only trivial leftovers outside blocks.
    return leftover.length < 80;
  }

  /** Collapsible card: summary "Grok thoughts", body = escaped original. */
  function formatGrokThoughtsHTML(raw) {
    const escaped = escapeHtml(raw == null ? "" : String(raw));
    return (
      '<details class="grok-thoughts">' +
      '<summary class="grok-thoughts-summary">Grok thoughts</summary>' +
      '<pre class="grok-thoughts-body">' +
      escaped +
      "</pre></details>"
    );
  }

  /** Restyle an existing .msg bubble as Grok-attributed scaffolding. */
  function presentAsGrokThoughts(msgEl) {
    if (!msgEl || !msgEl.classList) return;
    if (!msgEl.dataset.origRole) {
      if (msgEl.classList.contains("user")) msgEl.dataset.origRole = "user";
      else if (msgEl.classList.contains("system")) msgEl.dataset.origRole = "system";
      else if (msgEl.classList.contains("assistant")) msgEl.dataset.origRole = "assistant";
    }
    msgEl.classList.remove("user", "system", "unknown");
    msgEl.classList.add("assistant", "grok-thoughts-msg");
    msgEl.dataset.scaffolding = "1";
    const roleEl = msgEl.querySelector(".role");
    if (roleEl) roleEl.textContent = "Grok";
  }

  function findScaffoldingMsgByContent(content) {
    const want = String(content == null ? "" : content).trim();
    if (!want) return null;
    const nodes = els.feed.querySelectorAll(".msg.grok-thoughts-msg");
    for (let i = nodes.length - 1; i >= 0; i--) {
      const body = nodes[i].querySelector(".body");
      const raw =
        body && (body.dataset.raw != null ? body.dataset.raw : body.textContent);
      if (String(raw || "").trim() === want) return nodes[i];
    }
    return null;
  }


  /** Escaped text → ATX headers, then bold. Tiny md subset only. */
  function formatMdSubset(escaped) {
    return formatBold(formatAtxHeaders(escaped));
  }

  /**
   * XSS-safe message HTML: escape first, then ATX headers + bold; wrap
   * complete <system-reminder> blocks (case-insensitive) as muted asides.
   * Complete <user_query>…</user_query> is unwrapped into the normal
   * message body (no labeled aside). Lone/unclosed tags stay escaped.
   */
  function formatTaggedAside(kind, inner) {
    const k = String(kind || "").toLowerCase();
    if (k === "system-reminder") {
      return (
        '<aside class="sys-reminder" role="note">' +
        '<div class="sys-reminder-label">System reminder</div>' +
        '<div class="sys-reminder-body">' +
        formatMdSubset(escapeHtml(inner)) +
        "</div></aside>"
      );
    }
    return formatMdSubset(escapeHtml(inner));
  }

  function formatMessageHTML(raw) {
    // Unwrap user_query into plain body text before formatting asides.
    const s = unwrapUserQueryText(raw);
    const re = /<(system-reminder)>([\s\S]*?)<\/\1>/gi;
    let html = "";
    let last = 0;
    let m;
    while ((m = re.exec(s)) !== null) {
      if (m.index > last) {
        html += formatMdSubset(escapeHtml(s.slice(last, m.index)));
      }
      html += formatTaggedAside(m[1], m[2]);
      last = m.index + m[0].length;
    }
    if (last < s.length) {
      html += formatMdSubset(escapeHtml(s.slice(last)));
    }
    return html;
  }

  /** Set .body from raw text via formatMessageHTML; keep data-raw for streaming. */
  function setMsgBody(el, raw) {
    if (!el) return;
    const body = el.classList && el.classList.contains("body") ? el : el.querySelector(".body");
    if (!body) return;
    const text = raw == null ? "" : String(raw);
    body.dataset.raw = text;
    if (isGrokScaffolding(text)) {
      body.innerHTML = formatGrokThoughtsHTML(text);
      const msg = body.closest ? body.closest(".msg") : null;
      if (msg) presentAsGrokThoughts(msg);
      return;
    }
    body.innerHTML = formatMessageHTML(text);
  }

  function safeMsgRole(role) {
    const r = String(role || "");
    if (r === "user" || r === "assistant" || r === "system") return r;
    return "unknown";
  }

  function roleLabel(role) {
    if (role === "user") return "You";
    if (role === "assistant") return "Grok";
    if (role === "system") return "System";
    return role;
  }

  let suppressAutoScroll = false;

  function appendMsg(role, content, streaming) {
    const follow = !suppressAutoScroll && nearBottom();
    clearEmpty();
    const safeRole = safeMsgRole(role);
    const text = content || "";
    const scaffolding = !streaming && isGrokScaffolding(text);
    const displayRole = scaffolding ? "assistant" : safeRole;
    const div = document.createElement("div");
    div.className =
      "msg " +
      displayRole +
      (scaffolding ? " grok-thoughts-msg" : "") +
      (streaming ? " streaming" : "");
    if (scaffolding) {
      div.dataset.scaffolding = "1";
      div.dataset.origRole = safeRole;
    }
    const roleEl = document.createElement("div");
    roleEl.className = "role";
    roleEl.textContent = scaffolding ? "Grok" : roleLabel(safeRole);
    const bodyEl = document.createElement("div");
    bodyEl.className = "body";
    if (scaffolding) {
      bodyEl.dataset.raw = text;
      bodyEl.innerHTML = formatGrokThoughtsHTML(text);
    } else {
      setMsgBody(bodyEl, text);
    }
    div.appendChild(roleEl);
    div.appendChild(bodyEl);
    els.feed.appendChild(div);
    if (follow) scrollFeed();
    else updateJumpBottom();
    return div;
  }

  function clearEmpty() {
    const e = els.feed.querySelector(".empty");
    if (e) e.remove();
  }

  /** Scroll feed to bottom. Use { instant: true } on session open so CSS
   * scroll-behavior:smooth does not animate from the top of the transcript. */
  function scrollFeed(opts) {
    const instant = !!(opts && opts.instant);
    const feed = els.feed;
    if (instant) {
      feed.style.scrollBehavior = "auto";
      feed.scrollTop = feed.scrollHeight;
      updateJumpBottom();
      requestAnimationFrame(() => {
        feed.style.scrollBehavior = "";
      });
      return;
    }
    feed.scrollTop = feed.scrollHeight;
    updateJumpBottom();
  }

  function pinFeedToBottom() {
    scrollFeed({ instant: true });
  }

  function nearBottom() {
    return els.feed.scrollHeight - els.feed.scrollTop - els.feed.clientHeight < 96;
  }

  function updateJumpBottom() {
    if (activeId && nearBottom() && !document.hidden && unreadIds.has(activeId)) {
      clearUnread(activeId);
      renderSessionList();
    }
    const btn = els.btnJumpBottom;
    if (!btn) return;
    const hasMsgs = !!els.feed.querySelector(".msg");
    btn.hidden = !(hasMsgs && !nearBottom());
  }

  let jumpBottomRaf = 0;
  function onFeedScroll() {
    if (jumpBottomRaf) return;
    jumpBottomRaf = requestAnimationFrame(() => {
      jumpBottomRaf = 0;
      updateJumpBottom();
    });
  }

  function emptyStateHtml(title, body) {
    return (
      '<div class="empty">' +
      '<div class="empty-mark" aria-hidden="true">' +
      '<svg viewBox="0 0 32 32" width="22" height="22" fill="none">' +
      '<circle cx="16" cy="16" r="11" stroke="currentColor" stroke-width="1.5" opacity="0.35"/>' +
      '<circle cx="16" cy="16" r="5.5" stroke="currentColor" stroke-width="1.5"/>' +
      '<circle cx="16" cy="5" r="1.6" fill="currentColor"/>' +
      "</svg></div>" +
      "<h2>" + escapeHtml(title) + "</h2>" +
      "<p>" + escapeHtml(body) + "</p></div>"
    );
  }

  function renderEmptySession() {
    els.feed.innerHTML = emptyStateHtml(
      "Start a session",
      "New chat on the left — same history on desktop and phone. The hub keeps the transcript."
    );
    updateJumpBottom();
  }

  function renderTranscript(session) {
    els.feed.innerHTML = "";
    streamingEl = null;
    streamingTools = null;
    setTurnActive(false);
    const msgs = session.messages || [];
    if (!msgs.length) {
      els.feed.innerHTML = emptyStateHtml(
        "Continue anywhere",
        "Same session on desktop and phone — history lives on the hub."
      );
      updateJumpBottom();
      return;
    }
    // Bulk-render without per-message follow scrolls, then pin instantly so the
    // first paint is already at the latest message (no smooth scroll from top).
    suppressAutoScroll = true;
    try {
      msgs.forEach((m) => {
        const el = appendMsg(m.role, m.content || "", false);
        if (m.id) el.dataset.id = m.id;
        if (m.tools && m.tools.length) renderTools(el, m.tools);
        if (m.cancelled) markCancelled(el);
      });
    } finally {
      suppressAutoScroll = false;
    }
    pinFeedToBottom();
  }

  /** Merge server transcript into the feed without wiping scroll awkwardly. */
  function mergeTranscript(session) {
    const msgs = session.messages || [];
    if (!msgs.length) {
      if (!els.feed.querySelector(".msg")) {
        els.feed.innerHTML = emptyStateHtml(
          "Continue anywhere",
          "Same session on desktop and phone — history lives on the hub."
        );
      }
      updateJumpBottom();
      return;
    }
    const stick = nearBottom();
    clearEmpty();
    const have = new Set(
      Array.prototype.map.call(els.feed.querySelectorAll(".msg[data-id]"), (el) => el.dataset.id)
    );
    msgs.forEach((m) => {
      if (m.id && have.has(m.id)) return;
      if (m.role === "user") {
        if (isGrokScaffolding(m.content || "")) {
          const existingScaf = findScaffoldingMsgByContent(m.content || "");
          if (existingScaf) {
            if (m.id) {
              existingScaf.dataset.id = m.id;
              have.add(m.id);
            }
            return;
          }
        }
        if (adoptOptimisticUser(m.content, m.id)) {
          if (m.id) have.add(m.id);
          return;
        }
        // Consecutive identical user bubble with a different id (Build live vs disk).
        const userNodes = els.feed.querySelectorAll(".msg.user");
        const lastUser = userNodes.length ? userNodes[userNodes.length - 1] : null;
        if (lastUser) {
          const body = lastUser.querySelector(".body");
          const raw = body && (body.dataset.raw != null ? body.dataset.raw : body.textContent);
          if (normalizeUserContent(raw || "") === normalizeUserContent(m.content || "")) {
            const prevId = lastUser.dataset.id || "";
            if (prevId && prevId.indexOf("build-live-") !== 0 && prevId !== m.id) {
              // Distinct real server messages with same text — keep both.
            } else {
              if (m.id) {
                lastUser.dataset.id = m.id;
                have.add(m.id);
              }
              if (prevId) have.delete(prevId);
              return;
            }
          }
        }
      }
      if (m.role === "assistant") {
        // Live stream still open, or finished bubble with no/synthetic id after
        // assistant_done + catchUp/snapshot (same class of bug as You-bubble dedupe).
        if (adoptLiveAssistant(m.content, m.id, m.tools)) {
          if (m.id) have.add(m.id);
          return;
        }
      }
      const el = appendMsg(m.role, m.content || "", false);
      if (m.id) {
        el.dataset.id = m.id;
        have.add(m.id);
      }
      if (m.tools && m.tools.length) renderTools(el, m.tools);
    });
    if (stick) scrollFeed();
    else updateJumpBottom();
  }

  async function catchUp() {
    if (!token || !activeId || catchingUp) return;
    catchingUp = true;
    try {
      const res = await api("/api/sessions/" + encodeURIComponent(activeId));
      if (!res.ok) return;
      const sess = await res.json();
      if (sess.id !== activeId) return;
      if (sess.title) els.chatTitle.textContent = sess.title;
      mergeTranscript(sess);
      updateCtxMeter(sess);
    } catch (_) {
      // Transient network — next poll / reconnect will retry
    } finally {
      catchingUp = false;
    }
  }

  function syncGrokOnlyToggle() {
    if (!els.grokOnlyToggle) return;
    els.grokOnlyToggle.checked = includeGrokOnly;
  }

  function setSettingsOpen(open) {
    if (!els.settingsModal || !els.btnSettings) return;
    els.settingsModal.hidden = !open;
    els.btnSettings.setAttribute("aria-expanded", open ? "true" : "false");
    if (open && els.demoToggle) els.demoToggle.checked = DEMO;
  }

  function isLooking(id) {
    return !!(id && id === activeId && !document.hidden && nearBottom());
  }

  function markUnread(id) {
    if (!id || isLooking(id)) return;
    unreadIds.add(id);
  }

  function clearUnread(id) {
    if (!id) return;
    unreadIds.delete(id);
  }

  function noteSessionCounts(list) {
    list.forEach((s) => {
      const next = s.message_count || 0;
      const prev = seenCounts[s.id];
      if (countsPrimed && prev != null && next > prev) {
        const recentSelf = selfSendAt[s.id] && (Date.now() - selfSendAt[s.id] < 8000) && next === prev + 1;
        if (isLooking(s.id)) clearUnread(s.id);
        else if (!recentSelf) markUnread(s.id);
      }
      seenCounts[s.id] = next;
    });
    countsPrimed = true;
  }

  function startLiveRefresh() {
    if (liveTimer) return;
    liveTimer = setInterval(() => {
      if (!token) return;
      refreshSessions()
        .then(() => { if (activeId) return catchUp(); })
        .catch(() => {});
    }, LIVE_MS);
  }

  async function refreshSessions() {
    const q = includeGrokOnly ? "?include=grok-only" : "";
    const res = await api("/api/sessions" + q);
    const data = await res.json();
    sessions = data.sessions || [];
    noteSessionCounts(sessions);
    syncGrokOnlyToggle();
    renderSessionList();
  }

  function relativeTime(ts) {
    if (!ts) return "";
    const sec = typeof ts === "number" ? ts : Date.parse(ts) / 1000;
    if (!sec || Number.isNaN(sec)) return "";
    const now = Date.now() / 1000;
    const d = Math.max(0, Math.floor(now - sec));
    if (d < 45) return "just now";
    if (d < 3600) return Math.floor(d / 60) + "m";
    if (d < 86400) return Math.floor(d / 3600) + "h";
    if (d < 86400 * 7) return Math.floor(d / 86400) + "d";
    return Math.floor(d / (86400 * 7)) + "w";
  }

  function isBuildSession(sOrId) {
    if (!sOrId) return false;
    if (typeof sOrId === "string") return sOrId.indexOf("build:") === 0;
    return sOrId.source === "build" || (sOrId.id && String(sOrId.id).indexOf("build:") === 0);
  }

  function setComposerReadOnly(on, hint) {
    const ro = !!on;
    if (els.input) {
      els.input.disabled = ro;
      els.input.placeholder = ro
        ? (hint || "Unavailable")
        : (isBuildSession(activeId) ? "Message Grok Build…" : "Message Grok…");
    }
    if (ro) {
      if (els.btnSend) els.btnSend.disabled = true;
    } else {
      syncPrimaryButton();
    }
    let banner = document.getElementById("buildRoHint");
    if (ro) {
      if (!banner) {
        banner = document.createElement("div");
        banner.id = "buildRoHint";
        banner.className = "build-ro-hint";
        banner.dataset.lockOnly = "1";
        banner.setAttribute("role", "status");
        const host = els.composer && els.composer.parentNode;
        if (host) host.insertBefore(banner, els.composer);
      }
      banner.textContent = hint || "Composer locked.";
      banner.hidden = false;
    } else if (banner) {
      banner.hidden = true;
    }
  }

  function setBuildSessionChrome(on) {
    if (els.ctxSub) {
      els.ctxSub.hidden = !on;
      els.ctxSub.textContent = on ? "via Grok Build" : "";
    }
    // Remove legacy composer banner if present
    const banner = document.getElementById("buildRoHint");
    if (banner && !banner.dataset.lockOnly) {
      banner.remove();
    }
    if (els.input) {
      els.input.placeholder = on ? "Message Grok Build…" : "Message Grok…";
    }
  }

  const OLDER_AFTER_SEC = 7 * 86400;
  let olderOpen = false;

  function sessionEpoch(s) {
    const raw = s && (s.updated_at || s.created_at);
    if (raw == null || raw === "") return 0;
    if (typeof raw === "number") return raw > 1e12 ? raw / 1000 : raw;
    const ms = Date.parse(raw);
    return Number.isNaN(ms) ? 0 : ms / 1000;
  }

  function isOlderSession(s) {
    const sec = sessionEpoch(s);
    if (!sec) return false;
    return (Date.now() / 1000 - sec) >= OLDER_AFTER_SEC;
  }

  function sessionRow(s) {
    const btn = document.createElement("button");
    btn.type = "button";
    const build = isBuildSession(s);
    const unread = unreadIds.has(s.id);
    btn.className = "session-item" + (s.id === activeId ? " active" : "") + (build ? " build" : "") + (unread ? " unread" : "");
    if (unread) btn.setAttribute("aria-label", (s.title || "Untitled") + ", unread");
    btn.innerHTML = '<div class="t-row"><div class="t"></div></div><div class="m"></div>';
    if (unread) {
      const dot = document.createElement("span");
      dot.className = "unread-dot";
      dot.setAttribute("aria-hidden", "true");
      btn.appendChild(dot);
    }
    btn.querySelector(".t").textContent = s.title || "Untitled";
    if (build && !s.grok_only) {
      const badge = document.createElement("span");
      badge.className = "src-badge build";
      badge.textContent = "Build";
      badge.title = "Grok Build session";
      btn.querySelector(".t-row").appendChild(badge);
    }
    if (s.grok_only) {
      const badge = document.createElement("span");
      const kind = String(s.session_kind || "").toLowerCase();
      const isFork = kind === "fork" || kind.indexOf("fork") !== -1;
      badge.className = "src-badge subagent" + (isFork ? " fork" : "");
      badge.textContent = isFork ? "Subagent fork" : "Subagent";
      badge.title = isFork
        ? "Subagent fork — agent-to-agent child session"
        : "Subagent — agent-to-agent child session";
      btn.querySelector(".t-row").appendChild(badge);
    }
    if (s.parent_session_id) {
      const parentId = String(s.parent_session_id).indexOf("build:") === 0
        ? String(s.parent_session_id)
        : "build:" + String(s.parent_session_id);
      const parentJump = document.createElement("span");
      parentJump.className = "parent-jump";
      parentJump.textContent = "Parent";
      parentJump.title = "Open parent Build chat";
      parentJump.setAttribute("role", "link");
      parentJump.tabIndex = 0;
      const goParent = (e) => {
        e.preventDefault();
        e.stopPropagation();
        openSession(parentId);
      };
      parentJump.addEventListener("click", goParent);
      parentJump.addEventListener("keydown", (e) => {
        if (e.key === "Enter" || e.key === " ") goParent(e);
      });
      btn.querySelector(".t-row").appendChild(parentJump);
    }
    const meta = btn.querySelector(".m");
    const count = (s.message_count || 0) + " msg";
    const rel = relativeTime(s.updated_at || s.created_at);
    meta.textContent = "";
    const c = document.createElement("span");
    c.textContent = count;
    meta.appendChild(c);
    if (rel) {
      const dot = document.createElement("span");
      dot.className = "dot";
      dot.setAttribute("aria-hidden", "true");
      meta.appendChild(dot);
      const r = document.createElement("span");
      r.textContent = rel;
      meta.appendChild(r);
    }
    btn.onclick = () => openSession(s.id);
    return btn;
  }

  function renderSessionList() {
    els.sessionList.innerHTML = "";
    const recent = [];
    const older = [];
    sessions.forEach((s) => (isOlderSession(s) ? older : recent).push(s));
    recent.forEach((s) => els.sessionList.appendChild(sessionRow(s)));
    if (!older.length) return;
    const activeIsOlder = older.some((s) => s.id === activeId);
    const expanded = olderOpen || activeIsOlder;
    const wrap = document.createElement("div");
    wrap.className = "older-group";
    const toggle = document.createElement("button");
    toggle.type = "button";
    toggle.className = "older-toggle";
    toggle.setAttribute("aria-expanded", expanded ? "true" : "false");
    toggle.setAttribute("aria-controls", "olderSessionList");
    const n = older.length;
    const label = "Older — " + n + " chat" + (n === 1 ? "" : "s");
    toggle.innerHTML = '<span class="older-chevron" aria-hidden="true"></span><span class="older-label"></span>';
    toggle.querySelector(".older-label").textContent = label;
    const list = document.createElement("div");
    list.id = "olderSessionList";
    list.className = "older-list";
    list.hidden = !expanded;
    older.forEach((s) => list.appendChild(sessionRow(s)));
    toggle.onclick = () => {
      olderOpen = !expanded;
      renderSessionList();
    };
    wrap.appendChild(toggle);
    wrap.appendChild(list);
    els.sessionList.appendChild(wrap);
  }

  async function openSession(id) {
    activeId = id;
    clearUnread(id);
    renderSessionList();
    closeMenu();
    const res = await api("/api/sessions/" + encodeURIComponent(id));
    if (!res.ok) return;
    const sess = await res.json();
    els.chatTitle.textContent = sess.title || "Chat";
    renderTranscript(sess);
    updateJumpBottom();
    updateCtxMeter(sess);
    subscribeSession(id);
    const build = isBuildSession(sess) || isBuildSession(id);
    setComposerReadOnly(false);
    setBuildSessionChrome(build);
    els.btnSend.disabled = false;
    setTurnActive(false);
    try { history.replaceState(null, "", (DEMO ? "/?demo=1" : "/") + "#s=" + encodeURIComponent(id)); } catch (_) {}
  }

  async function newSession() {
    const payload = DEMO
      ? { title: "Demo chat", demo: true }
      : { title: "New chat" };
    const res = await api("/api/sessions", {
      method: "POST",
      body: JSON.stringify(payload),
    });
    const sess = await res.json().catch(() => ({}));
    if (!res.ok) {
      const msg = sess.hint || sess.error || "Could not create a Grok Build session";
      showCreateError(msg);
      return;
    }
    hideCreateError();
    await refreshSessions();
    await openSession(sess.id);
  }

  function showCreateError(msg) {
    let banner = document.getElementById("newSessionError");
    if (!banner) {
      banner = document.createElement("div");
      banner.id = "newSessionError";
      banner.className = "build-ro-hint";
      banner.setAttribute("role", "status");
      const host = els.composer && els.composer.parentNode;
      if (host) host.insertBefore(banner, els.composer);
    }
    banner.textContent = msg;
    banner.hidden = false;
  }

  function hideCreateError() {
    const banner = document.getElementById("newSessionError");
    if (banner) banner.hidden = true;
  }

  async function sendMessage(text) {
    if (!text || !activeId || sending || turnActive) return;
    sending = true;
    selfSendAt[activeId] = Date.now();
    els.btnSend.disabled = true;
    setTurnActive(true);
    try {
      if (wsIsOpen()) {
        // Optimistic You bubble (same as HTTP path). user_message / mergeTranscript
        // adoptOptimisticUser stamps data-id or retargets build-live → disk id.
        appendMsg("user", text);
        ws.send(JSON.stringify({ type: "chat.send", session_id: activeId, content: text }));
        els.input.value = "";
        autosize();
        return;
      }
      setWsPill(statusForDegraded());
      const localEl = appendMsg("user", text);
      const res = await api("/api/sessions/" + encodeURIComponent(activeId) + "/messages", {
        method: "POST",
        body: JSON.stringify({ content: text }),
      });
      const data = await res.json().catch(() => ({}));
      if (!res.ok) {
        localEl.classList.add("failed");
        throw new Error(data.error || "send failed");
      }
      if (data.message_id) localEl.dataset.id = data.message_id;
      els.input.value = "";
      autosize();
      ensurePoll();
      catchUp();
    } catch (err) {
      console.error(err);
      setWsPill(statusForDegraded());
      setTurnActive(false);
    } finally {
      sending = false;
      syncPrimaryButton();
    }
  }

  async function cancelTurn() {
    if (!token || !activeId || !turnActive) return;
    if (els.btnSend) els.btnSend.disabled = true;
    try {
      const res = await api("/api/sessions/" + encodeURIComponent(activeId) + "/cancel", {
        method: "POST",
        body: "{}",
      });
      if (!res.ok) {
        const data = await res.json().catch(() => ({}));
        console.error("cancel failed", data);
        syncPrimaryButton();
      }
    } catch (err) {
      console.error(err);
      syncPrimaryButton();
    }
  }

  els.feed.addEventListener("scroll", onFeedScroll, { passive: true });
  if (els.btnJumpBottom) {
    els.btnJumpBottom.addEventListener("click", () => {
      scrollFeed();
    });
  }

  els.composer.addEventListener("submit", (e) => {
    e.preventDefault();
    if (turnActive) {
      cancelTurn();
      return;
    }
    const text = els.input.value.trim();
    if (!text || !activeId) return;
    sendMessage(text);
  });

  els.input.addEventListener("keydown", (e) => {
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      if (turnActive) return;
      els.composer.requestSubmit();
    }
  });
  els.input.addEventListener("input", autosize);
  function autosize() {
    els.input.style.height = "auto";
    els.input.style.height = Math.min(els.input.scrollHeight, 160) + "px";
  }

  if (els.btnSend) {
    els.btnSend.addEventListener("click", (e) => {
      if (!turnActive) return;
      e.preventDefault();
      cancelTurn();
    });
  }
  els.btnNew.onclick = () => newSession();
  els.btnMenu.onclick = () => {
    els.sidebar.classList.add("open");
    els.backdrop.hidden = false;
  };
  els.backdrop.onclick = closeMenu;
  function closeMenu() {
    els.sidebar.classList.remove("open");
    els.backdrop.hidden = true;
  }

  async function restartHub() {
    if (!token || !els.btnRestart) return;
    els.btnRestart.disabled = true;
    setWsPill("…");
    try {
      const res = await api("/api/control/restart", { method: "POST", body: "{}" });
      const data = await res.json().catch(() => ({}));
      if (!res.ok) {
        console.error(data.error || "restart failed");
        els.btnRestart.disabled = false;
        return;
      }
    } catch (err) {
      // Hub is likely restarting; WS reconnect will re-enable the button.
    }
  }

  if (els.btnRestart) {
    els.btnRestart.onclick = () => restartHub();
  }

  /** Pin .app to the visible viewport on phone (URL bar / keyboard). Desktop keeps CSS svh/dvh. */
  function isNarrowViewport() {
    return window.matchMedia("(max-width: 800px)").matches;
  }

  function syncAppHeight() {
    const root = document.documentElement;
    const body = document.body;
    if (!isNarrowViewport()) {
      root.style.removeProperty("--app-height");
      root.style.removeProperty("--app-top");
      body.classList.remove("vv-pinned", "vv-keyboard");
      return;
    }
    const vv = window.visualViewport;
    const h = vv && vv.height > 0 ? vv.height : window.innerHeight;
    const top = vv && Number.isFinite(vv.offsetTop) ? vv.offsetTop : 0;
    if (!(h > 0)) return;
    root.style.setProperty("--app-height", Math.round(h) + "px");
    root.style.setProperty("--app-top", Math.round(top) + "px");
    body.classList.add("vv-pinned");
    // Soft keyboard: visual viewport much shorter than layout viewport.
    const keyboardLikely = h < window.innerHeight * 0.75;
    body.classList.toggle("vv-keyboard", keyboardLikely);
  }

  /** Android Chrome often updates visualViewport late during keyboard animation. */
  function scheduleViewportSync(opts) {
    const scrollComposer = !!(opts && opts.scrollComposer);
    const run = () => {
      syncAppHeight();
      if (scrollComposer && els.composer && document.activeElement === els.input) {
        try {
          els.composer.scrollIntoView({ block: "nearest", inline: "nearest" });
        } catch (_) {}
      }
    };
    run();
    requestAnimationFrame(run);
    [50, 100, 200, 300].forEach((ms) => setTimeout(run, ms));
  }

  function installViewportSync() {
    const run = () => syncAppHeight();
    run();
    window.addEventListener("resize", run);
    window.addEventListener("orientationchange", () => scheduleViewportSync());
    if (window.visualViewport) {
      window.visualViewport.addEventListener("resize", run);
      window.visualViewport.addEventListener("scroll", run);
    }
    if (els.input) {
      els.input.addEventListener("focus", () => scheduleViewportSync({ scrollComposer: true }));
      els.input.addEventListener("blur", () => scheduleViewportSync());
    }
    if (els.composer) {
      els.composer.addEventListener("focusin", () => scheduleViewportSync({ scrollComposer: true }));
    }
  }
  installViewportSync();

  document.addEventListener("visibilitychange", () => {
    if (!document.hidden) {
      reconnectDelay = RECONNECT_BASE_MS;
      forceReconnectAndCatchUp();
    } else {
      refreshPollState();
    }
  });
  window.addEventListener("online", () => {
    reconnectDelay = RECONNECT_BASE_MS;
    forceReconnectAndCatchUp();
  });
  window.addEventListener("offline", () => setWsPill("offline"));
  window.addEventListener("pageshow", (ev) => {
    if (ev.persisted || !wsIsOpen()) {
      reconnectDelay = RECONNECT_BASE_MS;
      forceReconnectAndCatchUp();
    }
  });

  if (els.grokOnlyToggle) {
    syncGrokOnlyToggle();
    els.grokOnlyToggle.addEventListener("change", () => {
      includeGrokOnly = els.grokOnlyToggle.checked;
      localStorage.setItem(GROK_ONLY_KEY, includeGrokOnly ? "1" : "0");
      refreshSessions().catch(() => {});
    });
  }

  if (els.demoToggle) {
    els.demoToggle.checked = DEMO;
    els.demoToggle.addEventListener("change", () => {
      const u = new URL(location.href);
      if (els.demoToggle.checked) u.searchParams.set("demo", "1");
      else u.searchParams.delete("demo");
      location.href = u.pathname + u.search + u.hash;
    });
  }

  if (els.btnSettings && els.settingsModal) {
    els.btnSettings.addEventListener("click", (e) => {
      e.stopPropagation();
      setSettingsOpen(els.settingsModal.hidden);
    });
    els.settingsModal.addEventListener("click", (e) => e.stopPropagation());
    document.addEventListener("click", () => setSettingsOpen(false));
    document.addEventListener("keydown", (e) => {
      if (e.key === "Escape") setSettingsOpen(false);
    });
  }

  async function loadSessionOwner() {
    try {
      const st = await fetch("/api/auth/status").then((r) => r.json());
      if (st && st.user_name) setSessionOwner(st.user_name);
      else setSessionOwner("");
    } catch (_) {
      setSessionOwner("");
    }
  }

  async function boot() {
    showInsecureBanner();
    await showEndpointBanner();
    await loadSessionOwner();
    await ensurePaired(false);
    connectWs();
    await refreshSessions();
    startLiveRefresh();
    let hash = (location.hash || "").replace(/^#s=/, "");
    try { hash = decodeURIComponent(hash); } catch (_) {}
    if (hash && sessions.some((s) => s.id === hash)) {
      await openSession(hash);
    } else if (sessions.length) {
      await openSession(sessions[0].id);
    } else {
      await newSession();
    }
    els.btnSend.disabled = !activeId;
    setTurnActive(false);
    if (!activeId) {
      els.chatTitle.textContent = "Select a session";
      clearCtxMeter();
      renderEmptySession();
    }
    refreshPollState();
  }

  boot().catch((err) => {
    console.error(err);
    els.feed.innerHTML = emptyStateHtml("Could not start", String(err));
  });
})();
