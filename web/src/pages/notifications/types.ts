export type Channel = {
  smtpMode?: string;
  subscription?: PushSubscriptionJSON;
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

export type Delivery = {
  id: number;
  channel: string;
  created: number;
  status: string;
  error?: string;
  kind?: string;
  attempted?: number;
  due?: number;
};

export type NotificationsData = {
  channels: Channel[];
  events: string[];
  deliveries: Delivery[];
  eventCatalog?: { kind: string; description: string; automatic: boolean }[];
  crashWatcher?: boolean;
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

export const CRITICAL_EVENTS = [
  "server.crash",
  "playback.error",
  "provider.outage",
  "indexer.outage",
  "cache.full",
];
export const TRANSPORT_HELP: Record<string, string> = {
  smtp: "Use your mail provider’s SMTP server and app password. STARTTLS usually uses port 587; implicit TLS uses 465. Authentication is optional for a trusted TLS relay. Recipients can be separated by commas.",
  ntfy: "Enter your complete topic URL. Subscribe to the same topic in the ntfy phone app. Add a bearer token for private topics.",
  gotify:
    "Enter your server’s /message URL and an application token. Connect the Gotify app on your phone to receive alerts.",
  telegram:
    "Create a bot with BotFather, message it first, then enter its bot token and the destination chat ID. Group chat IDs may be negative.",
  discord:
    "Create an incoming webhook in the destination Discord channel and paste its full webhook URL. Mentions are disabled.",
  slack: "Create an incoming webhook for the destination Slack channel and paste its full URL.",
  webhook:
    "Ether sends a JSON POST with kind, title, message and at. Optionally add a bearer token. Only HTTP 2xx responses count as accepted.",
  webpush:
    "This subscription belongs to one browser profile on one device. Use Connect this device on each device you want to receive alerts.",
};
const EVENT_LABELS: Record<string, string> = {
  "server.started": "Server online",
  "server.stopping": "Server stopping",
  "server.crash": "Server crashed",
  "provider.recovered": "Provider recovered",
  "indexer.recovered": "Indexer recovered",
  "cache.recovered": "Cache space available",
  "*": "All events",
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
  webpush: "Browser push",
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
  smtpMode: "starttls",
  enabled: true,
  events: [...CRITICAL_EVENTS],
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
