# Owner insights and notifications

`/ether/insights` combines real observed play sessions, viewing summaries, provider/indexer usage, notification settings, and detected transcoding hardware. No analytics are generated for periods before collection began.

## Collection

`internal/stats` owns persistent `stats_*` tables. App wiring subscribes to playback, watch-state, and indexer events. A 30-second sampler updates active bandwidth and provider counters; event hooks record starts, decisions, failovers, and final bytes at stop. After restart, previously active records end at their last observed update.

Timeline duration is elapsed time between consecutive `playing` reports, excluding gaps over 60 seconds. Watch-state events lack a client ID, so simultaneous sessions for the same viewer/title are excluded from timeline attribution. Explicit scrobbles can complete a currently matching session. Wall-clock session duration and client-reported viewing time are different measurements. Bytes include data served to ffmpeg for transcoding; they are not a measurement of transcoded client output.

Daily/hourly aggregates and provider/indexer months use UTC. Session history is capped at 500 records per request. Summary queries aggregate the full selected period. The UI limits displayed daily bars and popular lists; the API returns all aggregate buckets within its bounded group limit.

Provider counters are sampled as deltas, with counter resets handled additively. Sample time assigns deltas to the day. `bodyMisses` counts unavailable BODY responses; `bodyTakedowns` is the subset with NNTP status 451. `misses` also includes STAT misses. `backfillSaves` counts BODY fetches that succeed on a backfill provider after an earlier provider miss. It is not a count of rescued plays. The availability ratio uses successful BODY fetches and BODY misses only. EWMA latency is a current process snapshot.

Indexer operations record search/grab success, failures, latency, budget exhaustion, and average returned release age. Search calls may use the indexer's result cache; daily API-hit/grab budget counters remain the source for actual quota usage. No indexer health-check pass rate or per-play provider attribution is claimed.

Costs use owner-entered monthly equivalents. Annual subscriptions or block plans need a monthly estimate. Cost per observed play divides a full monthly plan price by this month's recorded sessions; cost per GB divides by this month's observed provider bytes. Currencies remain separate. Neither value is a billing reconciliation.

## Alerts

`internal/notify` stores channels, rules, and pending notifications in `notify_*` tables. Endpoints, bot tokens, and passwords are redacted in API responses and preserved when saved as `[configured]`. Delivery logs expose status without upstream response bodies or credential-bearing errors.

Supported transports are Discord, Slack, Telegram, SMTP, ntfy, Gotify, and generic JSON webhooks. SMTP requires verified TLS via STARTTLS or implicit TLS on port 465. HTTP redirects are rejected. Events enqueue synchronously; a separate queue consumer performs delivery. Per-channel limits defer pending events. Digest delays collect due events into a bounded digest.

Playback and indexer hooks emit events automatically. The health sampler emits provider outage and cache-full transitions. Other producing services can call `Emit` with the documented event kind; discovery can use `TitleAvailable`. Selecting a rule alone does not create an event producer.

Test buttons enqueue real deliveries when an owner uses them. Development tests use only offline fake upstreams. Failed delivery attempts are visible and are not retried automatically, because a timeout may have delivered upstream. A process interruption during delivery leaves `sending` visible rather than silently retrying an ambiguous send. Sent/failed delivery records expire after 30 days.

## Weekly report

The newsletter is an escaped HTML report over the last seven days, with optional viewer filtering. Preview/download do not send email.

Automatic delivery defaults off. Configure an enabled SMTP channel with the `newsletter.weekly` rule, then opt in under Viewing stats and choose a weekday/hour in UTC. The next occurrence is persisted; the weekly ledger claims each slot before enqueueing, preventing duplicate enqueueing after restart. Missed schedules enqueue one current report on restart and advance to the next future slot. `queued` means accepted by the notification bus; delivery status is separate. Failures are recorded without an automatic retry of that weekly slot. Changing/saving the schedule selects the next future slot.

## Mobile dashboard

The manifest and icons support installing the owner UI from a secure browser context. The service worker never caches HTML, authenticated API responses, artwork, or notification data. Installation does not provide offline data. Web Push is explicitly unavailable; configured external channels deliver background alerts. Live CPU/GPU utilization and tone-map benchmarks are not collected; Hardware displays the existing detected ffmpeg/encoder information.

Live TV status reports one viewing reservation per viewer, including passthrough
IPTV, and owner termination closes that reservation. Virtual programme source
activity still feeds the ordinary content history. Raw IPTV has no on-demand
source byte counters or watch-state timeline; its presence in Now Playing does
not manufacture those measurements.

A compact authenticated status response is available at `/ether/api/status` for
owner dashboards and generic HTTP widgets. It reports `server`, `version`,
`streams`, `providers`, `healthyProviders`, `cacheUsed` and `cacheBudget`. Supply
the owner token in an HTTP header from the local network; tokens in query strings
are not accepted by the owner API.
