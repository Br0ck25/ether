package collections

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/llm"
)

// Chatter is the LLM the generator drafts specs with; *llm.Client
// satisfies it.
type Chatter interface {
	Enabled() bool
	Chat(ctx context.Context, msgs []llm.Message, jsonMode bool) (string, error)
}

var _ Chatter = (*llm.Client)(nil)

// ErrLLMDisabled is returned by Generate when no LLM is configured.
var ErrLLMDisabled error = notConfigured{errors.New("the LLM is not enabled (Settings → LLM)")}

type notConfigured struct{ error }

func (notConfigured) NotConfigured() bool { return true }

// upstreamError marks a model that answered with nothing usable (502).
type upstreamError struct{ error }

func (upstreamError) Upstream() bool { return true }

const generatorSystem = `You turn a request for a film/TV collection into a JSON collection spec for a media server. Reply with one JSON object only, no prose:
{"spec": <spec>, "explanation": "<one or two sentences on how the rules capture the request>"}

<spec> fields:
- name (short, like a streaming-service row title), description (one sentence)
- mediaType: "movie" | "show" | "mixed"
- sources: list, usually [{"kind":"catalog"}] (the whole library). Others:
  {"kind":"tmdbPerson","label":"<person name>","role":"director"|"cast"|"writer"} (a filmography)
  {"kind":"tmdbCollection","label":"<TMDB franchise collection name>"}
  {"kind":"tmdbKeyword","label":"<TMDB keyword>"}, {"kind":"tmdbCompany","label":"<studio>"}, {"kind":"tmdbNetwork","label":"<TV network>"}
  {"kind":"watchProvider","label":"<streaming service>"}
  {"kind":"tmdbList","list":"trending_week"|"popular"|"top_rated"|"now_playing"|"upcoming"|"airing_today"|"on_the_air","mediaType":"movie"|"show"}
- match: "all" | "any"; rules: list of {"field","op","value"}; exclude: list of rules to remove
- fields and value types:
  genre (TMDB genre names: Action, Adventure, Animation, Comedy, Crime, Documentary, Drama, Family, Fantasy, History, Horror, Music, Mystery, Romance, Science Fiction, Thriller, War, Western), keyword (TMDB keyword names), person (names), studio, network, country (ISO codes like "KR"), language (ISO 639-1 like "ko"), contentRating ("PG-13"…)
     ops: is (has all), in (any of), isNot / notIn (none of); value: a string or a list of strings
  year, decade (e.g. 1980), rating (TMDB 0-10), votes (vote count), popularity, runtime (minutes)
     ops: gte, lte, between ([lo, hi]), is
  released: ops within ("90d", "2y") or gte/lte/between with "YYYY-MM-DD" or offsets like "-30d", "+90d"
  watched, inProgress: op is, value true/false (per viewer)
- sort: popularity | rating | released | title | random | personal; sortDesc: true for highest/newest first
- limit: number of titles (0 = all); use 50-100 unless the request implies otherwise
Critically acclaimed means rating >= 7.5 with votes >= 500. Prefer rules over keywords when genres or numbers express the request.`

// Generate drafts a spec from a plain-English request with the LLM,
// resolves the people, keywords and companies it names to TMDB ids,
// validates it, and explains it.
func (s *Service) Generate(ctx context.Context, prompt, mediaType string) (*core.CollectionSpec, string, error) {
	chat := s.chatDep()
	if chat == nil || !chat.Enabled() {
		return nil, "", ErrLLMDisabled
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return nil, "", &ValidationError{Fields: map[string]string{"prompt": "describe the collection you want"}}
	}
	if len(prompt) > 1000 {
		prompt = prompt[:1000]
	}
	user := "Request: " + prompt + "\nToday is " + s.now().Format("2006-01-02") + "."
	switch mediaType {
	case MediaMovie, MediaShow, MediaMixed:
		user += " Media type: " + mediaType + "."
	}
	msgs := []llm.Message{{Role: "system", Content: generatorSystem}, {Role: "user", Content: user}}
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		out, err := chat.Chat(ctx, msgs, true)
		if err != nil {
			return nil, "", upstreamError{fmt.Errorf("LLM: %w", err)}
		}
		sp, explanation, notes, perr := s.parseDraft(ctx, out, mediaType)
		if perr == nil {
			if len(notes) > 0 {
				explanation = strings.TrimSpace(explanation + "\n\n" + strings.Join(notes, "\n"))
			}
			return sp, explanation, nil
		}
		lastErr = perr
		msgs = append(msgs, llm.Message{Role: "assistant", Content: truncate(out, 4000)},
			llm.Message{Role: "user", Content: "That reply could not be used (" + perr.Error() + "). Reply again with only the JSON object described."})
	}
	return nil, "", upstreamError{fmt.Errorf("the model's draft was not usable: %w", lastErr)}
}

// draftSpec is the lenient shape models produce.
type draftSpec struct {
	Name        string            `json:"name"`
	Title       string            `json:"title"`
	Description string            `json:"description"`
	MediaType   string            `json:"mediaType"`
	Sources     []json.RawMessage `json:"sources"`
	Match       string            `json:"match"`
	Rules       []json.RawMessage `json:"rules"`
	Exclude     []json.RawMessage `json:"exclude"`
	Sort        string            `json:"sort"`
	SortDesc    *bool             `json:"sortDesc"`
	Limit       json.Number       `json:"limit"`
}

func (s *Service) parseDraft(ctx context.Context, content, mediaType string) (*core.CollectionSpec, string, []string, error) {
	js := extractObject(content)
	if js == "" {
		return nil, "", nil, errors.New("no JSON object in the reply")
	}
	var env map[string]json.RawMessage
	if err := json.Unmarshal([]byte(js), &env); err != nil {
		return nil, "", nil, fmt.Errorf("invalid JSON: %v", err)
	}
	explanation := ""
	if raw, ok := env["explanation"]; ok {
		_ = json.Unmarshal(raw, &explanation)
	}
	specRaw := json.RawMessage(js)
	for _, k := range []string{"spec", "collection", "collectionSpec"} {
		if raw, ok := env[k]; ok {
			specRaw = raw
			break
		}
	}
	var d draftSpec
	dec := json.NewDecoder(strings.NewReader(string(specRaw)))
	dec.UseNumber()
	if err := dec.Decode(&d); err != nil {
		return nil, "", nil, fmt.Errorf("spec is not an object: %v", err)
	}
	var notes []string
	sp := core.CollectionSpec{
		Name: firstNonEmpty(d.Name, d.Title), Description: d.Description, MediaType: strings.ToLower(d.MediaType),
		Match: strings.ToLower(d.Match), Sort: d.Sort, Refresh: RefreshDaily, Enabled: true,
		Artwork: core.CollectionArtwork{Mode: "auto"},
		Targets: core.CollectionTargets{CollectionsTab: true, Users: core.AllUsers()},
	}
	if sp.Name == "" {
		return nil, "", nil, errors.New("the spec has no name")
	}
	switch mediaType {
	case MediaMovie, MediaShow, MediaMixed:
		sp.MediaType = mediaType
	}
	if sp.MediaType == "tv" || sp.MediaType == "series" {
		sp.MediaType = MediaShow
	}
	if sp.MediaType != MediaMovie && sp.MediaType != MediaShow && sp.MediaType != MediaMixed {
		sp.MediaType = MediaMovie
	}
	if sp.Match != MatchAny {
		sp.Match = MatchAll
	}
	if !sortModes[sp.Sort] || sp.Sort == "source" {
		sp.Sort = "popularity"
	}
	sp.SortDesc = sp.Sort != "title"
	if d.SortDesc != nil {
		sp.SortDesc = *d.SortDesc
	}
	if n, err := d.Limit.Int64(); err == nil && n > 0 {
		sp.Limit = int(min(n, maxMembers))
	}
	for i, raw := range d.Sources {
		var src core.CollectionSource
		if err := json.Unmarshal(raw, &src); err != nil {
			notes = append(notes, fmt.Sprintf("Dropped source %d: not understood.", i+1))
			continue
		}
		if src.Label == "" {
			var named struct {
				Name string `json:"name"`
			}
			_ = json.Unmarshal(raw, &named)
			src.Label = named.Name
		}
		src.URL, src.Keys = "", nil // never let a model point at URLs or pick keys
		if src.Kind == "titles" || src.Kind == "traktList" || src.Kind == "letterboxdList" || src.Kind == "mdblist" {
			notes = append(notes, fmt.Sprintf("Dropped a %s source; add lists yourself.", src.Kind))
			continue
		}
		src.Label = strings.TrimSpace(src.Label)
		src.Region = strings.ToUpper(strings.TrimSpace(src.Region))
		if src.MediaType == "tv" {
			src.MediaType = MediaShow
		}
		src.Role = strings.ToLower(src.Role)
		ve := &ValidationError{}
		validateSource(ve, "source", src)
		if len(ve.Fields) > 0 {
			notes = append(notes, fmt.Sprintf("Dropped source %q: %s", firstNonEmpty(src.Label, src.Kind), firstField(ve)))
			continue
		}
		sp.Sources = append(sp.Sources, src)
	}
	convertRules := func(raws []json.RawMessage, what string) []core.CollectionRule {
		out := []core.CollectionRule{}
		for i, raw := range raws {
			var r core.CollectionRule
			if err := json.Unmarshal(raw, &r); err != nil {
				notes = append(notes, fmt.Sprintf("Dropped %s %d: not understood.", what, i+1))
				continue
			}
			r.Op = normalizeOp(r.Op)
			if _, err := compileRule(r); err != nil {
				notes = append(notes, fmt.Sprintf("Dropped %s %q: %v.", what, r.Field, err))
				continue
			}
			out = append(out, r)
		}
		return out
	}
	sp.Rules = convertRules(d.Rules, "rule")
	sp.Exclude = convertRules(d.Exclude, "exclusion")
	Normalize(&sp)

	// Resolve what the model named to TMDB ids so the spec is concrete.
	rctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	kept := sp.Sources[:0]
	for _, src := range sp.Sources {
		entity := map[string]string{"tmdbCollection": "collection", "tmdbKeyword": "keyword", "tmdbCompany": "company",
			"tmdbNetwork": "network", "tmdbPerson": "person", "watchProvider": "watchProvider"}[src.Kind]
		if entity == "" || src.ID > 0 || s.api.c == nil {
			kept = append(kept, src)
			continue
		}
		r, err := s.names.resolve(rctx, entity, ref{Name: src.Label}, s.region(src.Region))
		if err != nil || r.ID <= 0 {
			notes = append(notes, fmt.Sprintf("Couldn't find %q on TMDB, so that source was dropped.", src.Label))
			continue
		}
		if !strings.EqualFold(normName(r.Name), normName(src.Label)) {
			notes = append(notes, fmt.Sprintf("Matched %q to %q on TMDB.", src.Label, r.Name))
		}
		src.ID, src.Label = r.ID, r.Name
		kept = append(kept, src)
	}
	sp.Sources = kept
	if len(sp.Sources) == 0 {
		sp.Sources = []core.CollectionSource{{Kind: "catalog"}}
	}
	if s.api.c != nil {
		for i := range sp.Rules {
			sp.Rules[i] = s.resolveRuleValue(rctx, sp.Rules[i], &notes)
		}
		for i := range sp.Exclude {
			sp.Exclude[i] = s.resolveRuleValue(rctx, sp.Exclude[i], &notes)
		}
	}
	if err := Validate(&sp); err != nil {
		return nil, "", nil, err
	}
	return &sp, strings.TrimSpace(explanation), notes, nil
}

// resolveRuleValue rewrites name values of entity rules (person, keyword,
// studio, network, collection, watchProvider) to {id, name} objects.
func (s *Service) resolveRuleValue(ctx context.Context, r core.CollectionRule, notes *[]string) core.CollectionRule {
	kind := map[string]string{"keyword": "keyword", "person": "person", "studio": "company", "network": "network",
		"collection": "collection", "watchProvider": "watchProvider"}[r.Field]
	if kind == "" {
		return r
	}
	refs, region, err := refList(r.Value)
	if err != nil {
		return r
	}
	out := make([]map[string]any, 0, len(refs))
	var labels []string
	for _, rf := range refs {
		got, err := s.names.resolve(ctx, kind, rf, s.region(region))
		if err != nil {
			*notes = append(*notes, fmt.Sprintf("Couldn't find the %s %q on TMDB.", r.Field, firstNonEmpty(rf.Name, strconv.FormatInt(rf.ID, 10))))
			got = rf
		}
		m := map[string]any{}
		if got.ID > 0 {
			m["id"] = got.ID
		}
		if got.Name != "" {
			m["name"] = got.Name
			labels = append(labels, got.Name)
		}
		if rf.Role != "" {
			m["role"] = rf.Role
		}
		out = append(out, m)
	}
	var v any = out
	if len(out) == 1 && (r.Op == "is" || r.Op == "isNot") {
		v = out[0]
	}
	b, err := json.Marshal(v)
	if err != nil {
		return r
	}
	r.Value = b
	if r.Label == "" {
		r.Label = strings.Join(labels, ", ")
	}
	return r
}

func normalizeOp(op string) string {
	switch strings.ToLower(strings.TrimSpace(op)) {
	case "is", "=", "==", "eq", "equals", "has", "contains":
		return "is"
	case "isnot", "!=", "not", "ne":
		return "isNot"
	case "in", "any", "anyof":
		return "in"
	case "notin", "noneof", "none":
		return "notIn"
	case "gte", ">=", "min", "atleast":
		return "gte"
	case "lte", "<=", "max", "atmost":
		return "lte"
	case "between", "range":
		return "between"
	case "within", "last":
		return "within"
	}
	return op
}

func firstField(ve *ValidationError) string {
	for _, v := range ve.Fields {
		return v
	}
	return "invalid"
}

var trailingComma = regexp.MustCompile(`,(\s*[}\]])`)

// extractObject finds the first balanced JSON object in model output,
// tolerating code fences, prose and trailing commas.
func extractObject(s string) string {
	s = llm.StripReasoning(s)
	if i := strings.Index(s, "```"); i >= 0 {
		rest := s[i+3:]
		rest = strings.TrimPrefix(rest, "json")
		if j := strings.Index(rest, "```"); j >= 0 {
			s = rest[:j]
		}
	}
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return ""
	}
	depth, inStr, esc := 0, false, false
	for i := start; i < len(s); i++ {
		c := s[i]
		switch {
		case esc:
			esc = false
		case c == '\\' && inStr:
			esc = true
		case c == '"':
			inStr = !inStr
		case inStr:
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				out := s[start : i+1]
				if !json.Valid([]byte(out)) {
					out = trailingComma.ReplaceAllString(out, "$1")
				}
				return out
			}
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
