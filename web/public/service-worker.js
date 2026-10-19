// The owner dashboard contains private data. Never cache authenticated responses,
// API requests, HTML, artwork, or notification payloads.
self.addEventListener("install", () => self.skipWaiting());
self.addEventListener("activate", (event) => event.waitUntil(self.clients.claim()));
self.addEventListener("fetch", () => {});
