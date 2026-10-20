export type Channel = {
  id: string;
  name: string;
  kind: string;
  enabled: boolean;
  events: string[];
  endpoint: string;
  token: string;
  target: string;
  username: string;
  password: string;
  from: string;
  to: string;
  perMinute: number;
  digestMinutes: number;
};

export type Delivery = { id: number; channel: string; created: number; status: string };

export type NotificationsData = {
  channels: Channel[];
  events: string[];
  deliveries: Delivery[];
};

export type NewsletterSettings = {
  enabled: boolean;
  weekday: number;
  hour: number;
  userId: number;
  nextDue: number;
  lastAttempt: number;
  lastStatus: string;
};

const EVENT_LABELS: Record<string, string> = {
  "playback.start": "Playback started",
  "playback.stop": "Playback ended",
  "playback.error": "Playback failed",
  "playback.failover": "Playback switched source",
  "provider.outage": "Provider unavailable",
  "indexer.outage": "Indexer unavailable",
  "indexer.budget": "Indexer budget reached",
  "cache.full": "Cache full",
  "cert.renewed": "Certificate renewed",
  "update.available": "Update available",
  "user.joined": "Viewer joined",
  "title.available": "Watchlist title available",
  "newsletter.weekly": "Weekly newsletter",
  test: "Test notification",
};
export const eventLabel = (kind: string) => EVENT_LABELS[kind] ?? kind;

export const TRANSPORTS = ["webhook", "discord", "slack", "telegram", "smtp", "ntfy", "gotify"];
const TRANSPORT_LABELS: Record<string, string> = {
  webhook: "Webhook",
  discord: "Discord",
  slack: "Slack",
  telegram: "Telegram",
  smtp: "Email (SMTP)",
  ntfy: "ntfy",
  gotify: "Gotify",
};
export const transportLabel = (kind: string) => TRANSPORT_LABELS[kind] ?? kind;

export const channelName = (c: Channel) => c.name || transportLabel(c.kind);

export const blankChannel = (): Channel => ({
  id: crypto.randomUUID(),
  name: "",
  kind: "webhook",
  enabled: true,
  events: ["playback.error"],
  endpoint: "",
  token: "",
  target: "",
  username: "",
  password: "",
  from: "",
  to: "",
  perMinute: 5,
  digestMinutes: 0,
});
