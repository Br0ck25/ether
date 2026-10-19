# plex.tv integration notes

Research notes for `internal/plextv`, `internal/auth` and `internal/remote`:
how a real Plex Media Server (PMS) registers with plex.tv, and how Ether
copies it. Each item says how sure we are:

- **confirmed**: seen in PMS logs, the official PMS Docker image, or public
  client libraries.
- **inferred**: consistent with what we have seen, but not observed directly.
- **unknown**: the spike must find out.

Sources are listed at the end.

## 1. Identity: which `X-Plex-*` headers Ether sends

Ether uses two identities.

| Use | Product | Client identifier | Provides |
| --- | --- | --- | --- |
| Server: claim exchange, publishing, access tokens, certificate | `Plex Media Server` | `config.Server.MachineIdentifier` | `server` |
| Setup wizard: PIN sign-in only | `Ether` | `config.Plex.ClientIdentifier` | (none) |

Why:

- plex.tv keys a server's device record on its client identifier. The
  `clientIdentifier` of a server in `/api/v2/resources` is the
  `machineIdentifier` that clients then expect from `/identity`. So all
  server calls must send the machine identifier. **confirmed**: the PMS
  Docker first-run script sends `X-Plex-Client-Identifier` set to the
  server's `ProcessedMachineIdentifier`, a 40-hex value. Ether's
  `MachineIdentifier` is 40 hex characters too.
- Clients filter resources on `provides` containing `server`. PMS sends
  `X-Plex-Provides: server` on its claim exchange. **confirmed**.
- Ether reports product `Plex Media Server` and a recent PMS version
  (`plextv.DefaultServerVersion`). Apps reach their server code paths
  through `product` and `provides`, and some gate features on the server
  version (**inferred**). The device name is the owner's friendly name, so
  the owner still sees "Ether" in the server list. `internal/plex` should
  report the same version in `/` and `/identity`. `Registrar.Status()` and
  `plextv.DefaultServerVersion` are the single source for that value.
- The PMS executable's static string table contains the headers
  `X-Plex-Product`, `X-Plex-Version`, `X-Plex-Client-Identifier`,
  `X-Plex-Device-Name`, `X-Plex-Platform`, `X-Plex-Platform-Version`, and
  `X-Plex-Provides`; Ether's `Client.setHeaders` sends these headers.
- The wizard's PIN sign-in uses product `Ether` with a separate client
  identifier. The owner's "Authorized devices" page on plex.tv then shows an
  honest "Ether" entry. That user token is used only to claim the server,
  and is never stored as the server token.
- Platform is `Windows`, `MacOSX` or `Linux`, the values PMS sends.

## 2. PIN sign-in (wizard)

**confirmed** from python-plexapi `MyPlexPinLogin` and the plex.tv API v2:

1. `POST https://plex.tv/api/v2/pins?strong=true` returns JSON
   `{id, code, clientIdentifier, expiresIn, expiresAt, authToken:null}`.
2. Send the owner to
   `https://app.plex.tv/auth#?clientID=<clientId>&code=<code>&context[device][product]=Ether&forwardUrl=<wizard>`.
3. Poll `GET /api/v2/pins/{id}?code=<code>` with the **same** client
   identifier until `authToken` is set. An expired PIN returns 404.
4. `GET /api/v2/user?includeSubscriptions=1&includeProviders=1` with that
   token returns the account XML/JSON, including subscription state. The exact
   query is present in the PMS executable's static string table.

Implemented in `Client.CreatePIN`, `Client.AuthURL`, `Client.CheckPIN`,
`Client.WaitPIN` and `Client.User`.

## 3. Claim

This is the real PMS flow (**confirmed**, from the official `pms-docker`
first-run script, which is how `PLEX_CLAIM` works):

1. With the user's token, `GET https://plex.tv/api/claim/token.json` returns
   `{"token":"claim-xxxx"}`. These are the same short-lived codes that
   `plex.tv/claim` shows.
2. With the **server** identity (headers above, no user token),
   `POST https://plex.tv/api/claim/exchange?token=claim-xxxx` returns a
   `<user …>` XML document containing
   `<authentication-token>SERVER_TOKEN</authentication-token>`.
3. PMS stores that token as `PlexOnlineToken`. It belongs to the owner's
   account and is bound to the server device.

`Client.Claim` does all three steps, then checks that the server token
resolves to the same account. `Registrar.Claim` saves `Plex.Token`,
`OwnerPlexID`, `OwnerUsername` and `Claimed` to the config, then calls
`UserSync.SetOwner`.

Fallback: if the exchange is refused, Ether uses the owner's token directly.
Before claim tokens existed, PMS signed in with the account and used the
account token. The result is reported as `Method = "owner-token"`.

## 4. Publishing connections

### New direct PMS observations (PMS 1.43.4, Windows; 2026-10-18/19)

The owner's filtered DEBUG log shows the current account service host is
`servers.plex.tv`. At startup and then once per minute in the captured run,
PMS issues `GET /api/v2/server/access_tokens`, then `/api/v2/server/users`,
then `/api/v2/server/users/subscriptions`; `users/services` is cached, and
`GET /media/providers` also runs about once a minute. This supersedes the
XML `access_tokens.xml` call as the current primary behaviour. The same
trace includes per-user `/api/v2/server/users/{id}/view_state_sync` reads;
IDs and all token/query values are deliberately omitted here. These are
observations from one server/session, not guaranteed protocol intervals.

Read-only responses are saved as structural redacted fixtures under
`internal/plextv/testdata/`. The access-token endpoint returns XML
`<tokens><token …/></tokens>` records with `type`, `device`, `token`,
`tokenDisabled`, `owned`, and `createdAt`; the observed device-token records
do not include an account ID. Users, subscriptions, and services return XML
`<users><user …/></users>` records with profile attributes and nested
`providers`, `subscriptions`, or `services` collections. Ether uses these
v2 endpoints. When returned tokens lack a user ID, it supplements the
account association from the legacy shared-server endpoint where possible;
it does not infer ownership from response ordering.

After `POST /servers.xml` receives 201, PMS sends
`PUT /api/servers/{machineIdentifier}/connectivity` and receives 200. The
request includes `X-Plex-Token` and `asyncIdentifier` query parameters; values
are per-request and are not reproduced. Registration logs report the
mapped public port (32401 in this capture), not the internal listener port
(32400). PMS then obtains the public address from `v4.plex.tv/pms/:/ip`.
The log confirms these endpoints and ordering. Static executable strings
confirm the path template, token query parameter, async identifier, and
connectivity vocabulary including `privateAddress`, `publicAddress`,
`publicPort`, `localAddresses`, `mappingState`, and `serverIdentifier`. The
observed URL has only the token and async identifier query parameters; Ether
issues that observed PUT without an unverified body.

The captured self-check is an HTTPS GET to the dashed public address under
`plex.direct`, on port **32401**, at `/identity`, returning 200. The exact
address and certificate hash are redacted. PMS logs that the certificate
is not nearing expiry and says it will check again in a week. This verifies
the weekly idle check and public mapped port for this installation; renewal
threshold behavior remains unspecified.

The PMS executable string table confirms the certificate paths
`/api/v2/devices/{machineId}/certificate/subject`, `/csr`, and `/download`.
The CSR URL includes `reason` and `invalidIn`; the binary also contains the
`application/x-pem-file` content type and PEM CSR markers. The subject is
obtained from Plex and supplies `*.<hash>.plex.direct`; the hash is not
derived locally. Read-only GETs to the reference using the owner-approved
server token confirmed that `GET .../certificate/subject` returns an XML
`<certificate>` element with `commonName` and `subject` attributes when sent
with `Accept: application/xml`; the root is `<subject>` with `commonName`
and `subject` attributes. `Accept: application/json` returns HTTP 406, Plex
error 1006. Both `servers.plex.tv` and `plex.tv` returned the XML shape.
Ether requests XML and extracts the assigned wildcard name from `commonName`.
The remaining CSR PUT and certificate download cannot be tested
against the reference because they would mutate production state.

The binary strings also contain `Content-Type`, `application/xml`, and the
registration vocabulary. Ether now sends an XML `<Server>` resource with
`name`, `address`, `port`, `version`, `machineIdentifier`, and
`localAddresses` attributes to `POST https://servers.plex.tv/servers.xml`
with `auth_token` in the query. The live Ether build previously returned 422
from `plex.tv` while sending the old form-encoded alias set; the corrected
host and XML serialization have not been exercised with the production
identity. Exact optional registration attributes remain uncertain because
the PMS body is not present in the filtered logs and native decompilation was
unavailable. REA was invoked
with `npx -y rea-agents@latest analyze`, but no PE native-analysis provider
was configured (Ghidra, Hopper, IDA, and Kuna were unavailable), so the
binary findings here come from its static string table rather than
decompilation. Ether emits the minimal established resource fields; it does
not send speculative mapping aliases in the registration XML.

The capture also contains `POST notifications.plex.tv/api/v1/notifications`
(201), scrobble `/state` traffic, and PMS-generated per-user view-state
syncs. Ether's current integration does not reproduce the notification and
cloud view-state-sync subsystems; they are outside the stable Auth contract.

An older PMS log observed the `PUT /devices/{machineIdentifier}` connection
list update. The current 1.43.4 capture instead confirms the v2 account
service host and the following registration sequence (**confirmed** methods,
paths, and statuses):

```
HTTP requesting POST https://servers.plex.tv/servers.xml?auth_token=…               -> 201
HTTP requesting PUT  https://servers.plex.tv/api/servers/{machineId}/connectivity?... -> 200
PublicAddressManager: Got public IP from v4.plex.tv/pms/:/ip
```

- `PUT /devices/{machineIdentifier}` with one `Connection[][uri]` per URI is
  how connections reach the resources list. With no addresses, PMS sends a
  single empty `Connection[][uri]=`.
  `Client.PublishConnections` sends exactly this.
- `POST /servers.xml` registers the mapped public port; PMS reports port
  32401 in the supplied capture while its listener is 32400. The binary
  contains XML content-type and field strings including `name`, `address`,
  `port`, `machineIdentifier`, `version`, `productVersion`,
  `localAddresses`, `mappingState`, `privateAddress`, `publicAddress`,
  `publicPort`, `remotePublicAddress`, and `serverIdentifier`. The filtered
  log omits the POST body, so the minimal resource XML Ether sends is an
  evidence-based implementation, while exact optional fields remain unknown.
  Registration errors remain non-fatal because resources connection
  publishing is separate.
- PMS publishes plain `http://ip:port` URIs. plex.tv builds the
  `https://a-b-c-d.<hash>.plex.direct:port` variants itself for clients
  that ask with `includeHttps=1` (**inferred**: resources show both
  variants for every address). Ether therefore publishes http URIs for LAN,
  WAN and custom URLs. `RegistrarOptions.PublishPlexDirect` adds the https
  ones too, in case the spike shows plex.tv wants them.
- `publicAddress` and `publicAddressMatches` in resources are computed by
  plex.tv. `publicAddress` is the source IP the server talks to plex.tv
  from. `publicAddressMatches` is true when the client's IP equals it. That
  flag is how apps decide whether a server is local. Ether cannot set it
  (**inferred**).
- Ether re-publishes when the LAN or WAN address changes (checked every
  5 min), when the UPnP mapping changes, when `Trigger()` is called, and
  every hour regardless. Failures back off from 1 min to 32 min.
- After publishing, `GET /api/v2/resources?includeHttps=1&includeRelay=1&includeIPv6=1`
  checks that the server is listed. It also records what clients will see
  (`Status.Listed`, `Status.ResourceConnections`, `Status.PublicAddress`).

## 5. Friends and their tokens

**confirmed** from the supplied PMS 1.43.4 log: `GET
https://servers.plex.tv/api/v2/server/access_tokens?auth_token=…&unknownToken=…`,
then `/api/v2/server/users` and `/api/v2/server/users/subscriptions`. PMS
also calls `/api/v2/user?includeSubscriptions=1&includeProviders=1` on
`servers.plex.tv`; its XML user response includes subscription state. The
captured access-token response contains device-token rows without account
IDs. PMS sends an additional opaque `unknownToken` query value; the available
logs and static strings do not reveal how it is generated. Ether omits it,
and the authorized read-only capture returned the token collection without
that parameter. Ether uses v2 as primary and retains the older
`access_tokens.xml` behavior only as a compatibility fallback.

This is how a server validates friends locally:

- plex.tv issues each account that has access a **server access token**.
  Official apps get it as `accessToken` on the server's entry in their own
  `/api/v2/resources`, and send it as `X-Plex-Token` to the server.
- PMS downloads every access token for its machine identifier and matches
  incoming tokens against that list. Users don't hit plex.tv on each
  request.
- The owner's entries carry `owned="1"`.
- The captured v2 access-token response is XML `<tokens><token …/></tokens>`
  with device-token rows (`type`, `device`, `token`, `tokenDisabled`,
  `owned`, `createdAt`) and no account ID. The PMS's `unknownToken` query
  behavior remains unknown; the account association is refreshed through the
  separate users and subscriptions endpoints.
- Fallback (**confirmed** by Tautulli and python-plexapi):
  `GET /api/servers/{machineId}/shared_servers`. It lists
  `<SharedServer userID username email accessToken acceptedAt …>` with
  `<Section key shared>` children. Profiles are filled in from
  `GET /api/users` (`<User id title username email thumb home restricted><Server machineIdentifier/></User>`).

`internal/auth` stores only SHA-256 hashes of tokens. It recognises:

1. the server token (owner);
2. synced access tokens (owner or friend);
3. any other token, by asking plex.tv `GET /api/v2/user`. The token is
   accepted only if the account is the owner, a synced friend, or (for
   home users and fresh shares) has this server in its own resources list.

Validated tokens are cached for 15 min and rejected tokens for 2 min.
Concurrent lookups of one token share a single plex.tv call. If plex.tv is
unreachable, a token that validated before keeps working for 24 h. Friends
who disappear from the sync are deactivated, but their history and settings
are kept. A friend whose invite is still pending gets no access.

Sections: the owner can share specific sections on plex.tv. Those section
keys are stored as `plex_sections`. If the owner has set no allowed
sections in Ether, the plex.tv selection becomes the user's
`AllowedSections`. An Ether-side setting always wins.

## 6. plex.direct certificates (open question 1)

**Unknown: certificate issuance for Ether has not been tested.** The owner has
a claimed Ether instance, but a branch test instance would share its machine
identifier; do not register or submit a CSR using that identity. The reference
PMS identity was used only for read-only subject GETs.

What PMS does (**confirmed** by the PMS executable's strings and public
PMS logs; exact request headers/body are still partly unknown):

1. `GET https://servers.plex.tv/api/v2/devices/{machineId}/certificate/subject`
   with `Accept: application/xml` returns HTTP 200 and a `<subject>` element
   whose `commonName` is `*.<hash>.plex.direct`. A JSON Accept returns 406
   (code 1006). The assigned wildcard is not derived locally.
2. `PUT https://servers.plex.tv/api/v2/devices/{machineId}/certificate/csr?reason=missing&invalidIn=0`
   returns 204. When renewing, the call is `reason=expiring&invalidIn=<seconds, negative once expired>`.
   The executable contains the `application/x-pem-file` content type and
   Ether sends a PEM-encoded RSA-2048 CSR with subject and SAN set to the
   returned wildcard.
3. `GET …/certificate/download` returns 202 while the certificate is pending
   and 200 once it is ready. PMS polls for about 90 s, then fails with 408.
4. PMS stores the certificate as `Cache/cert-v2.p12`, alongside the
   `CertificateUUID` and `CertificateVersion` prefs.

The hash:

- It is **assigned by plex.tv** per server and returned by the `subject`
  call. It is not derived locally.
- Any `a-b-c-d.<hash>.plex.direct` name resolves to `a.b.c.d` through
  plex.tv's DNS, so one wildcard certificate covers every LAN and WAN IP
  (Filippo Valsorda's write-up; DigiCert issued the original
  certificates). Reported lifetimes have shrunk; recent forum posts mention
  certificates that need renewal roughly monthly.
- `remote.PlexDirectHost` builds these names. IPv4 dots become dashes. IPv6
  becomes the 8 expanded groups joined by dashes (**inferred**; check
  against an IPv6 resources entry).

Rate limiting (**confirmed**, many forum threads): plex.tv returns
`429 "API rate limit exceeded" (code 1003)` on CSR uploads from a server
that asks too often. Affected servers stayed locked out until Plex staff
reset them. `CertManager` therefore:

- renews only when a third of the lifetime is left (at least 7 days);
- waits 24 h after HTTP 429 and 5 min after other 4xx client errors (likely
  request-format bugs); other failures retain the 24 h default;
- writes that back-off to `cert-state.json` so it survives restarts;
- never requests a certificate when the current one is a valid plex.direct
  certificate.

Still unknown: whether the subject endpoint returns a hash for an Ether
device, and whether Plex will issue it a certificate. Ether's
`Client.RequestPlexDirectCertificate` makes the exact interoperability
attempt: GET `/subject` with `Accept: application/xml`, using the server
identity and server token; PUT `/csr?reason=missing&invalidIn=0` with an
`application/x-pem-file` PEM CSR; then GET `/download` until ready (up to
90 seconds). It accepts PEM, DER, or JSON-wrapped PEM and verifies the
certificate's key and wildcard coverage. The live Ether install already owns
its machine identifier, so do not run this flow from a copied branch instance.
The owner-approved reference server was used only for read-only GETs; no
CSR/registration was sent for it.

**If no certificate can be obtained**, Ether generates a self-signed
ECDSA certificate covering `localhost`, the LAN IPs and
`*.<hash>.plex.direct` when a hash is known, and serves it.

Expected client behaviour (**inferred** from PMS behaviour with secure
connections set to "Preferred", and from forum reports of expired PMS
certificates):

- Official apps reject an untrusted certificate for https connections.
  With `Secure connections = preferred` (Ether's default) they fall back to
  plain `http://` on the LAN. On a WAN connection, some apps fall back to
  http only after a warning; newer apps may refuse. The relay is not
  available to third-party servers.
- With `required`, apps cannot connect at all without a trusted
  certificate.
- Ether keeps publishing http URIs unless `required` is set. Without a
  plex.direct hash it never publishes https URIs; the
  `HTTPSOnPlainIP` option is for owners who run their own trusted
  certificate.
- The spike must record what Web, iOS and Apple TV do on LAN and WAN with
  (a) a plex.direct certificate, (b) a self-signed certificate,
  (c) http only.

## 7. Plex Pass for remote streaming (open question 2)

The design doc says "since April 2026". Plex's own article puts the start
at **29 April 2025**. That is when remote playback of personal video began
to need one of:

- the server owner's Plex Pass (which then covers every user streaming from
  that server);
- the viewer's Plex Pass; or
- the viewer's Remote Watch Pass.

Rollout by platform:

| Date | Platform |
| --- | --- |
| April 2025 | New mobile apps |
| November 2025 | Roku |
| Spring 2026 | Fire TV, Samsung, LG, Vizio, PlayStation and Xbox |

Plex also said the rule would reach third-party clients using the API in
2026. Ars Technica reported that a Plex Pass is needed to *enable* remote
access on the server.

What this means for Ether (**inferred**; the spike must measure):

- The check happens **in the client apps and plex.tv**, from account
  entitlements: the server owner's subscription (via the resource's
  `ownerId`/`owned`) and the viewer's own. "Remote" is decided on the
  client from `publicAddressMatches` and connection `local` flags. Ether
  cannot grant or forge either the entitlement or `publicAddressMatches`,
  and should not try.
- So a friend on a remote network can play from Ether only if the owner or
  the friend has Plex Pass or Remote Watch Pass. LAN playback is never
  affected.
- Ether reports the owner's subscription in `Status.PlexPass` (from
  `/api/v2/user` `subscription.active`) so the admin UI can explain this.
  `internal/plex` should report the true value in `myPlexSubscription` on
  `/` (the root response).
- Open for the spike: whether apps also read server-side flags, such as
  `myPlexSubscription` in the root or `/:/prefs`, and what an app shows
  when the check fails (a paywall or a playback error).

## 8. Remote access, UPnP and reachability

- WAN IP: `GET https://v4.plex.tv/pms/:/ip` returns plain text (**confirmed**
  in current PMS logs). Fallbacks are ipify, icanhazip and
  ifconfig.me. The UPnP gateway's `GetExternalIPAddress` is preferred when
  it returns a public address. Behind CGNAT it returns a 100.64/10 address,
  which Ether ignores.
- Port mapping: UPnP IGD (WANIPConnection v2, then v1, then
  WANPPPConnection v1) is found by SSDP. NAT-PMP (RFC 6886) to the default
  gateway is the fallback. Ether requests a 2 h lease and renews at half
  that. If the router answers error 725 (permanent leases only), Ether
  maps permanently and re-asserts the mapping hourly. It replaces stale
  entries of its own (error 718) and reports conflicts with other hosts.
  The mapping is deleted on `Registrar.Stop()`. PMS logs the same steps:
  "NAT: UPnP, usable device … public address is …". Both are implemented in
  pure Go (no goupnp dependency), and tested against a fake IGD and a fake
  NAT-PMP gateway.
- Reachability: `remote.ReachabilityChecker` fetches `<WAN uri>/identity`
  and compares `machineIdentifier`. Probing your own WAN address from
  inside the LAN needs NAT loopback on the router, so a failure means
  "could not verify", not "unreachable". PMS's own check is done by
  plex.tv: Settings → Remote Access asks plex.tv to connect back. That
  endpoint was not identified with confidence, so it is not implemented
  yet; adding it is a spike follow-up.

## 9. GDM (LAN discovery)

**confirmed** by python-plexapi `gdm.py` and community write-ups:

- Clients send `M-SEARCH * HTTP/1.1` to UDP **32414** on multicast group
  `239.0.0.250`, and also by broadcast.
- The server replies unicast:

  ```
  HTTP/1.0 200 OK
  Content-Type: plex/media-server
  Resource-Identifier: <machineIdentifier>
  Name: <friendly name>
  Port: 32400
  Updated-At: <unix seconds>
  Version: <pms version>
  ```

- PMS listens on 32410, 32412, 32413 and 32414. Players announce
  themselves with `HELLO * HTTP/1.0` (and `BYE`) on 32413.
  `remote.GDMResponder` answers M-SEARCH on all four ports. Server HELLO
  announcements are optional (`GDMOptions.Announce`, off by default),
  because current clients find servers by searching.
- The responder binds with `SO_REUSEADDR` (through `net.ListenMulticastUDP`).
  Ports that fail to bind, for example when a real PMS runs on the same
  host, are skipped with a warning.

## 10. Wiring summary (for `internal/app`)

```go
cfgStore, _ := config.Open(dataDir)
cfg := cfgStore.Get()
client, _ := plextv.New(plextv.Options{Identity: plextv.ServerIdentity(cfg, "")})
users, _ := auth.New(ctx, db, auth.Options{Validator: auth.PlexTVValidator{Client: client, MachineID: cfg.Server.MachineIdentifier}})
_ = users.BootstrapOwner(ctx, cfg.Plex)
certs, _ := plextv.NewCertManager(plextv.CertManagerOptions{Dir: filepath.Join(dataDir, "certs"), Client: client,
	CustomCertFile: cfg.Plex.CertFile, CustomKeyFile: cfg.Plex.KeyFile})
reg, _ := plextv.NewRegistrar(plextv.RegistrarOptions{Config: cfgStore, Client: client, Users: users, Certs: certs})
cfgStore.Subscribe(func(config.Config) { reg.Trigger() })
reg.Start(ctx); defer reg.Stop()
gdm := remote.NewGDMResponder(remote.GDMOptions{Info: func() remote.GDMInfo { … }})
gdm.Start(ctx); defer gdm.Stop()
// TLS listener: certs.TLSConfig(); flush users.FlushSeen periodically.
```

## Sources

- Owner-approved filtered PMS 1.43.4 DEBUG log at the private local
  thread-storage path supplied for this task (reviewed locally; no raw log,
  identifiers, tokens, addresses or hashes are copied into this repository).
- Static string table from the owner-approved PMS executable at
  `~/Documents/.ether-ref/pms-bin/Plex Media Server.exe`; the binary is not
  copied into this repository.
- Older PMS debug log with MyPlex, devices PUT, servers.xml and access_tokens
  calls: https://gist.github.com/keithah/645bad865984f296053e054a630eb8ef
- Plex forum, certificate flow and 429 rate limiting:
  - https://forums.plex.tv/t/plex-certificate-missing-csr-request-returns-429/942171
  - https://forums.plex.tv/t/timeout-trying-to-get-certificate/939784
  - https://forums.plex.tv/t/pms-stuck-in-certificate-acquisition-loop-persistent-api-rate-limit-exceeded-error-429/938964
  - https://forums.plex.tv/t/resolved-502-error-renewing-expired-server-ssl-certificate/937826
- Filippo Valsorda, "How Plex is doing HTTPS for all its users":
  https://words.filippo.io/how-plex-is-doing-https-for-all-its-users/
- Plex, Requirements for Remote Playback of Personal Media:
  https://support.plex.tv/articles/requirements-for-remote-playback-of-personal-media/
- Plex blog, Important 2025 Plex Updates:
  https://www.plex.tv/blog/important-2025-plex-updates/
- How-To Geek, remote play restrictions on TVs:
  https://www.howtogeek.com/plex-is-now-enforcing-remote-play-restrictions-on-tvs/
- Privacy Guides, enforcement news (Nov 2025):
  https://www.privacyguides.org/news/2025/11/26/plex-begins-enforcing-new-restrictions-on-remote-streaming-this-week/
- PMS API reference (connection objects with plex.direct URIs):
  https://developer.plex.tv/pms/
- plexinc/pms-docker first-run script (claim exchange headers and
  `<authentication-token>`), python-plexapi `myplex.py` and `gdm.py`, and
  Tautulli `plextv.py` (`shared_servers` `accessToken`): reference
  implementations, read for behaviour only.
