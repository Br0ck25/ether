package nzb

import (
	"path"
	"regexp"
	"strconv"
	"strings"
)

// Kind classifies a posted file by its name.
type Kind int

const (
	KindUnknown  Kind = iota
	KindVideo         // a playable container (.mkv, .mp4, ...)
	KindRAR           // a RAR volume (.rar, .r00, .partNN.rar)
	KindSevenZip      // a 7z volume (.7z, .7z.001)
	KindSplit         // a plain split volume (.001, .mkv.001)
	KindPar2
	KindNFO
	KindSFV
	KindSubtitle
	KindImage
	KindOther // known but irrelevant (.txt, .nzb, .srr, .iso, ...)
)

var kindNames = [...]string{"unknown", "video", "rar", "7z", "split", "par2", "nfo", "sfv", "subtitle", "image", "other"}

func (k Kind) String() string {
	if int(k) < len(kindNames) {
		return kindNames[k]
	}
	return "kind(" + strconv.Itoa(int(k)) + ")"
}

// IsArchive reports whether the kind is a volume of a multi-file set.
func (k Kind) IsArchive() bool { return k == KindRAR || k == KindSevenZip || k == KindSplit }

// FileInfo is what a file name says about a posted file.
type FileInfo struct {
	Kind Kind
	// Ext is the lower-cased extension of the content: "mkv" for
	// "movie.mkv" and for "movie.mkv.001"; "rar" / "7z" for archives.
	Ext string
	// Volume is the 0-based position of an archive volume in its set
	// (.rar=0, .r00=1, .part01.rar=0, .7z.001=0, .001=0). -1 otherwise.
	Volume int
	// SetName is the lower-cased name shared by every volume of a set, so
	// volumes group by (Kind, SetName). For other files it is the
	// lower-cased name without extension.
	SetName string
	// NewStyle is true for .partNN.rar naming, false for .rar/.rNN.
	NewStyle bool
	// Sample and Extra flag short clips that are not the feature.
	Sample bool
	Extra  bool
}

var videoExts = map[string]bool{
	"mkv": true, "mp4": true, "m4v": true, "avi": true, "ts": true, "m2ts": true,
	"mts": true, "wmv": true, "mov": true, "mpg": true, "mpeg": true, "webm": true,
	"vob": true, "ogm": true, "divx": true, "flv": true, "3gp": true, "asf": true,
}

var subtitleExts = map[string]bool{"srt": true, "sub": true, "idx": true, "ass": true, "ssa": true, "vtt": true, "sup": true}
var imageExts = map[string]bool{"jpg": true, "jpeg": true, "png": true, "gif": true, "bmp": true, "webp": true}
var otherExts = map[string]bool{
	"txt": true, "nzb": true, "srr": true, "iso": true, "img": true, "url": true, "exe": true,
	"zip": true, "md5": true, "sha1": true, "db": true, "log": true, "cue": true, "nfo2": true,
}

// IsVideoExt reports whether ext (without dot, any case) is a video container.
func IsVideoExt(ext string) bool { return videoExts[strings.ToLower(ext)] }

var (
	rePartRAR  = regexp.MustCompile(`(?i)^(.*)\.part(\d{1,4})\.rar$`)
	reOldRAR   = regexp.MustCompile(`(?i)^(.*)\.([r-y])(\d{2})$`)
	rePlainRAR = regexp.MustCompile(`(?i)^(.*)\.rar$`)
	re7zVol    = regexp.MustCompile(`(?i)^(.*)\.7z\.(\d{1,4})$`)
	re7z       = regexp.MustCompile(`(?i)^(.*)\.7z$`)
	reSplit    = regexp.MustCompile(`(?i)^(.*)\.(\d{3,4})$`)
	rePar2     = regexp.MustCompile(`(?i)^(.*?)(\.vol\d+[+-]\d+)?\.par2$`)
	reSample   = regexp.MustCompile(`(?i)(^|[^a-z0-9])(sample|proof)([^a-z0-9]|$)`)
	reExtra    = regexp.MustCompile(`(?i)(^|[^a-z0-9])(trailer|featurette|extras?|behind[ ._-]the[ ._-]scenes|deleted[ ._-]scenes?|bonus|interview|making[ ._-]of)([^a-z0-9]|$)`)
)

// Classify interprets a file name (a base name or a path inside an archive).
func Classify(name string) FileInfo {
	base := path.Base(strings.ReplaceAll(strings.TrimSpace(name), `\`, "/"))
	if base == "." || base == "/" {
		base = ""
	}
	info := FileInfo{Volume: -1}
	lower := strings.ToLower(base)
	// Sample/extra detection looks at the whole path: "Sample/x.mkv".
	full := strings.ToLower(name)
	info.Sample = reSample.MatchString(full)
	info.Extra = !info.Sample && reExtra.MatchString(full)

	switch {
	case rePar2.MatchString(lower):
		m := rePar2.FindStringSubmatch(lower)
		info.Kind, info.Ext, info.SetName = KindPar2, "par2", m[1]
		return info
	case rePartRAR.MatchString(lower):
		m := rePartRAR.FindStringSubmatch(lower)
		n, _ := strconv.Atoi(m[2])
		info.Kind, info.Ext, info.SetName, info.NewStyle = KindRAR, "rar", m[1], true
		info.Volume = n - 1 // part01 is the first; fixed up per set if numbering starts at 0
		return info
	case rePlainRAR.MatchString(lower):
		m := rePlainRAR.FindStringSubmatch(lower)
		info.Kind, info.Ext, info.SetName, info.Volume = KindRAR, "rar", m[1], 0
		return info
	case reOldRAR.MatchString(lower):
		m := reOldRAR.FindStringSubmatch(lower)
		n, _ := strconv.Atoi(m[3])
		letter := int(m[2][0] - 'r')
		info.Kind, info.Ext, info.SetName = KindRAR, "rar", m[1]
		info.Volume = 1 + letter*100 + n
		return info
	case re7zVol.MatchString(lower):
		m := re7zVol.FindStringSubmatch(lower)
		n, _ := strconv.Atoi(m[2])
		info.Kind, info.Ext, info.SetName, info.Volume = KindSevenZip, "7z", m[1], n-1
		return info
	case re7z.MatchString(lower):
		m := re7z.FindStringSubmatch(lower)
		info.Kind, info.Ext, info.SetName, info.Volume = KindSevenZip, "7z", m[1], 0
		return info
	case reSplit.MatchString(lower):
		m := reSplit.FindStringSubmatch(lower)
		n, _ := strconv.Atoi(m[2])
		info.Kind, info.SetName, info.Volume = KindSplit, m[1], n-1
		info.Ext = strings.TrimPrefix(path.Ext(m[1]), ".")
		if _, err := strconv.Atoi(info.Ext); err == nil {
			info.Ext = "" // "movie.2024.001": the year is not an extension
		}
		return info
	}

	ext := strings.TrimPrefix(path.Ext(lower), ".")
	info.Ext = ext
	info.SetName = strings.TrimSuffix(lower, path.Ext(lower))
	switch {
	case ext == "":
		info.Kind = KindUnknown
	case videoExts[ext]:
		info.Kind = KindVideo
	case ext == "nfo":
		info.Kind = KindNFO
	case ext == "sfv":
		info.Kind = KindSFV
	case subtitleExts[ext]:
		info.Kind = KindSubtitle
	case imageExts[ext]:
		info.Kind = KindImage
	case otherExts[ext]:
		info.Kind = KindOther
	default:
		info.Kind = KindUnknown
	}
	return info
}

var (
	reQuoted       = regexp.MustCompile(`"([^"]*)"`)
	reBracketName  = regexp.MustCompile(`\[\s*(?:\d+/)?([^\[\]"]+\.[A-Za-z0-9]{2,5}(?:\.\d{2,4})?)\s*\]`)
	reFileCounter  = regexp.MustCompile(`[\[(]\s*(\d{1,5})\s*(?:/|of)\s*(\d{1,5})\s*[\])]`)
	reSegCounter   = regexp.MustCompile(`\(\s*(\d{1,6})\s*/\s*(\d{1,6})\s*\)\s*(?:\d+\s*)?$`)
	reTrailingYEnc = regexp.MustCompile(`(?i)\s*(?:-\s*)?(?:yenc)?\s*(?:\(\s*\d+\s*/\s*\d+\s*\))?\s*(?:\d+\s*)?$`)
	reSizeNoise    = regexp.MustCompile(`(?i)\s+-?\s*\d+(?:[.,]\d+)?\s*[kmgt]i?b\b.*$`)
	reBareName     = regexp.MustCompile(`([^\s"\[\]()/\\]+\.(?:part\d+\.rar|r\d{2}|7z\.\d{3}|\d{3}|vol\d+[+-]\d+\.par2|[A-Za-z0-9]{2,4}))(?:\s|$)`)
)

// NameFromSubject extracts the file name from a Usenet subject line, e.g.
//
//	`"Movie.2024.part01.rar" yEnc (1/50)`                -> Movie.2024.part01.rar
//	`[01/42] - "Movie.2024.mkv" yEnc (1/1000)`          -> Movie.2024.mkv
//	`[PRiVATE]-[WtFnZb]-[Movie.2024.mkv]-[1/5] - "" yEnc` -> Movie.2024.mkv
//	`Movie.2024.r00 (1/50)`                             -> Movie.2024.r00
func NameFromSubject(subject string) string {
	s := strings.TrimSpace(subject)
	if s == "" {
		return ""
	}
	// 1. Quoted names: prefer one that looks like a file name.
	var firstQuoted string
	for _, m := range reQuoted.FindAllStringSubmatch(s, -1) {
		q := strings.TrimSpace(m[1])
		if q == "" {
			continue
		}
		if firstQuoted == "" {
			firstQuoted = q
		}
		if strings.Contains(q, ".") {
			return cleanName(q)
		}
	}
	// 2. Bracketed names, as used by WtFnZb-style posters.
	if m := reBracketName.FindAllStringSubmatch(s, -1); len(m) > 0 {
		return cleanName(m[len(m)-1][1])
	}
	if firstQuoted != "" {
		return cleanName(firstQuoted)
	}
	// 3. Unquoted: drop trailing yEnc/segment counter and size noise, then
	// look for a token with a known-looking extension.
	stripped := reTrailingYEnc.ReplaceAllString(s, "")
	stripped = reSizeNoise.ReplaceAllString(stripped, "")
	stripped = strings.TrimSpace(stripped)
	if ms := reBareName.FindAllStringSubmatch(stripped+" ", -1); len(ms) > 0 {
		return cleanName(ms[len(ms)-1][1])
	}
	// Fall back to the last whitespace-separated token.
	if f := strings.Fields(stripped); len(f) > 0 {
		return cleanName(f[len(f)-1])
	}
	return cleanName(s)
}

func cleanName(n string) string {
	n = strings.TrimSpace(n)
	n = strings.Trim(n, `"'`)
	n = strings.ReplaceAll(n, `\`, "/")
	if i := strings.LastIndex(n, "/"); i >= 0 {
		n = n[i+1:]
	}
	return strings.TrimSpace(n)
}

// subjectFileCounter finds the "[03/50]" file counter: the first counter
// that is not the trailing yEnc segment counter.
func subjectFileCounter(subject string) (int, int) {
	segLoc := reSegCounter.FindStringIndex(subject)
	for _, loc := range reFileCounter.FindAllStringSubmatchIndex(subject, -1) {
		if segLoc != nil && loc[0] >= segLoc[0] {
			break
		}
		i, _ := strconv.Atoi(subject[loc[2]:loc[3]])
		n, _ := strconv.Atoi(subject[loc[4]:loc[5]])
		if n > 0 && i <= n {
			return i, n
		}
	}
	return 0, 0
}

func subjectSegmentCount(subject string) int {
	m := reSegCounter.FindStringSubmatch(subject)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[2])
	return n
}

var (
	reHex      = regexp.MustCompile(`^[0-9a-fA-F]{24,}$`)
	reAlnumRnd = regexp.MustCompile(`^[0-9A-Za-z]{16,}$`)
	reUUIDish  = regexp.MustCompile(`^[0-9a-fA-F]{8}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{12}$`)
)

// IsObfuscated reports whether a name carries no information about the
// content: no extension, a hash/UUID-like base, a long random token, or the
// "abc.xyz" placeholder some posters use.
func IsObfuscated(name string) bool {
	n := strings.TrimSpace(name)
	if n == "" {
		return true
	}
	info := Classify(n)
	if info.Kind == KindUnknown && info.Ext == "" {
		return true
	}
	baseName := path.Base(strings.ReplaceAll(n, `\`, "/"))
	lower := strings.ToLower(baseName)
	if strings.HasPrefix(lower, "abc.xyz") {
		return true
	}
	stem := info.SetName
	if info.Kind == KindSplit && info.Ext != "" {
		stem = strings.TrimSuffix(stem, "."+info.Ext)
	}
	// SetName is a lower-cased prefix of the base name; recover the case.
	base := stem
	if len(lower) == len(baseName) && len(stem) <= len(baseName) {
		base = baseName[:len(stem)]
	}
	if base == "" {
		return true
	}
	if reHex.MatchString(base) || reUUIDish.MatchString(base) {
		return true
	}
	if reAlnumRnd.MatchString(base) && looksRandom(base) {
		return true
	}
	return false
}

// looksRandom: a separator-free token mixing digits with both letter cases,
// or digits with letters in long runs, typical of generated names.
func looksRandom(s string) bool {
	var upper, lower, digit, transitions int
	prev := 0
	for _, r := range s {
		cls := 0
		switch {
		case r >= 'A' && r <= 'Z':
			upper++
			cls = 1
		case r >= 'a' && r <= 'z':
			lower++
			cls = 2
		case r >= '0' && r <= '9':
			digit++
			cls = 3
		}
		if prev != 0 && cls != prev {
			transitions++
		}
		prev = cls
	}
	kinds := 0
	for _, c := range []int{upper, lower, digit} {
		if c > 0 {
			kinds++
		}
	}
	if kinds >= 3 {
		return true
	}
	// Two classes but frequently alternating, e.g. "a8f7e6d5c4b3a2f1e0d9".
	return kinds == 2 && digit > 0 && transitions*3 >= len(s)
}
