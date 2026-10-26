import type { FieldGroup } from "./SchemaForm";

export const SERVER: FieldGroup[] = [
  {
    title: "Server",
    fields: [
      {
        key: "friendlyName",
        label: "Server name",
        kind: "text",
        help: "How the server appears in Plex apps.",
      },
      {
        key: "port",
        label: "Port",
        kind: "number",
        min: 1,
        max: 65535,
        step: 1,
        help: "Plex apps expect 32400. A restart is needed after changing it.",
      },
      {
        key: "bindAddress",
        label: "Listen address",
        kind: "text",
        help: "0.0.0.0 listens on every network interface.",
      },
      {
        key: "machineIdentifier",
        label: "Machine identifier",
        kind: "readonly",
        help: "Generated on first run; never changes.",
      },
    ],
  },
  {
    title: "Streams",
    fields: [
      {
        key: "defaultStreamLimit",
        label: "Streams per viewer",
        kind: "number",
        min: 0,
        step: 1,
        help: "Default simultaneous streams for each viewer. 0 = unlimited.",
      },
      {
        key: "maxStreams",
        label: "Server-wide limit",
        kind: "number",
        min: 0,
        step: 1,
        help: "0 derives the limit from your Usenet connections.",
      },
      {
        key: "logLevel",
        label: "Log level",
        kind: "select",
        options: [
          { value: "debug", label: "Debug" },
          { value: "info", label: "Info" },
          { value: "warn", label: "Warnings" },
          { value: "error", label: "Errors only" },
        ],
      },
    ],
  },
];

/** server.* keys each cluster node keeps for itself (internal/config/scope.go). */
const NODE_LOCAL_SERVER_KEYS = new Set(["port", "bindAddress", "logLevel"]);

/**
 * General's server groups on a cluster node: settings shared by every node
 * stay in their groups, and this node's own settings (listener, logging and
 * its weight) gather under "This node".
 */
export function clusterServerGroups(): FieldGroup[] {
  const shared = SERVER.map((g) => ({
    ...g,
    fields: g.fields.filter((f) => !NODE_LOCAL_SERVER_KEYS.has(f.key)),
  }));
  const local = SERVER.flatMap((g) => g.fields.filter((f) => NODE_LOCAL_SERVER_KEYS.has(f.key)));
  return [
    ...shared,
    {
      title: "This node",
      description: "These apply only to this node; every other setting is shared by the cluster.",
      fields: [
        ...local,
        {
          key: "clusterWeight",
          label: "Node weight",
          kind: "number",
          min: 0,
          max: 100,
          step: 1,
          help: "This node's share of each provider's connections and request limits, relative to the other nodes. 2 gets twice the share of a node at 1; 0 means 1.",
        },
      ],
    },
  ];
}

export const PLEX: FieldGroup[] = [
  {
    title: "Remote access",
    description: "Let Plex apps reach Ether from outside your network.",
    fields: [
      {
        key: "remoteAccess",
        label: "Allow remote access",
        kind: "bool",
        help: "Publishes your public address to plex.tv.",
      },
      {
        key: "useUpnp",
        label: "Map the port automatically (UPnP)",
        kind: "bool",
        help: "Asks your router to forward the port.",
      },
      {
        key: "publicPort",
        label: "Public port",
        kind: "number",
        min: 0,
        max: 65535,
        step: 1,
        help: "The port forwarded on your router. 0 uses the server port.",
      },
      {
        key: "publicHost",
        label: "Public host name",
        kind: "text",
        help: "Optional. Leave empty to detect your public address.",
      },
      {
        key: "customUrls",
        label: "Custom server URLs",
        kind: "list",
        placeholder: "https://ether.example.com:443",
        help: "Extra addresses Plex apps should try, comma-separated.",
      },
    ],
  },
  {
    title: "Secure connections",
    fields: [
      {
        key: "secureConnections",
        label: "Secure connections",
        kind: "select",
        options: [
          { value: "required", label: "Required" },
          { value: "preferred", label: "Preferred" },
          { value: "disabled", label: "Disabled" },
        ],
      },
      {
        key: "certFile",
        label: "Certificate file",
        kind: "text",
        wide: true,
        help: "Leave empty to use the plex.direct certificate Ether manages.",
      },
      { key: "keyFile", label: "Key file", kind: "text", wide: true },
    ],
  },
  {
    title: "Account",
    fields: [
      { key: "ownerUsername", label: "Owner", kind: "readonly" },
      {
        key: "claimed",
        label: "Claimed",
        kind: "bool",
        help: "Set by the setup wizard when the server is linked to your Plex account.",
      },
      { key: "token", label: "Owner token", kind: "secret" },
      { key: "clientIdentifier", label: "Client identifier", kind: "readonly" },
      { key: "ownerPlexId", label: "Owner Plex ID", kind: "readonly" },
      { key: "certHash", label: "plex.direct hash", kind: "readonly" },
    ],
  },
  {
    title: "Compatibility",
    fields: [
      {
        key: "trackLatestVersion",
        label: "Report the latest public PMS version",
        kind: "bool",
        help: "Off reports the version Ether is verified against. Plex apps turn on features by server version, so tracking can expose features Ether doesn't support.",
      },
      {
        key: "compatibilityVersion",
        label: "Compatibility version override",
        kind: "text",
        help: "Leave empty to use the verified version, or the latest public version when tracking is on.",
      },
      { key: "latestCompatibilityVersion", label: "Latest public PMS version", kind: "readonly" },
    ],
  },
];

export const CACHE: FieldGroup[] = [
  {
    title: "Disk cache",
    description: "Segments from Usenet are cached on disk so seeks and rewatches are instant.",
    fields: [
      { key: "dir", label: "Cache folder", kind: "text", wide: true },
      {
        key: "budgetBytes",
        label: "Disk budget",
        kind: "bytes",
        unit: "GiB",
        help: "The oldest segments are evicted when the cache reaches this size.",
      },
    ],
  },
  {
    title: "Prefetch",
    fields: [
      {
        key: "prefetchNextEpisode",
        label: "Prefetch the next episode",
        kind: "bool",
        help: "Warms the opening of the next episode so it starts instantly.",
      },
      {
        key: "nextEpisodeAt",
        label: "Start prefetching at",
        kind: "number",
        min: 0,
        max: 1,
        step: 0.05,
        help: "Fraction of the current episode watched (0–1).",
      },
      {
        key: "prefetchBytes",
        label: "Prefetch size",
        kind: "bytes",
        unit: "MiB",
        help: "How much of the next episode to pull ahead.",
      },
      {
        key: "keepInProgressWarm",
        label: "Keep in-progress titles warm",
        kind: "bool",
        help: "Keeps the resume point of unfinished titles cached.",
      },
      {
        key: "inProgressPinMaxAge",
        label: "Stop keeping a title warm after",
        kind: "duration",
        placeholder: "720h",
        help: "An unfinished title nobody has watched for this long loses its cache pin. Empty uses 30 days.",
      },
      {
        key: "inProgressPinPercent",
        label: "Share of the cache for in-progress titles",
        kind: "number",
        min: 0,
        max: 100,
        step: 5,
        help: "Percent of the disk budget unfinished titles may keep pinned; the least recently watched lose their pin first. 0 uses 50.",
      },
    ],
  },
];

export const CATALOG: FieldGroup[] = [
  {
    title: "TMDB",
    fields: [
      {
        key: "tmdbKey",
        label: "Your TMDB API key",
        kind: "secret",
        help: "Your personal TMDB v3 API key or v4 read-access token.",
      },
      {
        key: "language",
        label: "Language",
        kind: "text",
        help: "Metadata language, e.g. en-US or fr-FR.",
      },
      {
        key: "region",
        label: "Region",
        kind: "text",
        help: "Two-letter region for release dates and streaming availability.",
      },
      { key: "refreshInterval", label: "Refresh every", kind: "duration" },
    ],
  },
  {
    title: "What's browsable",
    description: "Thresholds keep obscure titles out of Plex libraries; they remain searchable.",
    fields: [
      { key: "minPopularity", label: "Minimum popularity", kind: "number", min: 0, step: 1 },
      { key: "minVotes", label: "Minimum votes", kind: "number", min: 0, step: 10 },
      { key: "includeAdult", label: "Include adult titles", kind: "bool" },
    ],
  },
];

export const RESOLVER: FieldGroup[] = [
  {
    title: "Release selection",
    fields: [
      {
        key: "allowCam",
        label: "Allow CAM and telesync releases",
        kind: "bool",
        help: "Off by default: these are recorded in cinemas.",
      },
      {
        key: "maxCandidates",
        label: "Releases to keep ready",
        kind: "number",
        min: 0,
        step: 1,
        help: "Fallback releases kept for instant failover.",
      },
      {
        key: "healthSamples",
        label: "Health check samples",
        kind: "number",
        min: 0,
        step: 1,
        help: "Articles checked before a release is trusted.",
      },
      { key: "searchCacheTtl", label: "Remember searches for", kind: "duration" },
      { key: "unavailableCooloff", label: "Retry unavailable titles after", kind: "duration" },
    ],
  },
  {
    title: "Blocklists",
    fields: [
      {
        key: "blockedGroups",
        label: "Blocked release groups",
        kind: "list",
        placeholder: "GROUP1, GROUP2",
      },
      {
        key: "blockedTerms",
        label: "Blocked terms",
        kind: "list",
        placeholder: "hardcoded, korsub",
      },
    ],
  },
];

export const TRANSCODE: FieldGroup[] = [
  {
    title: "Transcoding",
    fields: [
      {
        key: "hwAccel",
        label: "Hardware acceleration",
        kind: "select",
        options: [
          { value: "auto", label: "Automatic" },
          { value: "none", label: "Off (software)" },
          { value: "videotoolbox", label: "VideoToolbox (Apple)" },
          { value: "nvenc", label: "NVENC (NVIDIA)" },
          { value: "qsv", label: "Quick Sync (Intel)" },
          { value: "vaapi", label: "VA-API (Linux)" },
        ],
      },
      {
        key: "vaapiDevice",
        label: "VA-API device",
        kind: "text",
        help: "Only for VA-API, e.g. /dev/dri/renderD128.",
      },
      { key: "maxTranscodes", label: "Simultaneous transcodes", kind: "number", min: 0, step: 1 },
      {
        key: "remoteMaxBitrateKbps",
        label: "Remote bitrate cap",
        kind: "number",
        min: 0,
        step: 500,
        unit: "kbps",
        help: "0 = no cap.",
      },
    ],
  },
  {
    title: "Server video enhancement",
    description:
      "Use NVIDIA RTX Video Super Resolution on this server to improve lower-resolution video before streaming. On supported Windows x64 servers, enabling this installs and verifies the required runtime automatically. Enhancement uses additional GPU capacity.",
    fields: [
      {
        key: "rtxVideo",
        label: "RTX Video Super Resolution",
        kind: "group",
        fields: [
          {
            key: "mode",
            label: "RTX Video Super Resolution",
            kind: "select",
            options: [
              { value: "off", label: "Off" },
              { value: "auto", label: "Automatic · local streams" },
              { value: "on", label: "On · eligible streams" },
            ],
            help: "Eligible videos are enhanced within the viewer's resolution and bitrate limits. Unsupported videos use ordinary playback.",
          },
          {
            key: "sourceMaxHeight",
            label: "Enhance sources up to",
            kind: "number",
            min: 144,
            max: 2160,
            step: 1,
            unit: "p",
          },
          {
            key: "targetMaxHeight",
            label: "Enhanced resolution limit",
            kind: "number",
            min: 144,
            max: 2160,
            step: 1,
            unit: "p",
          },
          {
            key: "quality",
            label: "Enhancement quality",
            kind: "select",
            numeric: true,
            options: [
              { value: "1", label: "Low" },
              { value: "2", label: "Medium" },
              { value: "3", label: "High" },
              { value: "4", label: "Ultra" },
              { value: "21", label: "Streaming medium" },
              { value: "23", label: "Streaming ultra" },
            ],
            help: "Higher quality uses more GPU resources. Streaming modes require an Ampere or newer GPU. Start with Medium.",
          },
          {
            key: "maxConcurrent",
            label: "Simultaneous enhancements",
            kind: "number",
            min: 1,
            max: 8,
            step: 1,
            help: "Additional streams use ordinary playback when this limit is reached.",
          },
          {
            key: "executablePath",
            label: "NVEnc executable path",
            kind: "text",
            help: "Optional path to your own NVEncC64.exe. Leave empty for automatic setup; custom installations are never overwritten.",
          },
        ],
      },
    ],
  },
  {
    title: "ffmpeg",
    fields: [
      {
        key: "ffmpegPath",
        label: "ffmpeg path",
        kind: "text",
        wide: true,
        help: "Leave empty to use the bundled ffmpeg.",
      },
      { key: "ffprobePath", label: "ffprobe path", kind: "text", wide: true },
      { key: "tempDir", label: "Temporary folder", kind: "text", wide: true },
    ],
  },
];

export const TRAKT: FieldGroup[] = [
  {
    title: "Trakt",
    description: "Sync watch history and lists with Trakt.",
    fields: [
      { key: "enabled", label: "Enabled", kind: "bool" },
      { key: "clientId", label: "Client ID", kind: "text", wide: true },
      { key: "clientSecret", label: "Client secret", kind: "secret" },
    ],
  },
];

export const OPENSUBTITLES: FieldGroup[] = [
  {
    title: "OpenSubtitles",
    description: "Fetch subtitles when a release doesn't include them.",
    fields: [
      { key: "enabled", label: "Enabled", kind: "bool" },
      { key: "apiKey", label: "API key", kind: "secret" },
      { key: "username", label: "Username", kind: "text" },
      { key: "password", label: "Password", kind: "secret" },
    ],
  },
];

export const UPDATES: FieldGroup[] = [
  {
    title: "Release channel",
    fields: [
      {
        key: "channel",
        label: "Channel",
        kind: "select",
        options: [
          { value: "stable", label: "Stable" },
          { value: "beta", label: "Beta" },
        ],
      },
      {
        key: "autoUpdate",
        label: "Install updates automatically",
        kind: "bool",
        help: "Installs during the maintenance window, once nobody is watching.",
      },
      {
        key: "maintenanceStart",
        label: "Maintenance window starts",
        kind: "text",
        placeholder: "03:00",
        help: "24-hour local time.",
      },
      {
        key: "maintenanceEnd",
        label: "Maintenance window ends",
        kind: "text",
        placeholder: "06:00",
        help: "May wrap past midnight.",
      },
      {
        key: "feedUrl",
        label: "Update feed",
        kind: "text",
        wide: true,
        help: "Leave empty for the official feed.",
      },
    ],
  },
];

export const SPIKE: FieldGroup[] = [
  {
    title: "Test playback with a local file",
    description:
      "Serve a file from this computer to your Plex apps to tell whether a playback problem is the app or the stream. This is a diagnostic, not a library; turn it off when you're done.",
    fields: [
      { key: "enabled", label: "Serve test files", kind: "bool" },
      {
        key: "localFiles",
        label: "Test files",
        kind: "list",
        placeholder: "/path/to/movie.mkv",
        help: "Full paths to video files on this computer.",
      },
    ],
  },
];

export const QUALITY_SOURCES = [
  "remux",
  "bluray",
  "webdl",
  "webrip",
  "hdtv",
  "dvd",
  "screener",
  "cam",
];
export const QUALITY_CODECS = ["av1", "hevc", "h264", "vp9", "mpeg2"];
export const QUALITY_AUDIO = ["truehd", "dtshd", "eac3", "ac3", "dts", "aac", "flac", "opus"];

export const MARKERS: FieldGroup[] = [
  {
    title: "Chapters and skip markers",
    fields: [
      {
        key: "theIntroDb",
        label: "Look up intro and credits markers",
        kind: "bool",
        help: "Use the public TheIntroDB lookup, cached per episode and runtime.",
      },
      { key: "apiKey", label: "Optional TheIntroDB key", kind: "secret" },
    ],
  },
];

/** Debrid section settings, edited in place under the provider list (Sources › Debrid). */
export const DEBRID: FieldGroup[] = [
  {
    title: "Torrent handling",
    fields: [
      {
        key: "cachedOnly",
        label: "Cached torrents only",
        kind: "bool",
        help: "Only use torrents your debrid service already holds. Nothing is downloaded and playback never waits.",
      },
      {
        key: "removeUncached",
        label: "Remove uncached torrents",
        kind: "bool",
        help: "Remove torrents Ether added that turned out not to be cached. Cached torrents stay on your account so the next play starts instantly and they are reused instead of re-added.",
      },
    ],
  },
  {
    title: "Advanced",
    description: "Leave at 0 for the built-in behaviour.",
    fields: [
      {
        key: "chunkBytes",
        label: "Largest range request",
        kind: "bytes",
        unit: "MiB",
        help: "Requests start at 512 KiB for a fast first byte and grow up to this size. 0 = 8 MiB; at most 64 MiB.",
      },
      {
        key: "readAheadChunks",
        label: "Fixed read-ahead",
        kind: "number",
        min: -1,
        max: 32,
        step: 1,
        unit: "chunks",
        help: "0 adapts read-ahead to the measured download speed. Set up to 32 chunks to fix the window; -1 turns read-ahead off.",
      },
      {
        key: "maxConnections",
        label: "Connections per file",
        kind: "number",
        min: 0,
        max: 16,
        step: 1,
        help: "Concurrent range requests per open file. 0 = 4; at most 16.",
      },
    ],
  },
];

/** Plex offline downloads (General › Downloads). */
export const DOWNLOADS: FieldGroup[] = [
  {
    title: "Downloads",
    description:
      "Lets Plex apps save titles for offline viewing. Ether remuxes each one on demand.",
    fields: [
      { key: "enabled", label: "Allow downloads", kind: "bool" },
      {
        key: "allowFriends",
        label: "Friends can download",
        kind: "bool",
        help: "Off lets only you download. A viewer's access policy can still allow or deny it.",
      },
      {
        key: "maxHeight",
        label: "Highest resolution",
        kind: "number",
        min: 0,
        step: 1,
        unit: "p",
        help: "Downloads are capped at this height. 0 uses 1080p.",
      },
    ],
  },
  {
    title: "Limits",
    fields: [
      {
        key: "maxConcurrent",
        label: "Downloads at once",
        kind: "number",
        min: 0,
        step: 1,
        help: "Across the whole server. 0 uses 2.",
      },
      {
        key: "maxConcurrentPerUser",
        label: "Downloads at once per viewer",
        kind: "number",
        min: 0,
        step: 1,
        help: "0 uses 1.",
      },
      {
        key: "diskBudgetBytes",
        label: "Temporary disk space",
        kind: "bytes",
        unit: "GiB",
        help: "Prepared files waiting to be fetched. 0 uses 20 GiB.",
      },
      {
        key: "expiry",
        label: "Keep prepared files for",
        kind: "duration",
        help: "A finished download is deleted after this long if the app hasn't fetched it.",
      },
    ],
  },
];

/** Per-viewer touches in Plex apps (General › Personalization). */
export const PERSONALIZE: FieldGroup[] = [
  {
    title: "Personalization",
    description: "Per-viewer touches in Plex apps. Turn any off for plain Plex behaviour.",
    fields: [
      {
        key: "postPlay",
        label: "Personal “up next” picks",
        kind: "bool",
        help: "After a movie or finale, suggest what this viewer is most likely to watch next.",
      },
      {
        key: "postPlayPrebuffer",
        label: "Pre-buffer the top pick",
        kind: "bool",
        help: "Warm the first suggestion in the last minutes so it starts instantly.",
      },
      {
        key: "microReasons",
        label: "Match and finish time on details",
        kind: "bool",
        help: "Show “96% Match · Ends at 10:45 PM” on a title's detail page.",
      },
      {
        key: "sessionIntent",
        label: "Adapt Home to tonight's browsing",
        kind: "bool",
        help: "Titles a viewer opened but didn't play steer the rows they see next.",
      },
      {
        key: "clockFormat",
        label: "Finish time format",
        kind: "select",
        options: [
          { value: "", label: "From the language" },
          { value: "12h", label: "12-hour" },
          { value: "24h", label: "24-hour" },
        ],
      },
    ],
  },
];

/** Trailers and extras (General › Trailers & extras). */
export const EXTRAS: FieldGroup[] = [
  {
    title: "Trailers & extras",
    description:
      "TMDB only lists YouTube and Vimeo trailers, which Plex apps can't play. Point Ether at a yt-dlp-compatible tool you install to turn them into playable streams.",
    fields: [
      {
        key: "enabled",
        label: "Trailers and Cinema Trailers",
        kind: "bool",
        help: "Without a working tool no extras are offered.",
      },
      {
        key: "resolverCommand",
        label: "Resolver command",
        kind: "text",
        wide: true,
        placeholder: "yt-dlp",
        help: "Path or command name. Ether doesn't ship one.",
      },
      {
        key: "resolverArgs",
        label: "Arguments",
        kind: "list",
        placeholder: "-g, -f, b[ext=mp4]/b, --no-playlist, {url}",
        help: "Leave empty for the yt-dlp default. {url}, {site}, {key} and {id} are filled in; no shell is used.",
      },
      { key: "resolverTimeout", label: "Give up after", kind: "duration" },
      {
        key: "prewarmBytes",
        label: "Pre-buffer the feature",
        kind: "bytes",
        unit: "MiB",
        help: "Buffered while the trailer plays, 32–64 MiB. 0 uses 48 MiB.",
      },
    ],
  },
];
