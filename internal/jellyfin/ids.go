package jellyfin

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/braedonsaunders/ether/internal/core"
)

// Jellyfin item ids are 32 hex digits and Emby's are opaque strings, so one
// encoding serves both: the rating key, zero-padded to 128 bits. Apps treat
// the id as an opaque token and send it back unchanged.
func itemID(k core.RatingKey) string { return fmt.Sprintf("%032x", uint64(k)) }

// parseItemID accepts the 32-digit form (with or without dashes) and the
// plain decimal rating key some Emby clients send.
func parseItemID(s string) (core.RatingKey, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	h := strings.ReplaceAll(s, "-", "")
	if len(h) == 32 {
		if strings.Trim(h[:16], "0") != "" {
			return 0, false
		}
		n, err := strconv.ParseUint(h[16:], 16, 63)
		if err != nil || n == 0 {
			return 0, false
		}
		return core.RatingKey(n), true
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return core.RatingKey(n), true
}

// Libraries are synthetic keys far above any real key kind.
const (
	kindView core.KeyKind = 99
	viewBase              = int64(kindView) * 1_000_000_000_000_000
)

const (
	viewMovies core.RatingKey = core.RatingKey(viewBase + 1)
	viewShows  core.RatingKey = core.RatingKey(viewBase + 2)
	viewBoxes  core.RatingKey = core.RatingKey(viewBase + 3)
)

func isView(k core.RatingKey) bool { return k.Kind() == kindView }

// params reads query parameters case-insensitively: Jellyfin accepts
// "includeItemTypes", "IncludeItemTypes" and everything between.
type params map[string][]string

func queryParams(r *http.Request) params {
	p := params{}
	for k, vs := range r.URL.Query() {
		p[strings.ToLower(k)] = append(p[strings.ToLower(k)], vs...)
	}
	return p
}

func (p params) get(name string) string {
	if vs := p[strings.ToLower(name)]; len(vs) > 0 {
		return vs[0]
	}
	return ""
}

func (p params) has(name string) bool { return len(p[strings.ToLower(name)]) > 0 }

func (p params) int(name string, def int) int {
	n, err := strconv.Atoi(p.get(name))
	if err != nil {
		return def
	}
	return n
}

func (p params) int64(name string) int64 {
	n, _ := strconv.ParseInt(p.get(name), 10, 64)
	return n
}

func (p params) bool(name string) (value, set bool) {
	switch strings.ToLower(p.get(name)) {
	case "true", "1":
		return true, true
	case "false", "0":
		return false, true
	}
	return false, false
}

// list splits a comma- or pipe-separated parameter, which may repeat.
func (p params) list(name string) []string {
	var out []string
	for _, v := range p[strings.ToLower(name)] {
		for _, part := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == '|' }) {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}

func (p params) set(name, value string) { p[strings.ToLower(name)] = []string{value} }

func withQuery(path string, kv ...string) string {
	v := url.Values{}
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] != "" {
			v.Set(kv[i], kv[i+1])
		}
	}
	if len(v) == 0 {
		return path
	}
	return path + "?" + v.Encode()
}

// ticks are Jellyfin's 100 ns time unit.
const ticksPerSecond = 10_000_000
