// SPDX-License-Identifier: AGPL-3.0-only

// The service worker: shows Allstill's push messages, and opens their link
// when one is tapped. Served at /sw.js, so it covers the whole dashboard.

self.addEventListener("push", (event) => {
  let m = {};
  try {
    m = event.data?.json() ?? {};
  } catch {
    // An unreadable message still shows, so nothing is lost silently.
  }
  event.waitUntil(
    self.registration.showNotification(m.title || "Allstill", {
      body: m.body || "",
      tag: m.tag,
      renotify: Boolean(m.tag),
      icon: "/static/icon.svg",
      data: { url: m.url || "/messages" },
    }),
  );
});

self.addEventListener("notificationclick", (event) => {
  event.notification.close();
  // A link always opens in this dashboard, also when it was sent under
  // another address of it, and in a window of it that is open already.
  const link = new URL(event.notification.data?.url || "/", self.location.origin);
  const url = new URL(link.pathname + link.search, self.location.origin).href;
  event.waitUntil(
    self.clients.matchAll({ type: "window", includeUncontrolled: true }).then((windows) => {
      const open = windows.find((w) => new URL(w.url).origin === self.location.origin && "navigate" in w);
      if (!open) return self.clients.openWindow(url);
      // A window the worker does not control cannot be navigated.
      return open.navigate(url).then((w) => (w ?? open).focus(), () => self.clients.openWindow(url));
    }),
  );
});
