/**
 * Composer-chip model selector — loads real ACP models from the hub.
 * Always visible (this app is for Grok Build). Chip markup lives in index.html.
 */
(function () {
  const TOKEN_KEY = "grok_bridge_token";
  const $ = (id) => document.getElementById(id);

  let current = { configId: "model", value: "", models: [], available: false, reason: "" };
  let loading = false;

  function authHeaders() {
    const token = localStorage.getItem(TOKEN_KEY) || "";
    return token
      ? { Authorization: "Bearer " + token, "Content-Type": "application/json" }
      : { "Content-Type": "application/json" };
  }

  function activeSessionId() {
    if (window.GrokBridge && typeof window.GrokBridge.getActiveId === "function") {
      return window.GrokBridge.getActiveId();
    }
    const el = document.querySelector(".session-item.active");
    return el ? el.dataset.id || "" : "";
  }

  function ensureMarkup() {
    const dock = document.querySelector(".composer-dock");
    const form = $("composer");
    const ta = $("input");
    const send = $("btnSend");
    if (!dock || !form || !ta || !send) return false;

    $("modelMockSwitch")?.remove();
    $("modelBar")?.remove();
    document.body.classList.remove("model-placement-bar");
    document.body.classList.add("model-placement-chip");

    // Prefer static chip in index.html; inject only if missing.
    if (!$("modelChip")) {
      const wrap = document.createElement("div");
      wrap.className = "model-chip-wrap";
      wrap.id = "modelChipWrap";
      wrap.innerHTML =
        '<button type="button" class="model-chip" id="modelChip" aria-haspopup="listbox" aria-expanded="false" title="Model">' +
        '<span class="model-chip-label">Model</span><span class="chev" aria-hidden="true">▾</span></button>' +
        '<div class="model-menu" id="modelMenuChip" hidden role="listbox" aria-label="Choose model"></div>';
      form.insertBefore(wrap, send);
    }

    const wrap = $("modelChipWrap") || document.querySelector(".model-chip-wrap");
    if (wrap) {
      wrap.hidden = false;
      wrap.removeAttribute("hidden");
    }
    return !!$("modelChip");
  }

  function labelFor(id) {
    const m = (current.models || []).find((x) => x.id === id);
    return m ? m.name : id || "Model";
  }

  function renderMenu() {
    const menu = $("modelMenuChip");
    if (!menu) return;
    const cur = current.value;
    let html = '<div class="model-menu-label">Session model</div>';
    if (loading) {
      html += '<div class="model-menu-foot">Loading models…</div>';
    } else if (!current.available || !(current.models || []).length) {
      html +=
        '<div class="model-menu-foot">' +
        (current.reason || "No models from ACP for this session.") +
        "</div>";
    } else {
      current.models.forEach((m) => {
        const checked = m.id === cur ? "true" : "false";
        html +=
          '<button type="button" class="opt" role="option" data-model="' +
          escapeAttr(m.id) +
          '" aria-checked="' +
          checked +
          '"><span>' +
          escapeHtml(m.name) +
          '</span><span class="check" aria-hidden="true">✓</span></button>';
      });
    }
    menu.innerHTML = html;
    const chipLabel = document.querySelector(".model-chip-label");
    if (chipLabel) {
      chipLabel.textContent = current.available && cur ? labelFor(cur) : "Model";
    }
  }

  function escapeHtml(s) {
    return String(s)
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;")
      .replace(/\"/g, "&quot;");
  }
  function escapeAttr(s) {
    return escapeHtml(s).replace(/'/g, "&#39;");
  }

  function applyModelsPayload(mc) {
    if (!mc || typeof mc !== "object") return;
    current = {
      configId: mc.configId || "model",
      value: mc.currentValue || "",
      models: Array.isArray(mc.models) ? mc.models : [],
      available: !!mc.available,
      reason: mc.reason || "",
    };
    renderMenu();
  }

  async function loadModels() {
    const wrap = $("modelChipWrap") || document.querySelector(".model-chip-wrap");
    if (wrap) {
      wrap.hidden = false;
      wrap.removeAttribute("hidden");
    }
    const id = activeSessionId();
    if (!id) {
      current = {
        configId: "model",
        value: "",
        models: [],
        available: false,
        reason: "Select a session to load models.",
      };
      loading = false;
      renderMenu();
      return;
    }
    loading = true;
    renderMenu();
    try {
      const res = await fetch("/api/sessions/" + encodeURIComponent(id) + "/models", {
        headers: authHeaders(),
      });
      if (!res.ok) {
        const err = await res.json().catch(() => ({}));
        current = {
          configId: "model",
          value: "",
          models: [],
          available: false,
          reason: err.error || "Failed to load models (" + res.status + ")",
        };
      } else {
        applyModelsPayload(await res.json());
      }
    } catch (e) {
      current = {
        configId: "model",
        value: "",
        models: [],
        available: false,
        reason: (e && e.message) || "Failed to load models",
      };
    } finally {
      loading = false;
      renderMenu();
    }
  }

  async function setModel(modelId) {
    const id = activeSessionId();
    if (!id || !modelId) return;
    closeMenus();
    const chipLabel = document.querySelector(".model-chip-label");
    if (chipLabel) chipLabel.textContent = labelFor(modelId);
    try {
      const res = await fetch("/api/sessions/" + encodeURIComponent(id) + "/model", {
        method: "PUT",
        headers: authHeaders(),
        body: JSON.stringify({ value: modelId, configId: current.configId || "model" }),
      });
      const data = await res.json().catch(() => ({}));
      if (!res.ok) {
        current.reason = data.error || "Failed to set model";
        await loadModels();
        return;
      }
      applyModelsPayload(data);
    } catch (_) {
      await loadModels();
    }
  }

  function closeMenus() {
    const m = $("modelMenuChip");
    if (m) m.hidden = true;
    const b = $("modelChip");
    if (b) b.setAttribute("aria-expanded", "false");
  }

  function toggleMenu() {
    const menu = $("modelMenuChip");
    const btn = $("modelChip");
    if (!menu || !btn) return;
    const open = menu.hidden;
    closeMenus();
    if (open) {
      menu.hidden = false;
      btn.setAttribute("aria-expanded", "true");
      loadModels();
    }
  }

  function bind() {
    $("modelChip")?.addEventListener("click", (e) => {
      e.preventDefault();
      e.stopPropagation();
      toggleMenu();
    });
    document.addEventListener("click", (e) => {
      if (e.target.closest(".model-menu, .model-chip-wrap")) return;
      closeMenus();
    });
    document.addEventListener("keydown", (e) => {
      if (e.key === "Escape") closeMenus();
    });
    $("modelMenuChip")?.addEventListener("click", (e) => {
      const opt = e.target.closest("button.opt");
      if (!opt) return;
      e.preventDefault();
      setModel(opt.dataset.model);
    });
    window.addEventListener("grok-bridge:session", () => {
      loadModels();
    });
    window.addEventListener("grok-bridge:ws", (ev) => {
      const data = ev.detail;
      if (!data || data.type !== "session_models") return;
      if (data.session_id && data.session_id !== activeSessionId()) return;
      applyModelsPayload(data.models);
    });
  }

  function init() {
    if (!ensureMarkup()) return;
    bind();
    loadModels();
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }
})();
