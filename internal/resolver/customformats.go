package resolver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/dlclark/regexp2"
)

// errFormatTimeout reports a custom format regex that exceeded its time
// budget on one release name.
var errFormatTimeout = errors.New("regex timed out")

type FormatMatch struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Score int    `json:"score"`
}

type compiledSpec struct {
	def core.FormatSpecification
	re  *regexp2.Regexp
}
type compiledFormat struct {
	def   core.CustomFormat
	specs []compiledSpec
}
type FormatScorer struct {
	formats []compiledFormat
	scores  map[string]int
}

// NewFormatScorer validates every rule before publishing an immutable scorer.
// .NET lookarounds used by TRaSH are supported; regex execution is time bounded.
func NewFormatScorer(defs []core.CustomFormat, scores map[string]int) (*FormatScorer, error) {
	if len(defs) > 1000 {
		return nil, fmt.Errorf("custom formats: maximum 1000 definitions")
	}
	s := &FormatScorer{scores: map[string]int{}}
	for k, v := range scores {
		s.scores[k] = v
	}
	seen := map[string]bool{}
	for _, d := range defs {
		if d.ID == "" {
			d.ID = d.Name
		}
		if d.Name == "" || seen[d.ID] || len(d.Specifications) == 0 || len(d.Specifications) > 100 {
			return nil, fmt.Errorf("custom format %q: missing name, duplicate id, or invalid specifications", d.Name)
		}
		seen[d.ID] = true
		f := compiledFormat{def: d}
		for _, spec := range d.Specifications {
			c := compiledSpec{def: spec}
			switch spec.Implementation {
			case "ReleaseTitleSpecification", "ReleaseGroupSpecification", "EditionSpecification":
				var pattern string
				if json.Unmarshal(spec.Fields["value"], &pattern) != nil || pattern == "" || len(pattern) > 16384 {
					return nil, fmt.Errorf("custom format %q: invalid regex", d.Name)
				}
				var err error
				c.re, err = regexp2.Compile(pattern, regexp2.IgnoreCase)
				if err != nil {
					return nil, fmt.Errorf("custom format %q: %w", d.Name, err)
				}
				c.re.MatchTimeout = 25 * time.Millisecond
			case "ResolutionSpecification", "SourceSpecification", "QualityModifierSpecification", "SizeSpecification", "LanguageSpecification":
				if spec.Implementation == "SizeSpecification" {
					for _, key := range []string{"min", "max"} {
						if raw, ok := spec.Fields[key]; ok {
							var value float64
							if json.Unmarshal(raw, &value) != nil || value < 0 {
								return nil, fmt.Errorf("custom format %q: invalid size bound", d.Name)
							}
						}
					}
				} else {
					var value int
					if json.Unmarshal(spec.Fields["value"], &value) != nil {
						return nil, fmt.Errorf("custom format %q: numeric native specification value required", d.Name)
					}
					if (spec.Implementation == "LanguageSpecification" && (value <= 0 || value > 18)) || (spec.Implementation == "SourceSpecification" && (value < 0 || value > 9)) || (spec.Implementation == "QualityModifierSpecification" && (value < 0 || value > 5)) {
						return nil, fmt.Errorf("custom format %q: unsupported native enum value %d", d.Name, value)
					}
				}
			default:
				return nil, fmt.Errorf("custom format %q: unsupported specification %q", d.Name, spec.Implementation)
			}
			f.specs = append(f.specs, c)
		}
		s.formats = append(s.formats, f)
	}
	return s, nil
}

// assumedLanguage names a title's original language (ISO 639-1) the way
// release language tags are named; English when unknown.
func assumedLanguage(t *core.Title) string {
	if t == nil || t.Language == "" {
		return "english"
	}
	code := strings.ToLower(t.Language)
	if name, ok := map[string]string{"en": "english", "fr": "french", "es": "spanish", "de": "german", "it": "italian",
		"da": "danish", "nl": "dutch", "ja": "japanese", "is": "icelandic", "zh": "chinese", "ru": "russian", "pl": "polish",
		"vi": "vietnamese", "sv": "swedish", "no": "norwegian", "nb": "norwegian", "nn": "norwegian", "fi": "finnish",
		"tr": "turkish", "pt": "portuguese", "ko": "korean", "hi": "hindi", "te": "telugu", "ta": "tamil", "ar": "arabic",
		"cs": "czech", "hu": "hungarian", "el": "greek", "th": "thai", "id": "indonesian"}[code]; ok {
		return name
	}
	return code
}

func fieldNumber(s core.FormatSpecification, k string) float64 {
	var v float64
	_ = json.Unmarshal(s.Fields[k], &v)
	return v
}
func specMatch(s compiledSpec, r core.Release) (bool, error) {
	value := fieldNumber(s.def, "value")
	switch s.def.Implementation {
	case "ReleaseTitleSpecification":
		return s.re.MatchString(r.Name)
	case "ReleaseGroupSpecification":
		return s.re.MatchString(r.Info.Group)
	case "EditionSpecification":
		return s.re.MatchString(r.Info.Edition)
	case "ResolutionSpecification":
		return r.Info.Height == int(value), nil
	case "SourceSpecification":
		// Radarr's source enum: Unknown=0, CAM=1, TS=2, TC=3, WORKPRINT=4, DVD=5,
		// TV=6, WEB-DL=7, WEBRip=8, BluRay=9.
		source := map[core.Source]int{core.SourceCam: 1, core.SourceScreener: 5, core.SourceDVD: 5, core.SourceHDTV: 6, core.SourceWEBDL: 7, core.SourceWEBRip: 8, core.SourceBluRay: 9, core.SourceRemux: 9}[r.Info.Source]
		return source == int(value), nil
	case "QualityModifierSpecification":
		// Radarr's modifier enum: NONE=0, REGIONAL=1, SCREENER=2, RAWHD=3,
		// BRDISK=4, REMUX=5 (TRaSH's remux formats use 5).
		modifier := 0
		switch r.Info.Source {
		case core.SourceRemux:
			modifier = 5
		case core.SourceScreener:
			modifier = 2
		}
		return modifier == int(value), nil
	case "SizeSpecification":
		if r.Size <= 0 {
			return false, nil
		}
		gib := float64(r.Size) / (1 << 30)
		min, max := fieldNumber(s.def, "min"), fieldNumber(s.def, "max")
		return gib >= min && (max == 0 || gib <= max), nil
	case "LanguageSpecification":
		var name string
		_ = json.Unmarshal(s.def.Fields["value"], &name)
		// Native language enum values (only known values; missing metadata never matches).
		if name == "" {
			name = map[int]string{1: "english", 2: "french", 3: "spanish", 4: "german", 5: "italian", 6: "danish", 7: "dutch", 8: "japanese", 9: "icelandic", 10: "chinese", 11: "russian", 12: "polish", 13: "vietnamese", 14: "swedish", 15: "norwegian", 16: "finnish", 17: "turkish", 18: "portuguese"}[int(value)]
		}
		for _, l := range r.Info.Languages {
			if name != "" && strings.EqualFold(l, name) {
				return true, nil
			}
		}
		return false, nil
	}
	return false, nil
}

// Score mirrors Starr grouping: every specification type must match; within
// a type all required rules and at least one rule must match.
func (s *FormatScorer) Score(r core.Release) (int, []FormatMatch, error) {
	return s.ScoreWithScores(r, nil)
}

func (s *FormatScorer) ScoreWithScores(r core.Release, overrides map[string]int) (int, []FormatMatch, error) {
	if s == nil {
		return 0, nil, nil
	}
	total := 0
	var matches []FormatMatch
	for _, f := range s.formats {
		type group struct {
			requiredOK bool
			anyOK      bool
		}
		groups := map[string]*group{}
		for _, spec := range f.specs {
			g := groups[spec.def.Implementation]
			if g == nil {
				g = &group{requiredOK: true}
				groups[spec.def.Implementation] = g
			}
			match, err := specMatch(spec, r)
			if err != nil {
				return 0, nil, fmt.Errorf("custom format %q: %w", f.def.Name, errFormatTimeout)
			}
			if spec.def.Negate {
				match = !match
			}
			g.anyOK = g.anyOK || match
			if spec.def.Required {
				g.requiredOK = g.requiredOK && match
			}
		}
		ok := true
		for _, g := range groups {
			if !g.requiredOK || !g.anyOK {
				ok = false
			}
		}
		if !ok {
			continue
		}
		score := f.def.Scores["default"]
		if v, ok := s.scores[f.def.ID]; ok {
			score = v
		}
		if v, ok := overrides[f.def.ID]; ok {
			score = v
		}
		total += score
		matches = append(matches, FormatMatch{f.def.ID, f.def.Name, score})
	}
	return total, matches, nil
}

// ImportCustomFormats accepts a native single definition or an array, validating
// before returning. Callers persist the result in config.Resolver.CustomFormats.
func ImportCustomFormats(data []byte) ([]core.CustomFormat, error) {
	if len(data) > 8<<20 {
		return nil, fmt.Errorf("custom formats: input exceeds 8 MiB")
	}
	data = bytes.TrimSpace(data)
	var defs []core.CustomFormat
	if len(data) > 0 && data[0] == '[' {
		if err := json.Unmarshal(data, &defs); err != nil {
			return nil, err
		}
	} else {
		var d core.CustomFormat
		if err := json.Unmarshal(data, &d); err != nil {
			return nil, err
		}
		defs = []core.CustomFormat{d}
	}
	_, err := NewFormatScorer(defs, nil)
	return defs, err
}

// FetchFormatJSON conditionally refreshes an owner-selected JSON resource.
// The last valid download is cached atomically, and remains usable offline.
func FetchFormatJSON(ctx context.Context, cl *http.Client, rawURL, cachePath string) ([]byte, error) {
	if cl == nil {
		cl = &http.Client{Timeout: 20 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := cl.Do(req)
	if err != nil {
		if data, e := os.ReadFile(cachePath); e == nil {
			return data, nil
		}
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("custom formats: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (8<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 8<<20 {
		return nil, fmt.Errorf("custom formats: response too large")
	}
	if !json.Valid(data) {
		return nil, fmt.Errorf("custom formats: invalid JSON")
	}
	if cachePath != "" {
		if err = os.MkdirAll(filepath.Dir(cachePath), 0700); err != nil {
			return nil, err
		}
		f, e := os.CreateTemp(filepath.Dir(cachePath), ".formats-*")
		if e != nil {
			return nil, e
		}
		tmp := f.Name()
		defer os.Remove(tmp)
		_, e = f.Write(data)
		ce := f.Close()
		if e != nil {
			return nil, e
		}
		if ce != nil {
			return nil, ce
		}
		if e = os.Rename(tmp, cachePath); e != nil {
			return nil, e
		}
	}
	return data, nil
}
