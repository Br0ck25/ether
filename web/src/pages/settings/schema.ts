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
        key: "disableVersionTracking",
        label: "Keep the current compatibility version",
        kind: "bool",
        help: "Disable automatic public Plex release tracking.",
      },
      {
        key: "compatibilityVersion",
        label: "Compatibility version override",
        kind: "text",
        help: "Leave empty to use the tracked public PMS version.",
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
        help: "Optional. Leave empty to use the key bundled with Ether.",
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

export const LLM: FieldGroup[] = [
  {
    title: "Language model",
    description:
      "Powers “Describe it” in Collection Studio and themed Home rows. Any OpenAI-compatible endpoint works, including a local Ollama.",
    fields: [
      { key: "enabled", label: "Enabled", kind: "bool" },
      {
        key: "baseUrl",
        label: "Endpoint URL",
        kind: "text",
        wide: true,
        placeholder: "http://localhost:11434/v1",
      },
      { key: "model", label: "Model", kind: "text", placeholder: "llama3.1" },
      {
        key: "apiKey",
        label: "API key",
        kind: "secret",
        help: "Not needed for most local servers.",
      },
    ],
  },
];

export const UPDATES: FieldGroup[] = [
  {
    title: "Updates",
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
      { key: "autoUpdate", label: "Install updates automatically", kind: "bool" },
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
    title: "Local files",
    description:
      "Serve files from disk to verify a Plex client's playback. Meant for troubleshooting.",
    fields: [
      { key: "enabled", label: "Enabled", kind: "bool" },
      { key: "localFiles", label: "Files", kind: "list", placeholder: "/path/to/movie.mkv" },
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
      {
        key: "detectCached",
        label: "Detect markers from local media",
        kind: "bool",
        help: "Analyze complete local files in the background. Detection is bounded and does not download entire Usenet releases.",
      },
    ],
  },
];
