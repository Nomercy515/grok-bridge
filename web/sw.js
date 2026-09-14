/** Minimal SW for Grok Bridge reply notifications (background tab / Android Chrome). */
self.addEventListener("install", (event) => {
  self.skipWaiting();
});

self.addEventListener("activate", (event) => {
  event.waitUntil(self.clients.claim());
});

self.addEventListener("message", (event) => {
  const data = event.data || {};
  if (data.type !== "show-notification") return;
  const title = data.title || "Grok Bridge";
  const options = {
    body: data.body || "",
    icon: data.icon || "/logo-192.png",
    badge: data.badge || "/favicon-32.png",
    tag: data.tag || "grok-bridge-reply",
    renotify: true,
    data: data.data || {},
  };
  event.waitUntil(self.registration.showNotification(title, options));
});

self.addEventListener("notificationclick", (event) => {
  event.notification.close();
  const payload = (event.notification && event.notification.data) || {};
  const sessionId = payload.sessionId || "";
  const targetUrl = payload.url || "/";
  event.waitUntil(
    (async () => {
      const all = await self.clients.matchAll({ type: "window", includeUncontrolled: true });
      for (const client of all) {
        try {
          const u = new URL(client.url);
          if (u.origin !== self.location.origin) continue;
        } catch (_) {
          continue;
        }
        if ("focus" in client) await client.focus();
        client.postMessage({ type: "open-session", sessionId });
        return;
      }
      if (self.clients.openWindow) {
        const hash = sessionId ? "#s=" + encodeURIComponent(sessionId) : "";
        await self.clients.openWindow(targetUrl.split("#")[0] + hash);
      }
    })()
  );
});
