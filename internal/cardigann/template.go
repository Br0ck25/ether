package cardigann

import (
	"fmt"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/dlclark/regexp2"
)

// vars is the template data: .Config, .Query, .Keywords, .Categories,
// .Result, .True, .False, .Today and download-time .DownloadUri.
type vars map[string]any

func (v vars) clone() vars {
	out := make(vars, len(v)+2)
	for k, val := range v {
		out[k] = val
	}
	return out
}

// result returns the row's .Result map, creating it when absent.
func (v vars) result() map[string]string {
	if m, ok := v["Result"].(map[string]string); ok {
		return m
	}
	m := map[string]string{}
	v["Result"] = m
	return m
}

// baseVars builds the variables every template sees.
func baseVars(config map[string]any, now time.Time) vars {
	return vars{
		"Config":     config,
		"Query":      map[string]string{},
		"Keywords":   "",
		"Categories": []string{},
		"Result":     map[string]string{},
		"True":       "True",
		"False":      "",
		"Today":      map[string]string{"Year": strconv.Itoa(now.Year())},
	}
}

// render executes a Cardigann template. mod, when non-nil, is applied to
// every value the template prints (Cardigann URL-encodes substitutions in
// search paths and $raw inputs, but not the template's literal text).
func render(tpl string, data vars, mod func(string) string) (string, error) {
	if !strings.Contains(tpl, "{{") {
		return tpl, nil
	}
	t, err := compile(tpl)
	if err != nil {
		return "", err
	}
	if mod != nil {
		if t, err = t.Clone(); err != nil {
			return "", err
		}
		t.Funcs(template.FuncMap{"_mod": func(v any) string { return mod(toStr(v)) }})
	}
	var b strings.Builder
	if err := t.Execute(&b, map[string]any(data)); err != nil {
		return "", fmt.Errorf("template %q: %w", abbreviate(tpl), err)
	}
	return b.String(), nil
}

// mustRender renders, falling back to "" on errors (used where Cardigann
// treats a failed template as an empty value).
func mustRender(tpl string, data vars) string {
	s, err := render(tpl, data, nil)
	if err != nil {
		return ""
	}
	return s
}

var (
	compiledMu sync.Mutex
	compiled   = map[string]*template.Template{}
)

// compile translates and parses a template, caching by text.
func compile(tpl string) (*template.Template, error) {
	key := tpl
	compiledMu.Lock()
	defer compiledMu.Unlock()
	if t, ok := compiled[key]; ok {
		return t, nil
	}
	t, err := template.New("cardigann").Option("missingkey=zero").Funcs(templateFuncs).Parse(translate(tpl))
	if err != nil {
		return nil, fmt.Errorf("template %q: %w", abbreviate(tpl), err)
	}
	if len(compiled) > 4096 {
		compiled = map[string]*template.Template{}
	}
	compiled[key] = t
	return t, nil
}

func abbreviate(s string) string {
	if len(s) > 80 {
		return s[:77] + "..."
	}
	return s
}

var controlWords = map[string]bool{
	"if": true, "else": true, "end": true, "range": true, "with": true, "define": true,
	"template": true, "block": true, "break": true, "continue": true,
}

// translate rewrites Cardigann template idioms into Go text/template:
// double-quoted literals carry no escape sequences in Cardigann (regular
// expressions such as "\s+" are common), so they become raw literals; and
// every printing action is wrapped in _mod, which prints missing values as
// "" (not "<no value>") and lets a caller URL-encode substituted values.
func translate(tpl string) string {
	var b strings.Builder
	for {
		start := strings.Index(tpl, "{{")
		if start < 0 {
			b.WriteString(tpl)
			return b.String()
		}
		b.WriteString(tpl[:start])
		rest := tpl[start+2:]
		// Find the closing delimiter outside string literals.
		end, inStr := -1, byte(0)
		for i := 0; i < len(rest); i++ {
			c := rest[i]
			if inStr != 0 {
				if c == inStr {
					inStr = 0
				}
				continue
			}
			if c == '"' || c == '`' {
				inStr = c
				continue
			}
			if c == '}' && i+1 < len(rest) && rest[i+1] == '}' {
				end = i
				break
			}
		}
		if end < 0 {
			b.WriteString(tpl[start:])
			return b.String()
		}
		body := rest[:end]
		tpl = rest[end+2:]
		ltrim, rtrim := "", ""
		if strings.HasPrefix(body, "- ") || body == "-" {
			ltrim, body = "- ", body[1:]
		}
		if strings.HasSuffix(body, " -") {
			rtrim, body = " -", body[:len(body)-1]
		}
		body = rawLiterals(body)
		trimmed := strings.TrimSpace(body)
		word := trimmed
		if i := strings.IndexAny(word, " \t\n("); i >= 0 {
			word = word[:i]
		}
		if trimmed != "" && !controlWords[word] && !strings.HasPrefix(trimmed, "/*") && !strings.Contains(trimmed, ":=") && !strings.Contains(trimmed, " = ") {
			body = " _mod (" + trimmed + ") "
		}
		b.WriteString("{{" + ltrim + body + rtrim + "}}")
	}
}

// rawLiterals converts "..." literals in an action into Go raw `...`
// literals (or a quoted literal when the text contains a backtick).
func rawLiterals(action string) string {
	var b strings.Builder
	for i := 0; i < len(action); i++ {
		c := action[i]
		if c == '`' {
			j := strings.IndexByte(action[i+1:], '`')
			if j < 0 {
				b.WriteString(action[i:])
				return b.String()
			}
			b.WriteString(action[i : i+j+2])
			i += j + 1
			continue
		}
		if c != '"' {
			b.WriteByte(c)
			continue
		}
		j := strings.IndexByte(action[i+1:], '"')
		if j < 0 {
			b.WriteString(action[i:])
			return b.String()
		}
		lit := action[i+1 : i+1+j]
		if strings.Contains(lit, "`") {
			b.WriteString(strconv.Quote(lit))
		} else {
			b.WriteString("`" + lit + "`")
		}
		i += j + 1
	}
	return b.String()
}

var templateFuncs = template.FuncMap{
	"_mod":       func(v any) string { return toStr(v) },
	"re_replace": tplReReplace,
	"join":       tplJoin,
	"eq":         tplEq,
	"ne":         func(a any, b ...any) bool { return !tplEq(a, b...) },
	"urlencode":  func(v any) string { return url.QueryEscape(toStr(v)) },
	"tolower":    func(v any) string { return strings.ToLower(toStr(v)) },
	"toupper":    func(v any) string { return strings.ToUpper(toStr(v)) },
	"trim":       func(v any) string { return strings.TrimSpace(toStr(v)) },
}

func tplReReplace(input any, pattern, replacement string) (string, error) {
	re, err := compileRegex(pattern)
	if err != nil {
		return "", err
	}
	return re.Replace(toStr(input), replacement, -1, -1)
}

func tplJoin(list any, sep string) string {
	switch l := list.(type) {
	case []string:
		return strings.Join(l, sep)
	case string:
		return l
	case nil:
		return ""
	}
	rv := reflect.ValueOf(list)
	if rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array {
		parts := make([]string, rv.Len())
		for i := range parts {
			parts[i] = toStr(rv.Index(i).Interface())
		}
		return strings.Join(parts, sep)
	}
	return toStr(list)
}

// tplEq compares by string value, so missing values equal "" instead of
// failing with Go's strict type checks.
func tplEq(a any, bs ...any) bool {
	as := toStr(a)
	for _, b := range bs {
		if as == toStr(b) {
			return true
		}
	}
	return false
}

func toStr(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case []string:
		return strings.Join(x, ",")
	case bool:
		if x {
			return "True"
		}
		return ""
	case fmt.Stringer:
		return x.String()
	}
	return fmt.Sprint(v)
}

var (
	regexMu    sync.Mutex
	regexCache = map[string]*regexp2.Regexp{}
)

// compileRegex compiles a .NET-compatible expression (definitions are
// written for .NET's regex engine) with a match timeout.
func compileRegex(pattern string) (*regexp2.Regexp, error) {
	regexMu.Lock()
	defer regexMu.Unlock()
	if re, ok := regexCache[pattern]; ok {
		return re, nil
	}
	re, err := regexp2.Compile(pattern, regexp2.None)
	if err != nil {
		return nil, fmt.Errorf("regular expression %q: %w", pattern, err)
	}
	re.MatchTimeout = time.Second
	if len(regexCache) > 2048 {
		regexCache = map[string]*regexp2.Regexp{}
	}
	regexCache[pattern] = re
	return re, nil
}
