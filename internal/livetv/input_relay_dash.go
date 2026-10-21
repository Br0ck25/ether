package livetv

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// DASH manifests reach ffmpeg through the input relay with every URL made
// absolute and pointing back at the relay:
//
//   - BaseURL (MPD, Period, AdaptationSet, Representation) becomes the
//     exact relay URL of its resolved value; redundant BaseURLs are dropped.
//   - SegmentList SegmentURL@media/@index and Initialization,
//     RepresentationIndex and BitstreamSwitching @sourceURL are exact URLs.
//   - SegmentTemplate @media/@initialization/@index/@bitstreamSwitching
//     become template capabilities. Only their identifiers travel in the
//     relay path, and the relay accepts only values the manifest allows:
//     its own Representation ids and bandwidths, and decimal numbers/times.
//   - Location, PatchLocation and UTCTiming are removed; comments and
//     DTDs are not passed on.
//
// Unsupported forms fail the whole manifest: xlink references, identifiers
// other than RepresentationID/Number/Bandwidth/Time/SubNumber, a BaseURL
// after URLs it would apply to, and relative segment URLs inherited by an element
// that sets its own BaseURL (each such element would resolve the shared
// URL differently).

type mpdVar struct {
	kind  string // RepresentationID, Number, Bandwidth, Time, SubNumber
	raw   string // identifier as written, including any %0Nd width
	width int
}

// mpdScope is what a manifest allows its template identifiers to be.
// Immutable once rewriteMPD returns.
type mpdScope struct {
	repIDs, bandwidths map[string]bool
}

var mpdIdent = regexp.MustCompile(`^(RepresentationID|Number|Bandwidth|Time|SubNumber)(%0([1-9][0-9]?)d)?$`)

func placeholder(i int) string { return "ethervar" + strconv.Itoa(i) + "q" }

// fill substitutes request path values into a template resource.
func (r *relayResource) fill(vals []string) (string, bool) {
	out := r.url
	for i, v := range r.vars {
		val, err := url.PathUnescape(vals[i])
		if err != nil || !v.accepts(val, r.scope) {
			return "", false
		}
		out = strings.Replace(out, placeholder(i), val, 1)
	}
	return out, validHTTP(out)
}

func (v mpdVar) accepts(val string, scope *mpdScope) bool {
	switch v.kind {
	case "RepresentationID":
		return scope != nil && scope.repIDs[val]
	case "Bandwidth":
		return scope != nil && scope.bandwidths[val]
	}
	if val == "" || len(val) > 20 || len(val) < v.width {
		return false
	}
	for _, c := range val {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// parseTemplate replaces each $identifier$ by a placeholder that URL
// resolution leaves alone (a %05d width would otherwise read as an escape).
func parseTemplate(v string) (string, []mpdVar, error) {
	if strings.Contains(strings.ToLower(v), "ethervar") {
		return "", nil, errRelayUnsupported
	}
	var b strings.Builder
	var vars []mpdVar
	for {
		i := strings.IndexByte(v, '$')
		if i < 0 {
			b.WriteString(v)
			return b.String(), vars, nil
		}
		b.WriteString(v[:i])
		v = v[i+1:]
		j := strings.IndexByte(v, '$')
		if j < 0 {
			return "", nil, errRelayUnsupported
		}
		id := v[:j]
		v = v[j+1:]
		if id == "" { // $$ is a literal dollar sign
			b.WriteByte('$')
			continue
		}
		m := mpdIdent.FindStringSubmatch(id)
		if m == nil || (m[1] == "RepresentationID" && m[2] != "") {
			return "", nil, errRelayUnsupported
		}
		width, _ := strconv.Atoi(m[3])
		b.WriteString(placeholder(len(vars)))
		vars = append(vars, mpdVar{kind: m[1], raw: id, width: width})
	}
}

// safeTemplateValue is a manifest value the relay can carry in one path
// segment and substitute verbatim, as ffmpeg does.
func safeTemplateValue(v string) bool {
	return v != "" && len(v) <= 128 && !strings.ContainsAny(v, "/?#%\\ \t\r\n\x00$")
}

func mpdScopeOf(body []byte) (*mpdScope, error) {
	scope := &mpdScope{repIDs: map[string]bool{}, bandwidths: map[string]bool{}}
	d := xml.NewDecoder(bytes.NewReader(body))
	for {
		tok, err := d.RawToken()
		if errors.Is(err, io.EOF) {
			return scope, nil
		}
		if err != nil {
			return nil, errRelayUnsupported
		}
		if t, ok := tok.(xml.StartElement); ok && t.Name.Local == "Representation" {
			for _, a := range t.Attr {
				if a.Name.Space == "" && safeTemplateValue(a.Value) {
					switch a.Name.Local {
					case "id":
						scope.repIDs[a.Value] = true
					case "bandwidth":
						scope.bandwidths[a.Value] = true
					}
				}
			}
		}
	}
}

type mpdFrame struct {
	name     xml.Name
	base     *url.URL
	baseSet  bool
	used     bool // a URL was resolved against this element's base
	relSeg   bool // a segment-information child resolved a relative URL
	segOwner int  // index of the frame owning the enclosing segment information, or -1
	skip     bool
}

var (
	mpdTextEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	mpdTemplateAt  = map[string]map[string]bool{
		"SegmentTemplate":     {"media": true, "initialization": true, "index": true, "bitstreamSwitching": true},
		"SegmentURL":          {"media": true, "index": true},
		"Initialization":      {"sourceURL": true},
		"RepresentationIndex": {"sourceURL": true},
		"BitstreamSwitching":  {"sourceURL": true},
	}
	mpdSegmentInfo = map[string]bool{"SegmentTemplate": true, "SegmentList": true, "SegmentBase": true}
	mpdDropped     = map[string]bool{"Location": true, "PatchLocation": true, "UTCTiming": true}
)

func mpdQName(n xml.Name) string {
	if n.Space != "" {
		return n.Space + ":" + n.Local
	}
	return n.Local
}

// rewriteMPD rewrites a DASH manifest fetched from base. exact registers an
// absolute upstream URL; template registers an absolute URL holding one
// placeholder per identifier. Both return the relay URL to publish.
func rewriteMPD(body []byte, base *url.URL, exact func(string) (string, error), template func(string, []mpdVar, *mpdScope) (string, error)) ([]byte, error) {
	scope, err := mpdScopeOf(body)
	if err != nil {
		return nil, err
	}
	d := xml.NewDecoder(bytes.NewReader(body))
	var out bytes.Buffer
	var stack []*mpdFrame
	var baseText *bytes.Buffer // inside a BaseURL element
	sawRoot := false
	resolve := func(f *mpdFrame, owner int, raw string, isTemplate bool) (string, error) {
		raw = strings.TrimSpace(raw)
		protected, vars := raw, []mpdVar(nil)
		if isTemplate {
			var perr error
			if protected, vars, perr = parseTemplate(raw); perr != nil {
				return "", perr
			}
		}
		for _, frame := range stack {
			frame.used = true
		}
		ref, err := url.Parse(protected)
		if err != nil {
			return "", errRelayUnsupported
		}
		if !ref.IsAbs() && owner >= 0 {
			stack[owner].relSeg = true
		}
		abs := f.base.ResolveReference(ref).String()
		if len(vars) == 0 {
			return exact(abs)
		}
		for i := range vars {
			if strings.Count(abs, placeholder(i)) != 1 {
				return "", errRelayUnsupported
			}
		}
		return template(abs, vars, scope)
	}
	for {
		tok, err := d.RawToken()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, errRelayUnsupported
		}
		var top *mpdFrame
		if len(stack) > 0 {
			top = stack[len(stack)-1]
		}
		switch t := tok.(type) {
		case xml.ProcInst:
			if t.Target != "xml" || sawRoot {
				continue
			}
			fmt.Fprintf(&out, "<?xml %s?>", t.Inst)
		case xml.Directive:
			return nil, errRelayUnsupported
		case xml.Comment:
		case xml.CharData:
			switch {
			case baseText != nil:
				baseText.Write(t)
			case top != nil && top.skip:
			default:
				mpdTextEscaper.WriteString(&out, string(t))
			}
		case xml.StartElement:
			if baseText != nil || len(stack) >= 64 {
				return nil, errRelayUnsupported
			}
			for _, a := range t.Attr {
				if a.Name.Local == "href" {
					return nil, errRelayUnsupported // xlink remote elements
				}
			}
			if top == nil {
				if sawRoot || t.Name.Local != "MPD" {
					return nil, errRelayUnsupported
				}
				sawRoot = true
				f := &mpdFrame{name: t.Name, base: base, segOwner: -1}
				stack = append(stack, f)
				writeMPDStart(&out, t)
				continue
			}
			f := &mpdFrame{name: t.Name, base: top.base, segOwner: top.segOwner, skip: top.skip}
			stack = append(stack, f)
			if f.skip {
				continue
			}
			if t.Name.Local == "BaseURL" {
				if top.used {
					return nil, errRelayUnsupported // BaseURL after the URLs it should apply to
				}
				f.skip = true // emitted at its end element
				if !top.baseSet {
					baseText = &bytes.Buffer{}
				}
				continue
			}
			if mpdDropped[t.Name.Local] {
				f.skip = true
				continue
			}
			if mpdSegmentInfo[t.Name.Local] && f.segOwner < 0 {
				f.segOwner = len(stack) - 2
			}
			if attrs := mpdTemplateAt[t.Name.Local]; attrs != nil {
				for i, a := range t.Attr {
					if a.Name.Space != "" || !attrs[a.Name.Local] {
						continue
					}
					if t.Name.Local == "SegmentTemplate" && a.Name.Local == "bitstreamSwitching" && (a.Value == "true" || a.Value == "false") {
						continue
					}
					u, err := resolve(f, f.segOwner, a.Value, t.Name.Local == "SegmentTemplate")
					if err != nil {
						return nil, err
					}
					t.Attr[i].Value = u
				}
			}
			writeMPDStart(&out, t)
		case xml.EndElement:
			if top == nil || top.name != t.Name {
				return nil, errRelayUnsupported
			}
			stack = stack[:len(stack)-1]
			if t.Name.Local == "BaseURL" && baseText != nil {
				parent := stack[len(stack)-1]
				for i := range stack {
					// An inherited relative segment URL was resolved against
					// the old base; this element would resolve it anew.
					if stack[i].relSeg {
						return nil, errRelayUnsupported
					}
				}
				ref, err := url.Parse(strings.TrimSpace(baseText.String()))
				if err != nil {
					return nil, errRelayUnsupported
				}
				baseText = nil
				parent.base, parent.baseSet = parent.base.ResolveReference(ref), true
				u, err := exact(parent.base.String())
				if err != nil {
					return nil, err
				}
				q := mpdQName(t.Name)
				out.WriteString("<" + q + ">")
				mpdTextEscaper.WriteString(&out, u)
				out.WriteString("</" + q + ">")
				continue
			}
			if !top.skip {
				out.WriteString("</" + mpdQName(t.Name) + ">")
			}
		}
	}
	if !sawRoot || len(stack) != 0 {
		return nil, errRelayUnsupported
	}
	return out.Bytes(), nil
}

func writeMPDStart(out *bytes.Buffer, t xml.StartElement) {
	out.WriteString("<" + mpdQName(t.Name))
	for _, a := range t.Attr {
		out.WriteString(" " + mpdQName(a.Name) + `="`)
		_ = xml.EscapeText(out, []byte(a.Value))
		out.WriteString(`"`)
	}
	out.WriteString(">")
}
