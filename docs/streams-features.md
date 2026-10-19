# Audio, subtitle and marker implementation

The implementation provides persistent viewer stream rules and show memory,
container chapters, release-duration marker lookup/cache, cached-file acoustic
intro matching, conservative final-tail detection, subtitle normalization and
sync, and audio filters. Offline API tests establish these behaviors; they do
not establish that an official Plex app displays a skip button. The integrated
Plex Web spike belongs in the official-app validation record. No live account
settings or server markers were modified by this work.

## Viewer policy

`internal/trackprefs` registers its own SQLite migrations. User `0` stores
owner defaults; a viewer record overrides that whole rule set. Audio language
order, original-language preference, subtitle language order, off/forced-only/
foreign-only/always mode, normal/SDH/forced style, loudness, night mode and
centre-channel boost are persisted. Metadata `selected` flags and playback
decisions apply the same selection. Explicit per-item Plex selections win.
Episode choices are remembered by viewer and show, using language and track
flags rather than unstable stream indices. `PUT /library/parts/{part}` and
available `audioStreamID`/`subtitleStreamID` timeline fields record memory;
subtitle ID zero remembers off. Show memory does not cross viewers or shows.

Mount `trackprefs.Service.Handler()` at `/ether/api/stream-preferences` behind
owner authentication/CSRF protection, stripping the prefix. Routes:

- `GET|PUT|DELETE /{viewerID}` reads, sets, or resets an override.
- `GET /{viewerID}/shows` lists remembered choices.
- `DELETE /{viewerID}/shows/{TMDBShowID}` forgets that show's choices.

`web/src/pages/StreamPreferences.tsx` supplies these controls, coverage counts,
and a subtitle sync tool. Errors from unavailable endpoints are displayed;
there is no mock success response.

## Chapters and markers

The ffprobe parser supplies `MediaInfo.Chapters`. Plex metadata and decisions
serialize `Chapter` with millisecond offsets and `Marker` for intro/credits.
Estimated pre-resolution durations do not expose skip ranges. Clearly named
Intro/Opening/Credits/Recap chapters produce ranges. Recaps are cached and
counted but are not mislabeled as Plex intro markers.

The remote integration is **TheIntroDB**, not the separate `introdb.app`.
The current [official client documentation](https://theintrodb.github.io/theintrodb-npm/getting-started.html)
and [transport source](https://github.com/TheIntroDB/theintrodb-npm/blob/main/src/funcs.ts)
identify the v4 API `https://api.theintrodb.org/v4/media`, `tmdb_id`, episode
coordinates, and `duration_ms`. Reads are public; the optional Bearer key
belongs to the current end user and can include their pending submissions.
Ether never ships a shared key and never submits timestamps. Null starts are
zero; null credit ends mean media end. Out-of-range data is rejected. Unknown
release-duration submissions are excluded (`merge_unknown=false`). Results,
including no-result responses, are cached by item and exact runtime for seven
days. Chapters take precedence for each marker type. The primary docs reviewed
do not supply a separate data-redistribution license; this implementation
performs individual read-through requests and local caching, not bulk export.
The operator can disable remote lookup and provide their own key in settings.

`markers.Options.Config` reads the current enabled/key settings without a
mutable global. `markers.Service.Handler()` at `/ether/api/markers` supports
`GET /shows/{TMDBShowID}`. Counts cover observed cache entries, not every
catalogue episode or proven client skip-button support.

`ScheduleCached(ctx,key,info,localPath)` starts one lazy background worker with
a queue of eight files, deduplicates in-flight work and remembers successful
analysis for the cache TTL. `Close()` cancels active subprocesses and joins the
worker. The detector accepts regular local cached media only, limits input
protocols to file/pipe, uses one ffmpeg thread, a three-minute timeout and
bounded output. It does not download full Usenet releases to analyze them.

The acoustic path decodes at most the first ten minutes into 8 kHz mono PCM.
Pure Go Goertzel band energies form 250 ms fingerprints; persisted fingerprints
from up to twelve other episodes in the same season identify long contiguous
matches, rejecting silence and low-information runs. It is not the Chromaprint
format and needs no `libchromaprint` feature in the bundled ffmpeg. Matching
can miss edited/noisy intros; it is a conservative first implementation, not a
trained intro detector. Black-frame and silence overlap can mark a sustained
black/silent final tail. This does not recognize ordinary rolling credits;
chapter/remote timing remains the principal credits source. Detection tests
use synthetic fingerprints and fake subprocess output, not a claim of accuracy
across a real television corpus.

## Subtitles

`MovieHash` implements the published [OpenSubtitles algorithm](https://github.com/opensubtitles/oshash)
from its description, reading only the first/last 64 KiB. `SearchWithHash` and
`SidecarsWithHash` prioritize `moviehash_match` results and keep hash-specific
selection caches. Playback's `AugmentMedia` hook runs `subtitles.Service.Enrich`
while it owns the source, hashing adopted local files safely before publication.
Remote sources retain release-name matching: their interface has no cache-only
edge read, so reading those ranges could fetch Usenet data during startup.
Cached-only remote hashing requires an engine/cache hook and is not claimed.

Normalization handles UTF-8/UTF-16 and language-dependent legacy encodings,
SRT/WebVTT, and ASS event text. ASS styling, positioning and karaoke effects
are dropped only for text fallback. `AlignAgainst` estimates a constant offset
from at least three unique matching embedded-reference cues and rejects
inconsistent offsets. Manual offset/rate correction is also supported. It does
not run an automatic VAD model or infer drift/cut differences from unrelated
subtitle languages.

Mount `SyncHandler()` at `/ether/api/subtitle-sync`. `POST /{cachedID}` accepts
`offsetMs`, `rate`, optional reference subtitle text and `apply`. A new cached
SRT/VTT pair is created. With `apply=true`, future reads of that original
cached sidecar use the adjusted copy for all viewers; the original bytes are
preserved. Plex's optional `Deps.Subtitles` resolves these `ether-subtitle:`
streams through `OpenFormat`, never as a filesystem path.

PGS OCR is not bundled. FFmpeg's [OCR filter](https://ffmpeg.org/ffmpeg-filters.html#ocr)
requires libtesseract and trained data; it is a video filter, not a direct PGS
text decoder. There is no verified pure-Go OCR implementation or corresponding
bundled model here. PGS stays available unchanged to clients that support it;
unsupported bitmap tracks follow the transcoder's image-subtitle burn path.

## Audio processing and client capability reporting

Viewer rules produce only fixed, known ffmpeg filters: `loudnorm` for streamed
normalization, `acompressor` for night mode and a centre-weighted stereo mix
for six-or-more-channel dialogue boost. These force audio transcoding and never
attach `-af` to audio stream copy. The dialogue mix is not Plex's proprietary
frequency-isolation implementation.

Plex's [official audio enhancement documentation](https://support.plex.tv/articles/audio-track-enhancements-for-video/)
says its native normalization option requires analyzed tracks and its dialog/
normalization controls require the viewer's actual Plex Pass entitlement.
Ether does not spoof analyzed loudness (`canNormalizeLoudness` remains false)
or subscription features. The reviewed public PMS API documentation did not
establish current request parameter names for those native controls. Server
preferences therefore enable these filters; native per-playback control
interoperability remains unverified. Plex also [documents skip-credit Pass requirements](https://support.plex.tv/articles/credits-detection/).
