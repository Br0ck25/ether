# Owner notifications

Channels support SMTP email, Web Push, ntfy, Gotify, Telegram, Discord, Slack,
and generic JSON webhooks. Channel connection tests deliver immediately, even
when paused, and record success or a sanitized failure in history. A successful
send means the destination accepted the message, not that a person read it.

Regular events use the durable, claimed queue. Rate caps and digest windows apply
per channel. Server lifecycle events and weekly HTML reports are individual
messages outside digest windows. Ambiguous delivery failures are never retried
automatically. History is retained for 30 days, including deleted channels.

Browser subscriptions are individual channels with their own event filters.
The database holds one VAPID identity shared by cluster nodes. Subscriptions,
endpoints and secrets are redacted from ordinary API responses and preserved on
edits. Connecting requires a secure context and an explicit permission gesture.
The service worker displays push messages without caching authenticated content.
An expired subscription must be reconnected on its device.

The `ether` executable starts `ether notification-monitor` with an anonymous
stdin pipe. It sends private channel snapshots over that pipe, immediately on
local edits and every 30 seconds for cross-node changes. EOF without a clean
shutdown message triggers a direct `server.crash` delivery from the child, with
no database dependency, rate cap or digest delay. The child exits after delivery
or a 20-second deadline. Normal shutdown waits for the child to exit. Fatal Go
panics and process faults do not need a recovery handler. Watcher deliveries are
outside database history. An embedded App does not start a monitor automatically.

The crash watcher is best effort. A killed process group, machine power loss or
network outage can prevent its delivery. Optional external dead-man monitoring
covers those cases: after each successful one-minute local HTTP probe, Ether
POSTs to an owner-supplied HTTPS heartbeat URL. Configure the monitoring service
to expect one-minute pings with at least three minutes of grace and configure its
own destinations. Cluster nodes share this setting, so any surviving healthy
node keeps the service-level heartbeat alive. Per-node uptime needs separate
external checks.

The event catalogue reports whether Ether has an automatic producer. Legacy
certificate, update and viewer-joined event IDs remain available to owner API
integrations and are explicitly identified as integration-only.

Tests use local fake SMTP/TLS and HTTP services, encrypted Web Push delivery,
and a subprocess that exits without shutdown callbacks. No live credentials or
external services are needed.
