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
    modeBadge: $("modeBadge"),
    demoLink: $("demoLink"),
    btnRestart: $("btnRestart"),
    btnJumpBottom: $("btnJumpBottom"),
  };

  let token = localStorage.getItem(TOKEN_KEY) || "";
  let sessions = [];
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

  if (DEMO) {
    els.modeBadge.textContent = "Demo mode";
    els.modeBadge.classList.add("demo");
    document.title = "Grok Bridge (demo)";
  }

  /** Allowlist endpoint URLs before setting href (XSS-safe). */
  function parseAllowedEndpointUrl(raw) {
    try {
      const u = new URL(String(raw || ""));
      const host = u.hostname;
      const localHttp =
        u.protocol === "http:" && (host === "localhost" || host === "127.0.0.1");
      const httpsOk = u.protocol === "https:";
      if (!httpsOk && !localHttp) return null;
      const okHost =
        host === "localhost" ||
        host === "127.0.0.1" ||
        /^100\.\d{1,3}\.\d{1,3}\.\d{1,3}$/.test(host) ||
        /\.ts\.net$/i.test(host);
      if (!okHost) return null;
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
      }
      return;
    }
    if (data.session_id && activeId && data.session_id !== activeId) return;

    if (t === "user_message") {
      const mid = data.message_id;
      if (mid && els.feed.querySelector('.msg[data-id="' + CSS.escape(mid) + '"]')) return;
      if (adoptOptimisticUser(data.content, mid)) return;
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
      refreshSessions();
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
    const nodes = els.feed.querySelectorAll(".msg.user:not([data-id])");
    for (let i = 0; i < nodes.length; i++) {
      const el = nodes[i];
      const body = el.querySelector(".body");
      const raw = body && (body.dataset.raw != null ? body.dataset.raw : body.textContent);
      if (normalizeUserContent(raw || "") === want) {
        if (messageId) el.dataset.id = messageId;
        return true;
      }
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
    if (nearBottom()) scrollFeed();
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
   * XSS-safe message HTML: escape first, then bold; wrap complete
   * <system-reminder> blocks (case-insensitive) as muted asides.
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
        formatBold(escapeHtml(inner)) +
        "</div></aside>"
      );
    }
    return formatBold(escapeHtml(inner));
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
        html += formatBold(escapeHtml(s.slice(last, m.index)));
      }
      html += formatTaggedAside(m[1], m[2]);
      last = m.index + m[0].length;
    }
    if (last < s.length) {
      html += formatBold(escapeHtml(s.slice(last)));
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

  function appendMsg(role, content, streaming) {
    const follow = nearBottom();
    clearEmpty();
    const safeRole = safeMsgRole(role);
    const div = document.createElement("div");
    div.className = "msg " + safeRole + (streaming ? " streaming" : "");
    const roleEl = document.createElement("div");
    roleEl.className = "role";
    roleEl.textContent = roleLabel(safeRole);
    const bodyEl = document.createElement("div");
    bodyEl.className = "body";
    setMsgBody(bodyEl, content || "");
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

  function scrollFeed() {
    els.feed.scrollTop = els.feed.scrollHeight;
    updateJumpBottom();
  }

  function nearBottom() {
    return els.feed.scrollHeight - els.feed.scrollTop - els.feed.clientHeight < 96;
  }

  function updateJumpBottom() {
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
    msgs.forEach((m) => {
      const el = appendMsg(m.role, m.content || "", false);
      if (m.id) el.dataset.id = m.id;
      if (m.tools && m.tools.length) renderTools(el, m.tools);
      if (m.cancelled) markCancelled(el);
    });
    scrollFeed();
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
      if (m.role === "user" && adoptOptimisticUser(m.content, m.id)) {
        if (m.id) have.add(m.id);
        return;
      }
      if (m.role === "assistant" && streamingEl && !streamingEl.dataset.id) {
        streamingEl.classList.remove("streaming");
        setMsgBody(streamingEl, m.content || "");
        if (m.id) streamingEl.dataset.id = m.id;
        if (m.tools && m.tools.length) renderTools(streamingEl, m.tools);
        streamingEl = null;
        streamingTools = null;
        if (m.id) have.add(m.id);
        return;
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

  async function refreshSessions() {
    const res = await api("/api/sessions");
    const data = await res.json();
    sessions = data.sessions || [];
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
    let banner = document.getElementById("buildRoHint");
    if (on) {
      if (!banner) {
        banner = document.createElement("div");
        banner.id = "buildRoHint";
        banner.className = "build-ro-hint";
        banner.setAttribute("role", "status");
        const host = els.composer && els.composer.parentNode;
        if (host) host.insertBefore(banner, els.composer);
      }
      banner.textContent = "via Grok Build (ACP)";
      banner.hidden = false;
      if (els.input) els.input.placeholder = "Message Grok Build…";
    } else if (banner) {
      banner.hidden = true;
      if (els.input) els.input.placeholder = "Message Grok…";
    }
  }

  function renderSessionList() {
    els.sessionList.innerHTML = "";
    sessions.forEach((s) => {
      const btn = document.createElement("button");
      btn.type = "button";
      const build = isBuildSession(s);
      btn.className = "session-item" + (s.id === activeId ? " active" : "") + (build ? " build" : "");
      btn.innerHTML = '<div class="t-row"><div class="t"></div></div><div class="m"></div>';
      btn.querySelector(".t").textContent = s.title || "Untitled";
      if (build) {
        const badge = document.createElement("span");
        badge.className = "src-badge build";
        badge.textContent = "Build";
        badge.title = "Grok Build session (live via ACP)";
        btn.querySelector(".t-row").appendChild(badge);
      }
      const meta = btn.querySelector(".m");
      const count = (s.message_count || 0) + " msg";
      const rel = relativeTime(s.updated_at);
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
      els.sessionList.appendChild(btn);
    });
  }

  async function openSession(id) {
    activeId = id;
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
    const res = await api("/api/sessions", {
      method: "POST",
      body: JSON.stringify({ title: DEMO ? "Demo chat" : "New chat" }),
    });
    const sess = await res.json();
    await refreshSessions();
    await openSession(sess.id);
  }

  async function sendMessage(text) {
    if (!text || !activeId || sending || turnActive) return;
    sending = true;
    els.btnSend.disabled = true;
    setTurnActive(true);
    try {
      if (wsIsOpen()) {
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
    if (!isNarrowViewport()) {
      root.style.removeProperty("--app-height");
      return;
    }
    const vv = window.visualViewport;
    const h = vv && vv.height > 0 ? vv.height : window.innerHeight;
    if (!(h > 0)) return;
    root.style.setProperty("--app-height", Math.round(h) + "px");
  }

  function installViewportSync() {
    const run = () => syncAppHeight();
    run();
    window.addEventListener("resize", run);
    window.addEventListener("orientationchange", run);
    if (window.visualViewport) {
      window.visualViewport.addEventListener("resize", run);
      window.visualViewport.addEventListener("scroll", run);
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

  async function boot() {
    showInsecureBanner();
    await showEndpointBanner();
    await ensurePaired(false);
    connectWs();
    await refreshSessions();
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
