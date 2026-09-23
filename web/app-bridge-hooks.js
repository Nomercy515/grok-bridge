/**
 * Thin bridge between hub UI and model-selector (and future rail tools).
 * Keeps app.js untouched when GitHub MCP cannot push the large file.
 */
(function () {
  function activeIdFromDom() {
    const el = document.querySelector(".session-item.active");
    return el ? el.dataset.id || "" : "";
  }

  window.GrokBridge = window.GrokBridge || {
    getActiveId: activeIdFromDom,
    isBuildSession: (id) => typeof id === "string" && id.indexOf("build:") === 0,
  };

  let lastId = "";
  function emitSessionIfChanged() {
    const id = (window.GrokBridge.getActiveId && window.GrokBridge.getActiveId()) || activeIdFromDom();
    if (id === lastId) return;
    lastId = id;
    try {
      window.dispatchEvent(new CustomEvent("grok-bridge:session", { detail: { id } }));
    } catch (_) {}
  }

  // Poll + observe rail — works even if openSession does not dispatch.
  setInterval(emitSessionIfChanged, 400);
  const rail = document.getElementById("sessionList") || document.body;
  try {
    new MutationObserver(emitSessionIfChanged).observe(rail, {
      subtree: true,
      attributes: true,
      attributeFilter: ["class"],
      childList: true,
    });
  } catch (_) {}

  // Usage tip: bind once at startup (fixes listeners nested under session_created).
  function bindUsageTipOnce() {
    const meter = document.getElementById("usageMeter");
    const wrap = document.getElementById("usageWrap");
    const tip = document.getElementById("usageTip");
    if (!meter || meter.dataset.tipBound === "1") return;
    meter.dataset.tipBound = "1";
    const close = () => {
      if (tip) tip.hidden = true;
      meter.setAttribute("aria-expanded", "false");
    };
    const toggle = (ev) => {
      if (ev) ev.preventDefault();
      if (!tip) return;
      const open = tip.hidden;
      tip.hidden = !open;
      meter.setAttribute("aria-expanded", open ? "true" : "false");
    };
    meter.addEventListener("click", toggle);
    meter.addEventListener("keydown", (ev) => {
      if (ev.key === "Enter" || ev.key === " ") toggle(ev);
    });
    document.addEventListener("click", (ev) => {
      if (!tip || tip.hidden) return;
      if (wrap && wrap.contains(ev.target)) return;
      close();
    });
    document.addEventListener("keydown", (ev) => {
      if (ev.key === "Escape") close();
    });
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", () => {
      bindUsageTipOnce();
      emitSessionIfChanged();
    });
  } else {
    bindUsageTipOnce();
    emitSessionIfChanged();
  }
})();
