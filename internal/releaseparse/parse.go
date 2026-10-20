// Package releaseparse parses scene and P2P release names into
// core.ReleaseInfo. It is pure string work and safe for concurrent use.
package releaseparse

import (
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/braedonsaunders/ether/internal/core"
)

var (
	reYear       = regexp.MustCompile(`\b(19\d{2}|20\d{2})\b`)
	reSE         = regexp.MustCompile(`(?i)\bS(\d{1,2})E(\d{1,3})((?:E\d{1,3})*)(?:-?E?(\d{1,3}))?\b`)
	reSeasonOnly = regexp.MustCompile(`(?i)\bS(\d{1,2})(?:\s*-\s*S(\d{1,2}))?\b`)
	reSeasonWord = regexp.MustCompile(`(?i)\bSeason\s*(\d{1,2})\b`)
	reXEpisode   = regexp.MustCompile(`\b(\d{1,2})x(\d{1,3})\b`)
	reEpRange    = regexp.MustCompile(`(?i)E(\d{1,3})`)
	reChannels   = regexp.MustCompile(`([257])[\s.]([01])\b`)
	reGroupTail  = regexp.MustCompile(`-([A-Za-z0-9][A-Za-z0-9_]{1,})$`)
	reBracket    = regexp.MustCompile(`\[([^\]]+)\]`)
)

var containers = map[string]bool{
	"mkv": true, "mp4": true, "avi": true, "m4v": true, "mov": true,
	"wmv": true, "ts": true, "m2ts": true, "webm": true, "mpg": true, "mpeg": true,
}

var languages = []struct{ token, name string }{
	{"MULTI", "multi"}, {"FRENCH", "french"}, {"VOSTFR", "french"}, {"TRUEFRENCH", "french"},
	{"GERMAN", "german"}, {"SPANISH", "spanish"}, {"LATINO", "spanish"}, {"ITALIAN", "italian"},
	{"DUTCH", "dutch"}, {"PORTUGUESE", "portuguese"}, {"RUSSIAN", "russian"}, {"POLISH", "polish"},
	{"KOREAN", "korean"}, {"JAPANESE", "japanese"}, {"CHINESE", "chinese"}, {"MANDARIN", "chinese"},
	{"HINDI", "hindi"}, {"TELUGU", "telugu"}, {"TAMIL", "tamil"}, {"TURKISH", "turkish"},
	{"ARABIC", "arabic"}, {"SWEDISH", "swedish"}, {"NORWEGIAN", "norwegian"}, {"DANISH", "danish"},
	{"FINNISH", "finnish"}, {"CZECH", "czech"}, {"HUNGARIAN", "hungarian"}, {"GREEK", "greek"},
	{"THAI", "thai"}, {"VIETNAMESE", "vietnamese"}, {"INDONESIAN", "indonesian"},
	{"ENGLISH", "english"}, {"ENG", "english"},
	// Explicit regional language tags are used by WEB release groups.
	// Do not treat bare country codes (BR/US/GB) as audio languages.
	{"PT-BR", "portuguese"}, {"PT-PT", "portuguese"},
	{"ES-ES", "spanish"}, {"ES-MX", "spanish"}, {"ES-419", "spanish"},
	{"EN-US", "english"}, {"EN-GB", "english"},
}

// Parse extracts structured info from a release name.
func Parse(name string) core.ReleaseInfo {
	var info core.ReleaseInfo
	raw := strings.TrimSpace(name)
	if raw == "" {
		return info
	}
	// Leading / trailing bracket group, e.g. [YTS.MX] or [HorribleSubs].
	bracketGroup := ""
	if ms := reBracket.FindAllStringSubmatch(raw, -1); len(ms) > 0 {
		for _, m := range ms {
			v := strings.TrimSpace(m[1])
			if v != "" && len(v) <= 20 && !looksLikeMeta(v) {
				bracketGroup = v
			}
		}
	}
	// Container extension.
	stem := raw
	if i := strings.LastIndex(raw, "."); i > 0 {
		ext := strings.ToLower(raw[i+1:])
		// Only treat as container if the suffix is a bare extension.
		if containers[ext] {
			info.Container = ext
			stem = raw[:i]
		}
	}
	// Group: trailing -GROUP (after stripping bracket chunks).
	noBrackets := reBracket.ReplaceAllString(stem, " ")
	if bracketGroup != "" {
		info.Group = bracketGroup
	} else if m := reGroupTail.FindStringSubmatch(strings.TrimSpace(noBrackets)); m != nil {
		g := m[1]
		if !isKnownTail(g) {
			info.Group = g
		}
	}

	spaced := spacify(stem)
	upper := " " + strings.ToUpper(spaced) + " "

	// Season / episode.
	parseEpisodes(spaced, &info)
	var yearAt int
	info.Year, yearAt = parseYearAt(spaced)
	// "COMPLETE" before the release year is usually part of the title
	// ("A.Complete.Unknown.2024"), unless it names a pack.
	if idx := tokenIndex(spaced, "COMPLETE"); idx >= 0 && (idx > yearAt || len(info.Seasons) > 0 || namesPack(spaced[idx+len("COMPLETE"):])) {
		info.FullSeason = true
	}
	info.Height = parseHeight(upper)
	info.Source = parseSource(upper)
	info.VideoCodec = parseVideoCodec(upper)
	info.AudioCodec, info.Atmos = parseAudio(upper)
	if m := reChannels.FindStringSubmatch(strings.ToUpper(spaced)); m != nil {
		info.Channels = m[1] + "." + m[2]
	}
	// DDP5.1 style, where dot was a separator: DD 5 1 handled above; also "5.1" in raw.
	if info.Channels == "" {
		if m := regexp.MustCompile(`\b(5\.1|7\.1|2\.0|2\.1|6\.1)\b`).FindStringSubmatch(raw); m != nil {
			info.Channels = m[1]
		}
	}
	if strings.Contains(upper, "HDR10+") || strings.Contains(upper, "HDR10PLUS") || strings.Contains(upper, " HDR10 ") || strings.Contains(upper, " HDR ") || strings.Contains(upper, "HDR10") {
		info.HDR10 = true
	}
	if strings.Contains(upper, "DOLBY VISION") || strings.Contains(upper, " DOLBYVISION ") || strings.Contains(upper, " DOVI ") || strings.Contains(upper, " DV ") || strings.Contains(upper, " DV-") || strings.HasSuffix(strings.TrimSpace(upper), " DV") {
		info.DolbyVision = true
	}
	info.Edition = ParseEdition(name)
	info.Proper = strings.Contains(upper, " PROPER ")
	info.Repack = strings.Contains(upper, " REPACK ")
	// Words of the title itself ("The.French.Dispatch.2021", "The Danish
	// Girl") are not audio languages: only tags after the release year or
	// the episode marker count when the name has one.
	floor := yearAt
	if loc := reSE.FindStringIndex(spaced); loc != nil && (floor <= 0 || loc[0] < floor) {
		floor = loc[0]
	}
	seen := map[string]bool{}
	for _, l := range languages {
		if languageTagAfter(spaced, l.token, floor) {
			if !seen[l.name] {
				seen[l.name] = true
				info.Languages = append(info.Languages, l.name)
			}
		}
	}
	info.Title = extractTitle(spaced, yearAt, &info)
	return info
}

func looksLikeMeta(v string) bool {
	u := strings.ToUpper(v)
	for _, k := range []string{"1080", "720", "2160", "480", "BLURAY", "WEB", "HDTV", "X264", "X265", "HEVC", "HDR"} {
		if strings.Contains(u, k) {
			return true
		}
	}
	return false
}

func isKnownTail(g string) bool {
	switch strings.ToUpper(g) {
	case "DL", "RIP", "CAM", "TS", "TC", "MA", "HD", "SD", "VISION", "DV", "HDR", "ATMOS":
		return true
	}
	return false
}

// spacify turns separators into spaces, keeping hyphens inside tokens.
func spacify(s string) string {
	r := strings.NewReplacer(".", " ", "_", " ", "(", " ", ")", " ", "[", " ", "]", " ")
	s = r.Replace(s)
	return strings.Join(strings.Fields(s), " ")
}

func parseEpisodes(spaced string, info *core.ReleaseInfo) {
	if m := reSE.FindStringSubmatch(spaced); m != nil {
		s, _ := strconv.Atoi(m[1])
		info.Seasons = []int{s}
		first, _ := strconv.Atoi(m[2])
		eps := []int{first}
		// Additional E## tokens chained (S01E02E03).
		for _, em := range reEpRange.FindAllStringSubmatch(m[3], -1) {
			n, _ := strconv.Atoi(em[1])
			eps = append(eps, n)
		}
		// Range tail: S01E02-E04 or S01E02-04 (captured last group beyond chained).
		if m[4] != "" && len(eps) == 1 {
			last, _ := strconv.Atoi(m[4])
			// Only expand when it really looks like a range end and is
			// a small step forward.
			if last > first && last-first <= 30 && rangeTailPresent(spaced, m[0]) {
				for n := first + 1; n <= last; n++ {
					eps = append(eps, n)
				}
			} else if last != first {
				eps = append(eps, last)
			}
		}
		sort.Ints(eps)
		info.Episodes = dedupeInts(eps)
		return
	}
	if m := reSeasonWord.FindStringSubmatch(spaced); m != nil {
		s, _ := strconv.Atoi(m[1])
		info.Seasons = []int{s}
		info.FullSeason = true
		return
	}
	if m := reSeasonOnly.FindStringSubmatch(spaced); m != nil {
		// Guard against matching the S in e.g. S01E02 (already handled) —
		// here no E followed, so it is a season pack.
		s, _ := strconv.Atoi(m[1])
		info.Seasons = []int{s}
		if m[2] != "" {
			end, _ := strconv.Atoi(m[2])
			for n := s + 1; n <= end && n-s <= 20; n++ {
				info.Seasons = append(info.Seasons, n)
			}
		}
		info.FullSeason = true
		return
	}
	if m := reXEpisode.FindStringSubmatch(spaced); m != nil {
		s, _ := strconv.Atoi(m[1])
		e, _ := strconv.Atoi(m[2])
		// 1x02 is ambiguous with e.g. codec-ish tokens; season<=50 guard.
		if s >= 1 && s <= 50 {
			info.Seasons = []int{s}
			info.Episodes = []int{e}
		}
	}
}

func rangeTailPresent(spaced, match string) bool {
	return strings.Contains(strings.ToUpper(match), "-")
}

func dedupeInts(in []int) []int {
	out := in[:0:0]
	for i, v := range in {
		if i == 0 || v != in[i-1] {
			out = append(out, v)
		}
	}
	return out
}

var reResolution = regexp.MustCompile(`(?i)\b\d{3,4}[pi]\b`)

func parseYear(spaced string, info *core.ReleaseInfo) int {
	y, _ := parseYearAt(spaced)
	return y
}

// parseYearAt returns the release year and its offset in spaced, or (0, -1).
func parseYearAt(spaced string) (int, int) {
	locs := reYear.FindAllStringIndex(spaced, -1)
	if len(locs) == 0 {
		return 0, -1
	}
	// A year-like title ("1917", "Blade Runner 2049", "Wonder Woman 1984")
	// is followed by the release year: the last year before the episode
	// or resolution markers is the release year.
	limit := len(spaced)
	if loc := reSE.FindStringIndex(spaced); loc != nil {
		limit = loc[0]
	}
	if loc := reResolution.FindStringIndex(spaced); loc != nil && loc[0] < limit {
		limit = loc[0]
	}
	pick := locs[0]
	for _, l := range locs[1:] {
		// The end of a span ("1994-2004") is not a release year.
		rangeEnd := l[0] >= 2 && spaced[l[0]-1] == '-' && spaced[l[0]-2] >= '0' && spaced[l[0]-2] <= '9'
		if l[0] < limit && !rangeEnd {
			pick = l
		}
	}
	y, _ := strconv.Atoi(spaced[pick[0]:pick[1]])
	return y, pick[0]
}

// namesPack reports whether the words after "COMPLETE" describe a pack.
func namesPack(rest string) bool {
	next, _, _ := strings.Cut(strings.TrimLeft(rest, " -"), " ")
	switch strings.ToUpper(next) {
	case "SERIES", "SEASON", "SEASONS", "PACK", "COLLECTION", "BOXSET":
		return true
	}
	return false
}

// languageTagAfter reports whether the ASCII tag occurs in spaced after
// offset floor, preceded by the start or a space and followed by the end, a
// space or a hyphen.
func languageTagAfter(spaced, tag string, floor int) bool {
	for i := max(floor+1, 0); i+len(tag) <= len(spaced); i++ {
		if i > 0 && spaced[i-1] != ' ' {
			continue
		}
		end := i + len(tag)
		if end < len(spaced) && spaced[end] != ' ' && spaced[end] != '-' {
			continue
		}
		if strings.EqualFold(spaced[i:end], tag) {
			return true
		}
	}
	return false
}

// tokenIndex returns the offset in s of the first case-insensitive match of
// the ASCII keyword kw that is a whole token (bounded by the string ends,
// spaces or hyphens), or -1.
func tokenIndex(s, kw string) int {
	for i := 0; i+len(kw) <= len(s); i++ {
		if i > 0 && s[i-1] != ' ' && s[i-1] != '-' {
			continue
		}
		end := i + len(kw)
		if end < len(s) && s[end] != ' ' && s[end] != '-' {
			continue
		}
		if strings.EqualFold(s[i:end], kw) {
			return i
		}
	}
	return -1
}

func parseHeight(upper string) int {
	switch {
	case strings.Contains(upper, "2160P") || strings.Contains(upper, "2160I") || strings.Contains(upper, " 4K ") || strings.Contains(upper, " 4K-") || strings.Contains(upper, " UHD ") || strings.HasSuffix(strings.TrimSpace(upper), "UHD") || strings.Contains(upper, "UHD "):
		return 2160
	case strings.Contains(upper, "1080P") || strings.Contains(upper, "1080I"):
		return 1080
	case strings.Contains(upper, "720P") || strings.Contains(upper, "720I"):
		return 720
	case strings.Contains(upper, "576P") || strings.Contains(upper, "576I"):
		return 576
	case strings.Contains(upper, "480P") || strings.Contains(upper, "480I"):
		return 480
	case strings.Contains(upper, "360P"):
		return 360
	}
	return 0
}

func parseSource(upper string) core.Source {
	switch {
	case strings.Contains(upper, " REMUX"):
		return core.SourceRemux
	case containsAny(upper, "BLURAY", "BLU-RAY", "BDRIP", "BRRIP", "BD-RIP"):
		return core.SourceBluRay
	case containsAny(upper, "WEBRIP", "WEB-RIP", " WEB RIP "):
		return core.SourceWEBRip
	case containsAny(upper, "WEB-DL", "WEBDL", " WEB DL", " WEB "):
		return core.SourceWEBDL
	case strings.Contains(upper, " HDTV") || strings.Contains(upper, "HDTV "):
		return core.SourceHDTV
	case containsAny(upper, "DVDSCR", " SCR ", "SCREENER"):
		return core.SourceScreener
	case containsAny(upper, "DVDRIP", "DVD-RIP", " DVD "):
		return core.SourceDVD
	case containsAny(upper, "HDCAM", " HD CAM", " CAM ", " CAM-", "HDTS", "HD-TS", " TELESYNC", " TELECINE", " TS ", " TC ", "-TS ", "-TC "):
		return core.SourceCam
	case strings.HasSuffix(strings.TrimSpace(upper), "CAM"):
		return core.SourceCam
	}
	return core.SourceUnknown
}

func parseVideoCodec(upper string) string {
	switch {
	case containsAny(upper, "X265", "H265", "H 265", "HEVC"):
		return "hevc"
	case containsAny(upper, " AV1", "-AV1", " AV1 "):
		return "av1"
	case containsAny(upper, " VP9"):
		return "vp9"
	case containsAny(upper, "X264", "H264", "H 264", " AVC"):
		return "h264"
	case containsAny(upper, "XVID", "DIVX"):
		return "xvid"
	case containsAny(upper, "MPEG2", "MPEG-2"):
		return "mpeg2"
	}
	return ""
}

func parseAudio(upper string) (string, bool) {
	atmos := strings.Contains(upper, "ATMOS")
	// Fix spaced variants: DTS-HD MA etc are contiguous enough.
	switch {
	case containsAny(upper, "TRUEHD", "TRUE-HD", "TRUE HD"):
		return "truehd", atmos
	case containsAny(upper, "DTS-HD", "DTSHD", "DTS HD", "DTS-HDMA", "DTSX", "DTS-X"):
		return "dtshd", atmos
	case containsAny(upper, "EAC3", "E-AC3", "E AC3", "DDP", "DD+", " DD PLUS"):
		return "eac3", atmos
	case strings.Contains(upper, " DTS") || strings.Contains(upper, "-DTS"):
		return "dts", atmos
	case containsAny(upper, " FLAC"):
		return "flac", atmos
	case containsAny(upper, " OPUS"):
		return "opus", atmos
	case containsAny(upper, "AC3", " DD5", " DD ", " DD-", "DOLBY DIGITAL"):
		return "ac3", atmos
	case containsAny(upper, " AAC"):
		return "aac", atmos
	case containsAny(upper, " MP3"):
		return "mp3", atmos
	}
	return "", atmos
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// boundaryKeywords mark the end of the title portion.
var boundaryKeywords = []string{
	"2160P", "1080P", "1080I", "720P", "576P", "480P", "360P", "4K", "UHD",
	"BLURAY", "BLU-RAY", "BDRIP", "BRRIP", "WEB-DL", "WEBDL", "WEBRIP", "WEB", "HDTV",
	"DVDRIP", "DVDSCR", "REMUX", "X264", "X265", "H264", "H265", "HEVC", "AVC", "AV1",
	"CAM", "HDCAM", "HDTS", "TELESYNC", "TELECINE", "PROPER", "REPACK", "COMPLETE",
	"SEASON", "MULTI", "FRENCH", "GERMAN", "SPANISH", "ITALIAN", "HDR", "HDR10",
	"INTERNAL", "LIMITED", "SUBBED", "DUBBED",
}

func extractTitle(spaced string, yearAt int, info *core.ReleaseInfo) string {
	cut := len(spaced)
	consider := func(idx int) {
		if idx > 0 && idx < cut {
			cut = idx
		}
	}
	// Year boundary (the occurrence parseYearAt chose).
	consider(yearAt)
	if loc := reSE.FindStringIndex(spaced); loc != nil {
		consider(loc[0])
	}
	if loc := reSeasonWord.FindStringIndex(spaced); loc != nil {
		consider(loc[0])
	}
	if len(info.Seasons) > 0 && len(info.Episodes) == 0 {
		if loc := reSeasonOnly.FindStringIndex(spaced); loc != nil {
			consider(loc[0])
		}
	}
	if loc := reXEpisode.FindStringIndex(spaced); loc != nil && len(info.Episodes) > 0 {
		consider(loc[0])
	}
	for _, kw := range boundaryKeywords {
		// Keywords are whole tokens ("Multiverse" is not MULTI, "Scam" is
		// not CAM), and words before the release year belong to the title
		// ("Charlottes Web 2006", "Internal Affairs 1990").
		if idx := tokenIndex(spaced, kw); idx > 0 && (idx > yearAt || kw == "COMPLETE" && namesPack(spaced[idx+len(kw):])) {
			consider(idx)
		}
	}
	title := spaced[:cut]
	title = strings.Trim(title, " -")
	// Hyphens between words in titles are usually separators too, except
	// keep words joined: turn "Spider-Man" stays, "Title -" trimmed above.
	fields := strings.Fields(title)
	// Drop a leading bracket-group token, e.g. [HorribleSubs] Title ...
	if info.Group != "" && len(fields) > 1 {
		gf := strings.Fields(spacify(info.Group))
		if len(gf) > 0 && len(fields) > len(gf) && strings.EqualFold(strings.Join(fields[:len(gf)], " "), strings.Join(gf, " ")) {
			fields = fields[len(gf):]
		}
	}
	// Drop a stray leading "-" separator left by "[Group] Title - S01E01".
	for len(fields) > 0 && fields[0] == "-" {
		fields = fields[1:]
	}
	title = strings.Trim(strings.Join(fields, " "), " -")
	return title
}

// SeriesYearMatches distinguishes same-named series and reboots. A year
// explicitly present in a TV release identifies the series, not the episode's
// air year. Releases omitting a year remain eligible.
func SeriesYearMatches(releaseYear, showYear int) bool {
	return releaseYear == 0 || showYear == 0 || releaseYear == showYear
}
