package cardigann

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/PuerkitoBio/goquery"
	"github.com/andybalholm/cascadia"
	"golang.org/x/net/html"
)

// errNotFound reports a selector that matched nothing.
var errNotFound = errors.New("selector matched nothing")

var (
	cssMu    sync.Mutex
	cssCache = map[string]cascadia.Selector{}
)

// compileCSS parses a CSS selector group (cascadia supports :has,
// :contains, :not, :root and the structural pseudo-classes definitions use).
func compileCSS(sel string) (cascadia.Selector, error) {
	cssMu.Lock()
	defer cssMu.Unlock()
	if c, ok := cssCache[sel]; ok {
		return c, nil
	}
	c, err := cascadia.Compile(sel)
	if err != nil {
		return nil, fmt.Errorf("CSS selector %q: %w", sel, err)
	}
	if len(cssCache) > 4096 {
		cssCache = map[string]cascadia.Selector{}
	}
	cssCache[sel] = c
	return c, nil
}

// cssFind returns s itself when it matches sel, else the first descendant
// match (Cardigann's selection rule for field selectors).
func cssFind(s *goquery.Selection, sel string) (*goquery.Selection, error) {
	m, err := compileCSS(sel)
	if err != nil {
		return nil, err
	}
	if s.IsMatcher(m) {
		return s.First(), nil
	}
	return s.FindMatcher(m).First(), nil
}

// htmlValue evaluates a selector block against an HTML (or XML) element.
// found is false when the selector or attribute is missing.
func htmlValue(sel Selector, s *goquery.Selection, env filterEnv) (string, bool, error) {
	if sel.HasText {
		v, err := render(sel.Text, env.vars, nil)
		if err != nil {
			return "", false, err
		}
		v, err = applyFilters(v, sel.Filters, env)
		return v, true, err
	}
	selection := s
	if sel.Selector != "" {
		q, err := render(sel.Selector, env.vars, nil)
		if err != nil {
			return "", false, err
		}
		selection, err = cssFind(s, q)
		if err != nil {
			return "", false, err
		}
		if selection.Length() == 0 {
			return "", false, nil
		}
	}
	if sel.Remove != "" {
		m, err := compileCSS(sel.Remove)
		if err != nil {
			return "", false, err
		}
		selection = selection.Clone()
		selection.FindMatcher(m).Remove()
	}
	var value string
	switch {
	case len(sel.Case) > 0:
		matched := false
		for _, c := range sel.Case {
			if c.Key == "*" {
				value, matched = c.Value, true
				break
			}
			m, err := compileCSS(c.Key)
			if err != nil {
				return "", false, err
			}
			if selection.IsMatcher(m) || selection.FindMatcher(m).Length() > 0 {
				value, matched = c.Value, true
				break
			}
		}
		if !matched {
			return "", false, nil
		}
	case sel.Attribute != "":
		v, ok := selection.Attr(sel.Attribute)
		if !ok {
			return "", false, nil
		}
		value = v
	default:
		value = selection.Text()
	}
	value, err := applyFilters(normalizeSpace(value), sel.Filters, env)
	return value, true, err
}

// --- XML --------------------------------------------------------------------

// parseXMLDocument builds an HTML-node tree from an XML document so CSS
// selectors work on RSS-style responses. Element and attribute names are
// lower-cased (selectors are matched case-insensitively) and keep their
// namespace prefix ("torznab:attr" is selected as `torznab\:attr`).
func parseXMLDocument(body []byte) (*goquery.Document, error) {
	dec := xml.NewDecoder(bytes.NewReader(body))
	dec.Strict = false
	dec.Entity = xml.HTMLEntity
	dec.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
	root := &html.Node{Type: html.DocumentNode}
	cur := root
	prefixes := map[string]string{}
	name := func(n xml.Name) string {
		local := strings.ToLower(n.Local)
		if n.Space == "" {
			return local
		}
		if p, ok := prefixes[n.Space]; ok {
			if p == "" {
				return local
			}
			return p + ":" + local
		}
		return strings.ToLower(n.Space) + ":" + local
	}
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			if cur != root || root.FirstChild != nil {
				break // keep what parsed; truncated feeds are common
			}
			return nil, fmt.Errorf("parse XML: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			for _, a := range t.Attr {
				if a.Name.Space == "xmlns" {
					prefixes[a.Value] = strings.ToLower(a.Name.Local)
				} else if a.Name.Space == "" && a.Name.Local == "xmlns" {
					prefixes[a.Value] = ""
				}
			}
			n := &html.Node{Type: html.ElementNode, Data: name(t.Name)}
			for _, a := range t.Attr {
				if a.Name.Space == "xmlns" || a.Name.Local == "xmlns" {
					continue
				}
				n.Attr = append(n.Attr, html.Attribute{Key: name(a.Name), Val: a.Value})
			}
			cur.AppendChild(n)
			cur = n
		case xml.EndElement:
			if cur.Parent != nil {
				cur = cur.Parent
			}
		case xml.CharData:
			cur.AppendChild(&html.Node{Type: html.TextNode, Data: string(t)})
		}
	}
	return goquery.NewDocumentFromNode(root), nil
}

// --- JSON -------------------------------------------------------------------

// jsonString renders a JSON value the way Cardigann's .NET engine does:
// booleans as True/False, numbers as written, containers as JSON text.
func jsonString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case json.Number:
		return x.String()
	case bool:
		if x {
			return "True"
		}
		return "False"
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// jsonPath evaluates a dotted path ("data.items[0].name", "$.data",
// "a['b c']", "items[*].name", "*") against a decoded document.
func jsonPath(root any, path string) (any, bool) {
	path = strings.TrimSpace(path)
	switch {
	case path == "" || path == "$":
		return root, true
	case strings.HasPrefix(path, "$."):
		path = path[2:]
	case strings.HasPrefix(path, "$["):
		path = path[1:]
	}
	cur := []any{root}
	wild := false
	for _, seg := range splitJSONPath(path) {
		var next []any
		for _, node := range cur {
			next = append(next, jsonStep(node, seg, &wild)...)
		}
		cur = next
		if len(cur) == 0 {
			return nil, false
		}
	}
	if wild {
		return cur, true
	}
	return cur[0], true
}

type jsonSeg struct {
	key   string // object key ("*" for all values)
	index string // array index, "*" or "" for none
	isIdx bool
}

func splitJSONPath(path string) []jsonSeg {
	var segs []jsonSeg
	for path != "" {
		path = strings.TrimPrefix(path, ".")
		if strings.HasPrefix(path, "[") {
			end := strings.IndexByte(path, ']')
			if end < 0 {
				segs = append(segs, jsonSeg{key: path})
				return segs
			}
			inner := strings.TrimSpace(path[1:end])
			path = path[end+1:]
			if len(inner) >= 2 && (inner[0] == '\'' || inner[0] == '"') {
				segs = append(segs, jsonSeg{key: inner[1 : len(inner)-1]})
			} else {
				segs = append(segs, jsonSeg{index: inner, isIdx: true})
			}
			continue
		}
		end := strings.IndexAny(path, ".[")
		if end < 0 {
			segs = append(segs, jsonSeg{key: path})
			return segs
		}
		if end > 0 {
			segs = append(segs, jsonSeg{key: path[:end]})
		}
		path = path[end:]
	}
	return segs
}

func jsonStep(node any, seg jsonSeg, wild *bool) []any {
	if seg.isIdx {
		arr, ok := node.([]any)
		if !ok {
			if obj, isObj := node.(map[string]any); isObj && seg.index != "*" {
				if v, has := obj[seg.index]; has {
					return []any{v}
				}
			}
			if obj, isObj := node.(map[string]any); isObj && seg.index == "*" {
				*wild = true
				return objectValues(obj)
			}
			return nil
		}
		if seg.index == "*" {
			*wild = true
			return arr
		}
		i, err := strconv.Atoi(seg.index)
		if err != nil {
			return nil
		}
		if i < 0 {
			i += len(arr)
		}
		if i < 0 || i >= len(arr) {
			return nil
		}
		return []any{arr[i]}
	}
	switch n := node.(type) {
	case map[string]any:
		if seg.key == "*" {
			*wild = true
			return objectValues(n)
		}
		if v, ok := n[seg.key]; ok {
			return []any{v}
		}
	case []any:
		if seg.key == "*" {
			*wild = true
			return n
		}
		if i, err := strconv.Atoi(seg.key); err == nil && i >= 0 && i < len(n) {
			return []any{n[i]}
		}
	}
	return nil
}

func objectValues(obj map[string]any) []any {
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]any, len(keys))
	for i, k := range keys {
		out[i] = obj[k]
	}
	return out
}

// splitJSONSelector separates "path:filter(arg):filter(arg)" into the path
// and its filters. Filter arguments may themselves contain ':' and
// parentheses.
func splitJSONSelector(sel string) (string, [][2]string) {
	i := strings.IndexByte(sel, ':')
	if i < 0 {
		return sel, nil
	}
	path, rest := sel[:i], sel[i:]
	var filters [][2]string
	for len(rest) > 0 && rest[0] == ':' {
		open := strings.IndexByte(rest, '(')
		if open < 0 {
			break
		}
		name := rest[1:open]
		depth, end := 0, -1
		for j := open; j < len(rest); j++ {
			switch rest[j] {
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 {
					end = j
				}
			}
			if end >= 0 {
				break
			}
		}
		if end < 0 {
			break
		}
		filters = append(filters, [2]string{name, rest[open+1 : end]})
		rest = rest[end+1:]
	}
	return path, filters
}

// jsonSelect evaluates a Cardigann JSON selector with :has(), :not() and
// :contains() filters. ok is false when the path is missing or a filter
// rejects the value.
func jsonSelect(node any, sel string) (any, bool, error) {
	path, filters := splitJSONSelector(sel)
	v := node
	if strings.TrimSpace(path) != "" {
		var ok bool
		v, ok = jsonPath(node, path)
		if !ok {
			return nil, false, nil
		}
	}
	for _, f := range filters {
		switch f[0] {
		case "has":
			if _, ok, err := jsonSelect(v, f[1]); err != nil || !ok {
				return nil, false, err
			}
		case "not":
			if _, ok, err := jsonSelect(v, f[1]); err != nil || ok {
				return nil, false, err
			}
		case "contains":
			if !strings.Contains(jsonString(v), f[1]) {
				return nil, false, nil
			}
		default:
			return nil, false, fmt.Errorf("JSON selector filter %q is not supported", f[0])
		}
	}
	return v, true, nil
}

// jsonRows selects the row array: the path's array (or an object's
// values) with each element kept only when the selector's filters accept it.
func jsonRows(root any, sel string) ([]any, bool, error) {
	path, filters := splitJSONSelector(sel)
	v, ok := jsonPath(root, path)
	if !ok || v == nil {
		return nil, false, nil
	}
	var rows []any
	switch x := v.(type) {
	case []any:
		rows = x
	case map[string]any:
		rows = objectValues(x)
	default:
		return nil, false, fmt.Errorf("rows selector %q did not select an array", sel)
	}
	if len(filters) == 0 {
		return rows, true, nil
	}
	suffix := sel[len(path):]
	out := rows[:0:0]
	for _, r := range rows {
		_, keep, err := jsonSelect(r, suffix)
		if err != nil {
			return nil, true, err
		}
		if keep {
			out = append(out, r)
		}
	}
	return out, true, nil
}

// jsonValue evaluates a selector block against a JSON row (parent is the
// enclosing row for "multiple" rows; selectors starting with ".." read it).
func jsonValue(sel Selector, row, parent any, env filterEnv) (string, bool, error) {
	if sel.HasText {
		v, err := render(sel.Text, env.vars, nil)
		if err != nil {
			return "", false, err
		}
		v, err = applyFilters(v, sel.Filters, env)
		return v, true, err
	}
	var value string
	if sel.Selector != "" {
		q, err := render(sel.Selector, env.vars, nil)
		if err != nil {
			return "", false, err
		}
		node := row
		if strings.HasPrefix(q, "..") && parent != nil {
			node, q = parent, strings.TrimPrefix(q, "..")
		}
		v, ok, err := jsonSelect(node, q)
		if err != nil {
			return "", false, err
		}
		if !ok || v == nil {
			return "", false, nil
		}
		value = jsonString(v)
	}
	if len(sel.Case) > 0 {
		matched := false
		for _, c := range sel.Case {
			if c.Key == "*" || c.Key == value {
				value, matched = c.Value, true
				break
			}
		}
		if !matched {
			return "", false, nil
		}
	}
	value, err := applyFilters(value, sel.Filters, env)
	return value, true, err
}
