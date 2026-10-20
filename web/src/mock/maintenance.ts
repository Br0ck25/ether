/**
 * Fake notification, backup, connection-check and discovery endpoints for the
 * mock backend, so the server and maintenance pages render with real content.
 */

type Result = { status: number; body: unknown };

export function createMaintenance() {
  const day = 86_400_000;
  const now = Date.now();
  const channels = [
    {
      id: "ch-discord",
      name: "Owner Discord",
      kind: "discord",
      enabled: true,
      events: ["playback.error", "provider.outage", "indexer.outage", "update.available"],
      endpoint: "https://discord.example/webhook",
      token: "",
      target: "",
      username: "",
      password: "",
      from: "",
      to: "",
      perMinute: 5,
      digestMinutes: 0,
    },
  ];
  const events = [
    "playback.start",
    "playback.stop",
    "playback.error",
    "playback.failover",
    "provider.outage",
    "indexer.outage",
    "indexer.budget",
    "cache.full",
    "cert.renewed",
    "update.available",
    "user.joined",
    "title.available",
    "newsletter.weekly",
  ];
  const deliveries = Array.from({ length: 12 }, (_, i) => ({
    id: i + 1,
    channel: "ch-discord",
    created: Math.floor((now - i * 3.7 * 3_600_000) / 1000),
    status: i === 3 ? "failed: timeout" : "sent",
  }));
  const backups = {
    snapshots: Array.from({ length: 5 }, (_, i) => ({
      name: `ether-backup-${new Date(now - i * day).toISOString().slice(0, 10)}.tar.gz`,
      size: 48_000_000 + i * 1_200_000,
      created: new Date(now - i * day).toISOString(),
    })),
    settings: { intervalHours: 24, retain: 7 },
    warning: "",
  };
  let newsletter = {
    enabled: true,
    weekday: 1,
    hour: 9,
    userId: 1,
    nextDue: Math.floor((now + 3 * day) / 1000),
    lastAttempt: Math.floor((now - 4 * day) / 1000),
    lastStatus: "sent",
  };
  let discovery = {
    enabled: true,
    plex: true,
    trakt: false,
    notify: true,
    prewarm: true,
    checkHours: 6,
    naturalSearch: true,
    recaps: false,
  };
  const diagnostics = {
    compatibilityVersion: "1.43.4",
    latestPublicVersion: "1.43.4",
    steps: [
      {
        name: "Local discovery",
        state: "ok",
        detail: "Plex apps on your network find this server.",
        action: "",
      },
      {
        name: "Account access",
        state: "ok",
        detail: "The server is published to plex.tv under your account.",
        action: "",
      },
      {
        name: "Port forwarding",
        state: "warn",
        detail: "Plex couldn't reach 203.0.113.7:32400 from outside.",
        action: "Forward TCP 32400 on your router to this computer, or turn on UPnP.",
      },
      {
        name: "Secure connections",
        state: "ok",
        detail: "plex.direct certificate valid for 81 more days.",
        action: "",
      },
    ],
  };

  function handle(method: string, seg: string[], body: unknown): Result | null {
    const path = "/" + seg.join("/");
    const route = `${method} ${path}`;
    switch (route) {
      case "GET /notifications":
        return { status: 200, body: { channels, events, deliveries } };
      case "GET /stats/newsletter/settings":
        return { status: 200, body: newsletter };
      case "PUT /stats/newsletter/settings":
        newsletter = { ...newsletter, ...(body as object) };
        return { status: 200, body: newsletter };
      case "GET /adoption/backups":
        return { status: 200, body: backups };
      case "POST /adoption/backups/settings":
        backups.settings = body as typeof backups.settings;
        return { status: 200, body: backups.settings };
      case "GET /safety/diagnostics":
        return { status: 200, body: diagnostics };
      case "POST /safety/diagnostics/refresh":
        return { status: 200, body: { ok: true } };
      case "GET /discovery/settings":
        return { status: 200, body: discovery };
      case "PUT /discovery/settings":
        discovery = { ...discovery, ...(body as object) };
        return { status: 200, body: discovery };
      case "GET /discovery/ratings/settings":
        return { status: 200, body: { enabled: true, provider: "omdb" } };
    }
    if (method === "PUT" && seg[0] === "notifications" && seg[1]) {
      const c = body as (typeof channels)[number];
      const i = channels.findIndex((x) => x.id === seg[1]);
      if (i >= 0) channels[i] = c;
      else channels.push(c);
      return { status: 200, body: c };
    }
    if (method === "DELETE" && seg[0] === "notifications" && seg[1]) {
      const i = channels.findIndex((x) => x.id === seg[1]);
      if (i >= 0) channels.splice(i, 1);
      return { status: 200, body: { ok: true } };
    }
    return null;
  }

  return { handle };
}
