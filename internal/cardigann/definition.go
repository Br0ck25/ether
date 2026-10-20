// Package cardigann runs owner-supplied indexer definitions written in the
// Cardigann YAML format (the format Prowlarr and Jackett use for their
// built-in sites). Ether ships the engine only: no site definitions are
// bundled. Definitions are installed by the owner, either uploaded or
// fetched from a definitions source the owner configures, and stored in
// the database.
//
// A configured indexer of kind "cardigann" is searched through
// newznab.Client's pluggable backend, so built-in sites share Ether's daily
// budgets, result cache, reputation events and manager fan-out with
// Newznab and Torznab indexers.
package cardigann

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// maxDefinitionBytes bounds an uploaded or downloaded definition.
const maxDefinitionBytes = 1 << 20

// Definition is a parsed Cardigann indexer definition.
type Definition struct {
	ID              string
	Name            string
	Description     string
	Language        string
	Type            string // public, semi-private, private
	Encoding        string
	Links           []string
	LegacyLinks     []string
	FollowRedirect  bool
	TestLinkTorrent bool
	RequestDelay    float64 // seconds between requests
	// Protocol is "torrent" (the Cardigann default) or "usenet" for a site
	// whose downloads are NZB documents. Definitions may declare
	// `protocol: usenet`; anything else is a torrent site.
	Protocol string
	Caps     Caps
	Settings []Setting
	Login    *Login
	Search   Search
	Download *Download
}

// Caps is the definition's capabilities block.
type Caps struct {
	// CategoryMappings maps a site category id to a Newznab category.
	CategoryMappings []CategoryMapping
	Modes            map[string][]string
	AllowRawSearch   bool
}

// CategoryMapping is one caps.categorymappings (or legacy caps.categories) entry.
type CategoryMapping struct {
	ID      string
	Cat     string // Newznab category name, e.g. "Movies/HD"
	Desc    string
	Default bool
	Newznab int // resolved Newznab category id; 0 when Cat is unknown
}

// Setting is one owner-editable setting a definition declares.
type Setting struct {
	Name     string   `json:"name"`
	Type     string   `json:"type"`
	Label    string   `json:"label,omitempty"`
	Default  string   `json:"default,omitempty"`
	Defaults []string `json:"defaults,omitempty"`
	Options  []Option `json:"options,omitempty"`
}

// Option is one value of a select setting.
type Option struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// Secret reports whether the setting holds a credential.
func (s Setting) Secret() bool { return s.Type == "password" }

// Info reports whether the setting is read-only guidance text.
func (s Setting) Info() bool { return strings.HasPrefix(s.Type, "info") }

// Login describes how to establish a session with the site.
type Login struct {
	Path              string
	SubmitPath        string
	Method            string // post, form, cookie, get, oneurl
	Form              string
	Inputs            []KV
	SelectorInputs    []NamedSelector
	GetSelectorInputs []NamedSelector
	Cookies           []string
	Headers           []HeaderKV
	Error             []ErrorBlock
	Test              *LoginTest
	Captcha           bool
}

// LoginTest verifies an established session.
type LoginTest struct {
	Path     string
	Selector string
}

// ErrorBlock detects an error page: when Selector matches, the request
// failed with Message (or the matched element's text).
type ErrorBlock struct {
	Path     string
	Selector string
	Message  *Selector
}

// Search is the definition's search block.
type Search struct {
	Paths                []SearchPath
	Inputs               []KV
	Headers              []HeaderKV
	KeywordsFilters      []Filter
	PreprocessingFilters []Filter
	Rows                 Rows
	Fields               []Field
	Error                []ErrorBlock
	AllowEmptyInputs     bool
}

// SearchPath is one request issued per search.
type SearchPath struct {
	Path           string
	Method         string
	Inputs         []KV
	InheritInputs  bool
	Categories     []string
	FollowRedirect bool
	Response       Response
}

// Response describes how to parse a search response.
type Response struct {
	Type             string // html (default), json, xml
	NoResultsMessage string
}

// Rows selects result rows from a response.
type Rows struct {
	Selector                        string
	After                           int
	Remove                          string
	DateHeaders                     *Selector
	Count                           *Selector
	Multiple                        bool
	MissingAttributeEqualsNoResults bool
	Attribute                       string
	Filters                         []Filter
}

// Field is one search.fields entry; Name may carry "|modifier" suffixes.
type Field struct {
	Name      string
	Modifiers []string
	Selector  Selector
}

// Selector is a Cardigann selector block.
type Selector struct {
	Selector          string
	Attribute         string
	Remove            string
	Filters           []Filter
	Case              []KV
	Optional          bool
	Text              string
	HasText           bool
	Default           string
	HasDefault        bool
	UseBeforeResponse bool
}

// NamedSelector is a selector bound to an input name.
type NamedSelector struct {
	Name     string
	Selector Selector
}

// Filter is one filter step.
type Filter struct {
	Name string
	Args []string
}

// Download is the definition's download block.
type Download struct {
	Selectors []Selector
	Method    string
	Before    *Request
	InfoHash  *InfoHashBlock
}

// Request is a templated HTTP request (download.before).
type Request struct {
	Path   string
	Method string
	Inputs []KV
}

// InfoHashBlock builds a magnet from a page's hash and title.
type InfoHashBlock struct {
	Hash              Selector
	Title             Selector
	UseBeforeResponse bool
}

// KV is an ordered key/value pair.
type KV struct {
	Key   string
	Value string
}

// HeaderKV is one header with its (templated) values.
type HeaderKV struct {
	Name   string
	Values []string
}

// ---------------------------------------------------------------------------
// YAML decoding. Cardigann documents mix scalars and lists freely and rely
// on map order (fields are evaluated top to bottom), so most blocks are
// decoded from yaml.Node by hand.

// Parse decodes and validates a definition. Problems that make the
// definition unusable are returned as an error; unsupported optional
// features are returned as warnings.
func Parse(data []byte) (*Definition, []string, error) {
	if len(data) > maxDefinitionBytes {
		return nil, nil, fmt.Errorf("definition exceeds %d bytes", maxDefinitionBytes)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		// YAML 1.2 (and the .NET parser the reference engines use) accepts
		// "\/" in double-quoted strings; go-yaml does not. Retry with it
		// rewritten to "/", which is what it means.
		fixed, changed := unescapeSlashes(data)
		if !changed || yaml.Unmarshal(fixed, &doc) != nil {
			return nil, nil, fmt.Errorf("definition is not valid YAML: %w", err)
		}
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, nil, errors.New("definition must be a YAML mapping")
	}
	p := &parser{}
	def := p.definition(doc.Content[0])
	if p.err != nil {
		return nil, nil, p.err
	}
	errs, warnings := def.validate()
	warnings = append(p.warnings, warnings...)
	if len(errs) > 0 {
		return def, warnings, &ValidationError{Problems: errs}
	}
	return def, warnings, nil
}

// ValidationError lists the reasons a definition cannot be used.
type ValidationError struct{ Problems []string }

func (e *ValidationError) Error() string {
	return "invalid definition: " + strings.Join(e.Problems, "; ")
}

type parser struct {
	err      error
	warnings []string
}

func (p *parser) fail(format string, args ...any) {
	if p.err == nil {
		p.err = fmt.Errorf(format, args...)
	}
}

// mapping returns key → value nodes of a mapping node in document order.
func mapping(n *yaml.Node) [][2]*yaml.Node {
	budget := maxMergeVisits
	return mergeMapping(nil, n, 0, &budget)
}

// Merge keys ("<<: *anchor") are expanded by hand, so a self-referencing
// anchor ("a: &x {<<: *x}") or a fan-out of merges must not recurse
// forever or expand exponentially.
const (
	maxMergeDepth  = 16
	maxMergeVisits = 1 << 16
)

func mergeMapping(out [][2]*yaml.Node, n *yaml.Node, depth int, budget *int) [][2]*yaml.Node {
	n = resolveAlias(n)
	if n == nil || n.Kind != yaml.MappingNode || depth > maxMergeDepth {
		return out
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if *budget <= 0 {
			return out
		}
		*budget--
		k, v := n.Content[i], resolveAlias(n.Content[i+1])
		if k.Value == "<<" { // merge key
			out = mergeMapping(out, v, depth+1, budget)
			continue
		}
		out = append(out, [2]*yaml.Node{k, v})
	}
	return out
}

func resolveAlias(n *yaml.Node) *yaml.Node {
	for n != nil && n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	return n
}

func scalar(n *yaml.Node) string {
	n = resolveAlias(n)
	if n == nil || n.Kind != yaml.ScalarNode {
		return ""
	}
	if n.Tag == "!!null" {
		return ""
	}
	return n.Value
}

func boolean(n *yaml.Node) bool {
	switch strings.ToLower(scalar(n)) {
	case "true", "yes", "on", "1":
		return true
	}
	return false
}

func number(n *yaml.Node) float64 {
	f, _ := strconv.ParseFloat(scalar(n), 64)
	return f
}

// strList accepts a scalar or a sequence of scalars.
func strList(n *yaml.Node) []string {
	n = resolveAlias(n)
	if n == nil {
		return nil
	}
	switch n.Kind {
	case yaml.ScalarNode:
		if n.Tag == "!!null" {
			return nil
		}
		return []string{n.Value}
	case yaml.SequenceNode:
		out := make([]string, 0, len(n.Content))
		for _, c := range n.Content {
			out = append(out, scalar(c))
		}
		return out
	}
	return nil
}

func kvs(n *yaml.Node) []KV {
	var out []KV
	for _, kv := range mapping(n) {
		v := kv[1]
		val := scalar(v)
		if v.Kind == yaml.SequenceNode {
			val = strings.Join(strList(v), ",")
		}
		out = append(out, KV{Key: kv[0].Value, Value: val})
	}
	return out
}

func headers(n *yaml.Node) []HeaderKV {
	var out []HeaderKV
	for _, kv := range mapping(n) {
		out = append(out, HeaderKV{Name: kv[0].Value, Values: strList(kv[1])})
	}
	return out
}

func (p *parser) filters(n *yaml.Node) []Filter {
	n = resolveAlias(n)
	if n == nil || n.Kind != yaml.SequenceNode {
		return nil
	}
	var out []Filter
	for _, item := range n.Content {
		var f Filter
		for _, kv := range mapping(item) {
			switch kv[0].Value {
			case "name":
				f.Name = scalar(kv[1])
			case "args":
				f.Args = strList(kv[1])
			}
		}
		if f.Name != "" {
			out = append(out, f)
		}
	}
	return out
}

func (p *parser) selector(n *yaml.Node) Selector {
	var s Selector
	n = resolveAlias(n)
	if n != nil && n.Kind == yaml.ScalarNode {
		// A bare string is a CSS/JSON selector.
		s.Selector = n.Value
		return s
	}
	for _, kv := range mapping(n) {
		v := kv[1]
		switch kv[0].Value {
		case "selector":
			s.Selector = scalar(v)
		case "attribute":
			s.Attribute = scalar(v)
		case "remove":
			s.Remove = scalar(v)
		case "filters":
			s.Filters = p.filters(v)
		case "case":
			s.Case = kvs(v)
		case "optional":
			s.Optional = boolean(v)
		case "text":
			s.Text, s.HasText = scalar(v), true
		case "default":
			s.Default, s.HasDefault = scalar(v), true
		case "usebeforeresponse":
			s.UseBeforeResponse = boolean(v)
		}
	}
	return s
}

func (p *parser) selectorPtr(n *yaml.Node) *Selector {
	if resolveAlias(n) == nil {
		return nil
	}
	s := p.selector(n)
	return &s
}

func (p *parser) errorBlocks(n *yaml.Node) []ErrorBlock {
	n = resolveAlias(n)
	if n == nil {
		return nil
	}
	items := []*yaml.Node{n}
	if n.Kind == yaml.SequenceNode {
		items = n.Content
	}
	var out []ErrorBlock
	for _, item := range items {
		var e ErrorBlock
		for _, kv := range mapping(item) {
			switch kv[0].Value {
			case "path":
				e.Path = scalar(kv[1])
			case "selector":
				e.Selector = scalar(kv[1])
			case "message":
				e.Message = p.selectorPtr(kv[1])
			}
		}
		if e.Selector != "" {
			out = append(out, e)
		}
	}
	return out
}

func (p *parser) namedSelectors(n *yaml.Node) []NamedSelector {
	var out []NamedSelector
	for _, kv := range mapping(n) {
		out = append(out, NamedSelector{Name: kv[0].Value, Selector: p.selector(kv[1])})
	}
	return out
}

func (p *parser) definition(root *yaml.Node) *Definition {
	d := &Definition{Protocol: "torrent"}
	for _, kv := range mapping(root) {
		v := kv[1]
		switch kv[0].Value {
		case "id":
			d.ID = scalar(v)
		case "name":
			d.Name = scalar(v)
		case "description":
			d.Description = scalar(v)
		case "language":
			d.Language = scalar(v)
		case "type":
			d.Type = scalar(v)
		case "encoding":
			d.Encoding = scalar(v)
		case "links":
			d.Links = strList(v)
		case "legacylinks":
			d.LegacyLinks = strList(v)
		case "followredirect":
			d.FollowRedirect = boolean(v)
		case "testlinktorrent":
			d.TestLinkTorrent = boolean(v)
		case "requestDelay", "requestdelay":
			d.RequestDelay = number(v)
		case "protocol":
			if strings.EqualFold(scalar(v), "usenet") {
				d.Protocol = "usenet"
			}
		case "caps":
			d.Caps = p.caps(v)
		case "settings":
			d.Settings = p.settings(v)
		case "login":
			d.Login = p.login(v)
		case "search":
			d.Search = p.search(v)
		case "download":
			d.Download = p.download(v)
		}
	}
	return d
}

func (p *parser) caps(n *yaml.Node) Caps {
	c := Caps{Modes: map[string][]string{}}
	for _, kv := range mapping(n) {
		v := kv[1]
		switch kv[0].Value {
		case "categories":
			for _, cat := range mapping(v) {
				c.CategoryMappings = append(c.CategoryMappings, CategoryMapping{ID: cat[0].Value, Cat: scalar(cat[1])})
			}
		case "categorymappings":
			if resolveAlias(v) == nil || resolveAlias(v).Kind != yaml.SequenceNode {
				continue
			}
			for _, item := range resolveAlias(v).Content {
				var m CategoryMapping
				for _, f := range mapping(item) {
					switch f[0].Value {
					case "id":
						m.ID = scalar(f[1])
					case "cat":
						m.Cat = scalar(f[1])
					case "desc":
						m.Desc = scalar(f[1])
					case "default":
						m.Default = boolean(f[1])
					}
				}
				c.CategoryMappings = append(c.CategoryMappings, m)
			}
		case "modes":
			for _, mode := range mapping(v) {
				var params []string
				for _, s := range strList(mode[1]) {
					params = append(params, strings.ToLower(strings.TrimSpace(s)))
				}
				c.Modes[mode[0].Value] = params
			}
		case "allowrawsearch":
			c.AllowRawSearch = boolean(v)
		}
	}
	for i := range c.CategoryMappings {
		c.CategoryMappings[i].Newznab = newznabCategoryID(c.CategoryMappings[i].Cat)
	}
	return c
}

func (p *parser) settings(n *yaml.Node) []Setting {
	n = resolveAlias(n)
	if n == nil || n.Kind != yaml.SequenceNode {
		return nil
	}
	var out []Setting
	for _, item := range n.Content {
		var s Setting
		for _, kv := range mapping(item) {
			v := kv[1]
			switch kv[0].Value {
			case "name":
				s.Name = scalar(v)
			case "type":
				s.Type = strings.ToLower(scalar(v))
			case "label":
				s.Label = scalar(v)
			case "default":
				if resolveAlias(v) != nil && resolveAlias(v).Kind == yaml.SequenceNode {
					s.Defaults = strList(v)
				} else {
					s.Default = scalar(v)
				}
			case "defaults":
				s.Defaults = strList(v)
			case "options":
				for _, o := range mapping(v) {
					s.Options = append(s.Options, Option{Value: o[0].Value, Label: scalar(o[1])})
				}
			}
		}
		if s.Name != "" {
			out = append(out, s)
		}
	}
	return out
}

func (p *parser) login(n *yaml.Node) *Login {
	if resolveAlias(n) == nil {
		return nil
	}
	l := &Login{}
	for _, kv := range mapping(n) {
		v := kv[1]
		switch kv[0].Value {
		case "path":
			l.Path = scalar(v)
		case "submitpath":
			l.SubmitPath = scalar(v)
		case "method":
			l.Method = methodValue(scalar(v))
		case "form":
			l.Form = scalar(v)
		case "inputs":
			l.Inputs = kvs(v)
		case "selectorinputs":
			l.SelectorInputs = p.namedSelectors(v)
		case "getselectorinputs":
			l.GetSelectorInputs = p.namedSelectors(v)
		case "cookies":
			l.Cookies = strList(v)
		case "headers":
			l.Headers = headers(v)
		case "error":
			l.Error = p.errorBlocks(v)
		case "test":
			t := &LoginTest{}
			for _, f := range mapping(v) {
				switch f[0].Value {
				case "path":
					t.Path = scalar(f[1])
				case "selector":
					t.Selector = scalar(f[1])
				}
			}
			l.Test = t
		case "captcha":
			l.Captcha = resolveAlias(v) != nil
		}
	}
	if l.Method == "" {
		l.Method = "post"
		if l.Form != "" {
			l.Method = "form"
		}
	}
	return l
}

func (p *parser) search(n *yaml.Node) Search {
	var s Search
	var single *SearchPath
	for _, kv := range mapping(n) {
		v := kv[1]
		switch kv[0].Value {
		case "path":
			single = &SearchPath{Path: scalar(v), InheritInputs: true}
		case "paths":
			if rv := resolveAlias(v); rv != nil && rv.Kind == yaml.SequenceNode {
				for _, item := range rv.Content {
					s.Paths = append(s.Paths, p.searchPath(item))
				}
			}
		case "inputs":
			s.Inputs = kvs(v)
		case "headers":
			s.Headers = headers(v)
		case "keywordsfilters":
			s.KeywordsFilters = p.filters(v)
		case "preprocessingfilters":
			s.PreprocessingFilters = p.filters(v)
		case "rows":
			s.Rows = p.rows(v)
		case "fields":
			for _, f := range mapping(v) {
				parts := strings.Split(f[0].Value, "|")
				s.Fields = append(s.Fields, Field{Name: parts[0], Modifiers: parts[1:], Selector: p.selector(f[1])})
			}
		case "error":
			s.Error = p.errorBlocks(v)
		case "allowEmptyInputs", "allowemptyinputs":
			s.AllowEmptyInputs = boolean(v)
		}
	}
	if len(s.Paths) == 0 && single != nil {
		s.Paths = []SearchPath{*single}
	}
	return s
}

func (p *parser) searchPath(n *yaml.Node) SearchPath {
	sp := SearchPath{InheritInputs: true}
	for _, kv := range mapping(n) {
		v := kv[1]
		switch kv[0].Value {
		case "path":
			sp.Path = scalar(v)
		case "method":
			sp.Method = methodValue(scalar(v))
		case "inputs":
			sp.Inputs = kvs(v)
		case "inheritinputs":
			sp.InheritInputs = boolean(v)
		case "categories":
			sp.Categories = strList(v)
		case "followredirect":
			sp.FollowRedirect = boolean(v)
		case "response":
			for _, f := range mapping(v) {
				switch f[0].Value {
				case "type":
					sp.Response.Type = strings.ToLower(scalar(f[1]))
				case "noResultsMessage", "noresultsmessage":
					sp.Response.NoResultsMessage = scalar(f[1])
				}
			}
		}
	}
	return sp
}

func (p *parser) rows(n *yaml.Node) Rows {
	var r Rows
	n = resolveAlias(n)
	if n != nil && n.Kind == yaml.ScalarNode {
		r.Selector = n.Value
		return r
	}
	for _, kv := range mapping(n) {
		v := kv[1]
		switch kv[0].Value {
		case "selector":
			r.Selector = scalar(v)
		case "after":
			r.After = int(number(v))
		case "remove":
			r.Remove = scalar(v)
		case "dateheaders":
			r.DateHeaders = p.selectorPtr(v)
		case "count":
			r.Count = p.selectorPtr(v)
		case "multiple":
			r.Multiple = boolean(v)
		case "missingAttributeEqualsNoResults", "missingattributeequalsnoresults":
			r.MissingAttributeEqualsNoResults = boolean(v)
		case "attribute":
			r.Attribute = scalar(v)
		case "filters":
			r.Filters = p.filters(v)
		}
	}
	return r
}

func (p *parser) download(n *yaml.Node) *Download {
	if resolveAlias(n) == nil {
		return nil
	}
	d := &Download{}
	for _, kv := range mapping(n) {
		v := kv[1]
		switch kv[0].Value {
		case "selector":
			// Older single-selector form.
			s := p.selector(n)
			d.Selectors = append(d.Selectors, s)
		case "selectors":
			if rv := resolveAlias(v); rv != nil && rv.Kind == yaml.SequenceNode {
				for _, item := range rv.Content {
					d.Selectors = append(d.Selectors, p.selector(item))
				}
			}
		case "method":
			d.Method = methodValue(scalar(v))
		case "before":
			r := &Request{}
			for _, f := range mapping(v) {
				switch f[0].Value {
				case "path":
					r.Path = scalar(f[1])
				case "method":
					r.Method = methodValue(scalar(f[1]))
				case "inputs":
					r.Inputs = kvs(f[1])
				case "pathselector":
					p.warnings = append(p.warnings, "download.before.pathselector is not supported")
				}
			}
			d.Before = r
		case "infohash":
			ih := &InfoHashBlock{}
			for _, f := range mapping(v) {
				switch f[0].Value {
				case "hash":
					ih.Hash = p.selector(f[1])
				case "title":
					ih.Title = p.selector(f[1])
				case "usebeforeresponse":
					ih.UseBeforeResponse = boolean(f[1])
				}
			}
			d.InfoHash = ih
		}
	}
	return d
}

// ---------------------------------------------------------------------------
// Validation

var knownFilters = map[string]bool{
	"querystring": true, "regexp": true, "re_replace": true, "split": true, "replace": true,
	"trim": true, "prepend": true, "append": true, "tolower": true, "toupper": true,
	"urldecode": true, "urlencode": true, "htmldecode": true, "htmlencode": true,
	"dateparse": true, "timeparse": true, "timeago": true, "reltime": true, "fuzzytime": true,
	"diacritics": true, "jsonjoinarray": true, "validfilename": true, "validate": true,
	"strdump": true, "hexdump": true, "andmatch": true,
}

var knownSettingTypes = map[string]bool{
	"text": true, "password": true, "checkbox": true, "select": true, "multi-select": true,
}

var knownLoginMethods = map[string]bool{"post": true, "form": true, "cookie": true, "get": true, "oneurl": true}

func (d *Definition) validate() (errs, warnings []string) {
	if strings.TrimSpace(d.ID) == "" {
		errs = append(errs, "id is required")
	} else if !validID(d.ID) {
		errs = append(errs, "id may contain only letters, digits, '-', '_' and '.'")
	}
	if strings.TrimSpace(d.Name) == "" {
		errs = append(errs, "name is required")
	}
	if len(d.Links) == 0 {
		errs = append(errs, "links must list at least one site URL")
	}
	for _, l := range d.Links {
		if u, err := url.Parse(l); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			errs = append(errs, fmt.Sprintf("link %q is not an http(s) URL", l))
		}
	}
	if len(d.Search.Paths) == 0 {
		errs = append(errs, "search needs a path or paths")
	}
	if d.Search.Rows.Selector == "" {
		errs = append(errs, "search.rows.selector is required")
	}
	has := map[string]bool{}
	for _, f := range d.Search.Fields {
		has[f.Name] = true
	}
	if !has["title"] {
		errs = append(errs, "search.fields must include title")
	}
	if !has["download"] && !has["magnet"] && !has["infohash"] {
		errs = append(errs, "search.fields must include download, magnet or infohash")
	}
	for _, sp := range d.Search.Paths {
		switch sp.Response.Type {
		case "", "html", "json", "xml":
		default:
			errs = append(errs, fmt.Sprintf("search response type %q is not supported", sp.Response.Type))
		}
		if sp.Method != "" && sp.Method != "get" && sp.Method != "post" && !strings.Contains(sp.Method, "{{") {
			errs = append(errs, fmt.Sprintf("search method %q is not supported", sp.Method))
		}
	}
	if d.Login != nil {
		if !knownLoginMethods[d.Login.Method] {
			errs = append(errs, fmt.Sprintf("login method %q is not supported", d.Login.Method))
		}
		if d.Login.Captcha {
			warnings = append(warnings, "the site's login uses a captcha, which Ether cannot solve; sign-in will fail unless the site offers cookie login")
		}
	}
	unknown := map[string]bool{}
	var checkFilters func([]Filter)
	checkFilters = func(fs []Filter) {
		for _, f := range fs {
			if !knownFilters[f.Name] {
				unknown[f.Name] = true
			}
		}
	}
	checkFilters(d.Search.KeywordsFilters)
	checkFilters(d.Search.PreprocessingFilters)
	checkFilters(d.Search.Rows.Filters)
	for _, f := range d.Search.Fields {
		checkFilters(f.Selector.Filters)
	}
	if d.Search.Rows.DateHeaders != nil {
		checkFilters(d.Search.Rows.DateHeaders.Filters)
	}
	names := make([]string, 0, len(unknown))
	for name := range unknown {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		warnings = append(warnings, fmt.Sprintf("filter %q is not supported and will be skipped", name))
	}
	for _, s := range d.Settings {
		if !s.Info() && !knownSettingTypes[s.Type] {
			warnings = append(warnings, fmt.Sprintf("setting %q has unsupported type %q", s.Name, s.Type))
		}
	}
	unmapped := 0
	for _, m := range d.Caps.CategoryMappings {
		if m.Newznab == 0 {
			unmapped++
		}
	}
	if unmapped > 0 {
		warnings = append(warnings, fmt.Sprintf("%d category mapping(s) use an unknown Newznab category", unmapped))
	}
	return errs, warnings
}

func validID(id string) bool {
	if len(id) > 100 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return true
}

// SiteLink returns the base URL requests resolve against: the owner's
// choice when set, else the definition's first link. Always ends in "/".
func (d *Definition) SiteLink(override string) string {
	link := strings.TrimSpace(override)
	if link == "" && len(d.Links) > 0 {
		link = d.Links[0]
	}
	if link != "" && !strings.HasSuffix(link, "/") {
		link += "/"
	}
	return link
}

// SupportsMovies reports whether the definition maps any movie category
// (or maps no categories at all).
func (d *Definition) SupportsMovies() bool { return d.supportsFamily(2000) }

// SupportsTV reports whether the definition maps any TV category (or maps
// no categories at all).
func (d *Definition) SupportsTV() bool { return d.supportsFamily(5000) }

func (d *Definition) supportsFamily(family int) bool {
	if len(d.Caps.CategoryMappings) == 0 {
		return true
	}
	for _, m := range d.Caps.CategoryMappings {
		if m.Newznab/1000*1000 == family {
			return true
		}
	}
	return false
}

// doubleQuoted matches one-line double-quoted YAML scalars.
var doubleQuoted = regexp.MustCompile(`"(?:[^"\\\n]|\\.)*"`)

// unescapeSlashes rewrites the escape "\/" to "/" inside double-quoted
// scalars only (an even run of backslashes before "/" is left alone), so
// plain and single-quoted text keeps its meaning.
func unescapeSlashes(data []byte) ([]byte, bool) {
	changed := false
	out := doubleQuoted.ReplaceAllFunc(data, func(q []byte) []byte {
		var b []byte
		for i := 0; i < len(q); i++ {
			if q[i] == '\\' && i+1 < len(q) {
				if q[i+1] == '/' {
					b = append(b, '/')
					changed = true
				} else {
					b = append(b, q[i], q[i+1])
				}
				i++
				continue
			}
			b = append(b, q[i])
		}
		return b
	})
	return out, changed
}

// methodValue normalises a method name to lower case; a templated method
// ("{{ if .Keywords }}post{{ else }}get{{ end }}") is kept verbatim, since
// lower-casing would break its field names, and is lower-cased once
// rendered.
func methodValue(s string) string {
	if strings.Contains(s, "{{") {
		return strings.TrimSpace(s)
	}
	return strings.ToLower(strings.TrimSpace(s))
}
