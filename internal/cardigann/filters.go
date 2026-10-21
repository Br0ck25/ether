package cardigann

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// filterEnv is what filters need besides the value.
type filterEnv struct {
	vars vars
	now  time.Time
	log  *slog.Logger
}

func arg(f Filter, i int) string {
	if i < len(f.Args) {
		return f.Args[i]
	}
	return ""
}

// applyFilters runs a filter chain. Unknown filters are skipped (they were
// reported when the definition was installed).
func applyFilters(value string, filters []Filter, env filterEnv) (string, error) {
	for _, f := range filters {
		var err error
		value, err = applyFilter(value, f, env)
		if err != nil {
			return value, fmt.Errorf("filter %s: %w", f.Name, err)
		}
	}
	return value, nil
}

func applyFilter(v string, f Filter, env filterEnv) (string, error) {
	switch f.Name {
	case "querystring":
		u, err := url.Parse(strings.TrimSpace(v))
		if err != nil {
			// Not a URL: treat the whole value as a query string.
			q, _ := url.ParseQuery(strings.TrimPrefix(v, "?"))
			return q.Get(arg(f, 0)), nil
		}
		return u.Query().Get(arg(f, 0)), nil
	case "regexp":
		re, err := compileRegex(arg(f, 0))
		if err != nil {
			return v, err
		}
		m, err := re.FindStringMatch(v)
		if err != nil || m == nil {
			return "", err
		}
		if groups := m.Groups(); len(groups) > 1 {
			return groups[1].String(), nil
		}
		return m.String(), nil
	case "re_replace":
		re, err := compileRegex(arg(f, 0))
		if err != nil {
			return v, err
		}
		repl := arg(f, 1)
		if strings.Contains(repl, "{{") {
			repl = mustRender(repl, env.vars)
		}
		return re.Replace(v, repl, -1, -1)
	case "split":
		sep := arg(f, 0)
		parts := strings.Split(v, sep)
		if sep == "" {
			parts = []string{v}
		}
		pos, err := strconv.Atoi(strings.TrimSpace(arg(f, 1)))
		if err != nil {
			return v, fmt.Errorf("index %q is not a number", arg(f, 1))
		}
		if pos < 0 {
			pos += len(parts)
		}
		if pos < 0 || pos >= len(parts) {
			return "", nil
		}
		return parts[pos], nil
	case "replace":
		return strings.ReplaceAll(v, arg(f, 0), arg(f, 1)), nil
	case "trim":
		if cut := arg(f, 0); cut != "" {
			return strings.Trim(v, cut), nil
		}
		return strings.TrimSpace(v), nil
	case "prepend":
		return mustRender(arg(f, 0), env.vars) + v, nil
	case "append":
		return v + mustRender(arg(f, 0), env.vars), nil
	case "tolower":
		return strings.ToLower(v), nil
	case "toupper":
		return strings.ToUpper(v), nil
	case "urldecode":
		if d, err := url.QueryUnescape(v); err == nil {
			return d, nil
		}
		return v, nil
	case "urlencode":
		return url.QueryEscape(v), nil
	case "htmldecode":
		return html.UnescapeString(v), nil
	case "htmlencode":
		return html.EscapeString(v), nil
	case "dateparse", "timeparse":
		layout := arg(f, 0)
		if layout == "" {
			t, err := fuzzyTime(v, env.now)
			if err != nil {
				return v, fmt.Errorf("%q: %w", v, err)
			}
			return formatDate(t), nil
		}
		t, err := parseLayout(v, layout)
		if err != nil {
			return v, fmt.Errorf("%q does not match layout %q", v, layout)
		}
		return formatDate(t), nil
	case "timeago", "reltime":
		t, ok := timeAgo(v, env.now)
		if !ok {
			return v, fmt.Errorf("%q is not a relative time", v)
		}
		return formatDate(t), nil
	case "fuzzytime":
		t, err := fuzzyTime(v, env.now)
		if err != nil {
			return v, fmt.Errorf("%q: %w", v, err)
		}
		return formatDate(t), nil
	case "diacritics":
		out, _, err := transform.String(transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC), v)
		if err != nil {
			return v, nil
		}
		return out, nil
	case "jsonjoinarray":
		return jsonJoinArray(v, arg(f, 0), arg(f, 1))
	case "validfilename":
		return strings.Map(func(r rune) rune {
			if strings.ContainsRune(`<>:"/\|?*`, r) || r < 32 {
				return -1
			}
			return r
		}, v), nil
	case "validate":
		return validateWords(v, strings.Join(f.Args, ",")), nil
	case "strdump":
		if env.log != nil {
			env.log.Debug("cardigann strdump", "tag", arg(f, 0), "value", abbreviate(v))
		}
		return v, nil
	case "hexdump":
		if env.log != nil {
			env.log.Debug("cardigann hexdump", "value", hex.EncodeToString([]byte(abbreviate(v))))
		}
		return v, nil
	case "andmatch":
		// A row filter; as a field filter it is a no-op.
		return v, nil
	}
	return v, nil
}

const validateDelims = ", /)(.;[]\"|:"

func validateWords(v, allowed string) string {
	split := func(s string) []string {
		return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return strings.ContainsRune(validateDelims, r) })
	}
	have := map[string]bool{}
	for _, w := range split(v) {
		have[w] = true
	}
	var out []string
	seen := map[string]bool{}
	for _, w := range split(allowed) {
		if have[w] && !seen[w] {
			seen[w] = true
			out = append(out, w)
		}
	}
	return strings.Join(out, ",")
}

func jsonJoinArray(v, path, sep string) (string, error) {
	root, err := decodeJSON([]byte(v))
	if err != nil {
		return v, fmt.Errorf("value is not JSON: %w", err)
	}
	sel, ok := jsonPath(root, path)
	if !ok {
		return "", nil
	}
	arr, isArr := sel.([]any)
	if !isArr {
		return jsonString(sel), nil
	}
	parts := make([]string, 0, len(arr))
	for _, e := range arr {
		parts = append(parts, jsonString(e))
	}
	return strings.Join(parts, sep), nil
}

// normalizeSpace collapses runs of whitespace and trims, as Cardigann does
// for selected text.
func normalizeSpace(s string) string {
	return strings.Join(strings.FieldsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || r == ' ' }), " ")
}

// andMatch reports whether every keyword of query appears in title
// (case-insensitively). limit > 0 only considers that many characters of
// the query.
func andMatch(title, query string, limit int) bool {
	if limit > 0 && len(query) > limit {
		query = query[:limit]
	}
	split := func(s string) []string {
		s = strings.ToLower(s)
		if out, _, err := transform.String(transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC), s); err == nil {
			s = out
		}
		return strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	}
	titleTokens := split(title)
	words := map[string]bool{}
	for _, w := range titleTokens {
		words[w] = true
	}
	// Index collapsed runs of single-letter initialisms ("l a" <-> "la").
	for i := 0; i < len(titleTokens); i++ {
		if len(titleTokens[i]) == 1 && titleTokens[i][0] >= 'a' && titleTokens[i][0] <= 'z' {
			j := i + 1
			for j < len(titleTokens) && len(titleTokens[j]) == 1 && titleTokens[j][0] >= 'a' && titleTokens[j][0] <= 'z' {
				j++
			}
			if j-i >= 2 {
				words[strings.Join(titleTokens[i:j], "")] = true
			}
		} else if len(titleTokens[i]) >= 2 && len(titleTokens[i]) <= 5 {
			// Also allow an initialism in the title ("la", "mash") to satisfy
			// split single-letter tokens when query was sanitized from "L.A.".
		}
	}
	qTokens := split(query)
	for i := 0; i < len(qTokens); i++ {
		w := qTokens[i]
		if words[w] {
			continue
		}
		// Single-letter initialism run in query ("l", "a") matched against
		// joined token ("la") in title.
		if len(w) == 1 && w[0] >= 'a' && w[0] <= 'z' {
			j := i + 1
			for j < len(qTokens) && len(qTokens[j]) == 1 && qTokens[j][0] >= 'a' && qTokens[j][0] <= 'z' {
				j++
			}
			if j-i >= 2 && words[strings.Join(qTokens[i:j], "")] {
				i = j - 1
				continue
			}
		}
		// S02E03 in query also matches S02.E03 ("s02" + "e03") or 2x03 ("2x03" / "02x03").
		if len(w) == 6 && w[0] == 's' && w[3] == 'e' {
			sPart, ePart := w[:3], w[3:]
			if words[sPart] && words[ePart] {
				continue
			}
			sNum, sErr := strconv.Atoi(w[1:3])
			eNum, eErr := strconv.Atoi(w[4:6])
			if sErr == nil && eErr == nil {
				if words[fmt.Sprintf("%dx%02d", sNum, eNum)] || words[fmt.Sprintf("%02dx%02d", sNum, eNum)] {
					continue
				}
			}
		}
		return false
	}
	return true
}

// decodeJSON decodes keeping numbers as their source text.
func decodeJSON(b []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}
