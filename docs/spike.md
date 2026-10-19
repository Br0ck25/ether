# Ether Plex compatibility spike

The spike decides whether official Plex clients accept Ether as a server. Keep a short dated record of each result and attach sanitized logs or HAR files to the project issue; do not commit account tokens or personal network details.

## Prerequisites

- A host reachable by Plex Web, an iPhone/iPad running Plex, and an Apple TV running Plex.
- A Plex account for claiming Ether and a Plex Media Server available for response comparison.
- One small, legally obtained local MP4 with H.264 video and AAC audio. Keep it outside the repository.
- A working port forward for remote-stream testing; LAN-only testing is enough for the initial browse/play gate.

## Run Ether in spike mode

Configure the normal Ether `config.json` with `spike.enabled` set to `true` and `spike.localFiles` containing the absolute path to the sample MP4, for example:

```json
"spike": { "enabled": true, "localFiles": ["/path/to/spike-sample.mp4"] }
```

Start Ether using its normal service or executable entry point. The spike configuration provides a single local-file library without requiring Usenet or indexer setup. Open `http://<host>:32400/ether/`, complete the setup wizard, and claim the server with the test Plex account. Confirm the server appears in the account's device list before testing clients.

## Client pass bar

Every client must list the library, open the sample's detail page, start playback, seek to another point, and resume from the saved position. Record app version, Ether version, date, LAN/remote status, and outcome.

| Client | Browse | Detail page | Play | Seek | Resume | Remote stream |
|---|---|---|---|---|---|---|
| Plex Web | [ ] | [ ] | [ ] | [ ] | [ ] | [ ] |
| Plex for iOS | [ ] | [ ] | [ ] | [ ] | [ ] | [ ] |
| Plex for Apple TV | [ ] | [ ] | [ ] | [ ] | [ ] | [ ] |

The overall pass bar is browse and successful playback with seeking on Web, iOS, and Apple TV. Resume and remote-stream results should also be recorded for each.

## Open questions to record

For each question, capture the environment, steps, observed result, supporting sanitized logs, and a conclusion marked yes/no/unclear.

1. **plex.direct certificate:** Can the test server obtain and serve a `*.plex.direct` certificate that clients trust? Record DNS, certificate acquisition method, and client behavior with HTTPS and plain HTTP.
2. **Plex Pass on remote streams:** Test the same account and server on LAN and over the public connection. Record any app or server denial and whether account Pass status changes the result.
3. **Apple TV transcode decision:** Capture the decision request and response for the sample. Record whether the app direct-plays, requests a transcode, or refuses playback, plus the negotiated protocol and media profile.

## Compare responses with a real PMS

Use `pmsdiff` to record a sanitized reference corpus and compare Ether against it. Keep reference fixtures private until reviewed for accidental identifying data.

```sh
pmsdiff record --server http://<pms-host>:32400 --token '<token>' --out testdata/pms --client all
pmsdiff compare --reference testdata/pms --target http://localhost:32400 --token '<ether-token>' --client all --format text
```

Use `--format json` for machine-readable reports. `--client web|ios|appletv` narrows the probe set. `--timeout 90s` sets the per-request deadline (default 90 seconds), and `--concurrency 2` sets the maximum parallel requests (default 2). Individual request failures are logged and included in the report while remaining probes continue. Recorded fixtures include a manifest describing first-50-item listing trims. A nonzero exit status means the report contains request errors or blocking differences. Re-run against the same reference after changes so server-specific behavior remains comparable.

## Collecting diagnostics

For Plex Web, open browser developer tools before connecting. In **Network**, preserve the log, reproduce browse/detail/play/seek, then export a HAR with content. In **Console**, save relevant errors and warnings. Redact `X-Plex-Token`, cookies, account email, public/private IPs, usernames, and server identifiers before sharing. For iOS and Apple TV, note exact steps and app versions and collect the app/server logs through the platform's supported diagnostic export. Never attach raw credentials or an unredacted HAR.
