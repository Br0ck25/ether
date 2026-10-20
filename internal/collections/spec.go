package collections

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/braedonsaunders/ether/internal/core"
)

// Field, op, sort and source vocabularies.
const (
	MediaMovie = "movie"
	MediaShow  = "show"
	MediaMixed = "mixed"

	MatchAll = "all"
	MatchAny = "any"

	RefreshLive   = "live"
	RefreshHourly = "hourly"
	RefreshDaily  = "daily"
	RefreshWeekly = "weekly"
)

var (
	sourceKinds = map[string]bool{
		"catalog": true, "tmdbList": true, "tmdbCollection": true, "tmdbDiscover": true,
		"tmdbKeyword": true, "tmdbCompany": true, "tmdbNetwork": true, "tmdbPerson": true,
		"watchProvider": true, "traktList": true, "letterboxdList": true, "mdblist": true, "titles": true,
	}
	tmdbLists = map[string]bool{
		"trending_day": true, "trending_week": true, "popular": true, "top_rated": true,
		"now_playing": true, "upcoming": true, "airing_today": true, "on_the_air": true,
	}
	sortModes = map[string]bool{
		"popularity": true, "rating": true, "released": true, "title": true, "added": true,
		"random": true, "personal": true, "source": true,
	}
	artworkModes = map[string]bool{"auto": true, "collage": true, "backdrop": true, "tmdb": true, "custom": true}
	deviceNames  = map[string]bool{core.DeviceTV: true, core.DeviceMobile: true, core.DeviceWeb: true, core.DeviceDesktop: true}
	personRoles  = map[string]bool{"": true, "cast": true, "director": true, "writer": true, "crew": true}
)

// fieldClass groups rule fields by how they are evaluated.
type fieldClass int

const (
	classTags   fieldClass = iota + 1 // multi-valued names: genre, country, language, contentRating
	classRefs                         // TMDB entities by id or name: keyword, person, studio, network, collection, watchProvider
	classNumber                       // year, decade, rating, votes, popularity, runtime
	classDate                         // released, addedAt
	classBool                         // watched, inProgress (per user)
)

var ruleFields = map[string]fieldClass{
	"genre": classTags, "country": classTags, "language": classTags, "contentRating": classTags,
	"keyword": classRefs, "person": classRefs, "studio": classRefs, "network": classRefs,
	"collection": classRefs, "watchProvider": classRefs,
	"year": classNumber, "decade": classNumber, "rating": classNumber, "votes": classNumber,
	"popularity": classNumber, "runtime": classNumber,
	"released": classDate, "addedAt": classDate,
	"watched": classBool, "inProgress": classBool,
}

var classOps = map[fieldClass]map[string]bool{
	classTags:   {"is": true, "isNot": true, "in": true, "notIn": true},
	classRefs:   {"is": true, "isNot": true, "in": true, "notIn": true},
	classNumber: {"is": true, "isNot": true, "in": true, "notIn": true, "gte": true, "lte": true, "between": true},
	classDate:   {"gte": true, "lte": true, "between": true, "within": true},
	classBool:   {"is": true, "isNot": true},
}

// ValidationError lists the problems with a spec, keyed by field path
// ("name", "sources[1].url", "rules[0].value"). The admin API renders it
// as a 400 with per-field messages.
type ValidationError struct {
	Fields map[string]string
}

func (e *ValidationError) Error() string {
	keys := make([]string, 0, len(e.Fields))
	for k := range e.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+": "+e.Fields[k])
	}
	return "invalid collection: " + strings.Join(parts, "; ")
}

// InvalidFields exposes the field errors without importing this package.
func (e *ValidationError) InvalidFields() map[string]string { return e.Fields }

func (e *ValidationError) add(field, msg string) {
	if e.Fields == nil {
		e.Fields = map[string]string{}
	}
	if _, ok := e.Fields[field]; !ok {
		e.Fields[field] = msg
	}
}

var accentRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// Normalize fills defaults in place: an empty source list means the whole
// catalogue, empty match means "all", and so on. It never fails.
func Normalize(s *core.CollectionSpec) {
	s.Name = strings.TrimSpace(s.Name)
	s.Description = strings.TrimSpace(s.Description)
	if s.MediaType == "" {
		s.MediaType = MediaMovie
	}
	if s.MediaType == "tv" {
		s.MediaType = MediaShow
	}
	if len(s.Sources) == 0 {
		s.Sources = []core.CollectionSource{{Kind: "catalog"}}
	}
	for i := range s.Sources {
		src := &s.Sources[i]
		src.URL = strings.TrimSpace(src.URL)
		src.Label = strings.TrimSpace(src.Label)
		src.Region = strings.ToUpper(strings.TrimSpace(src.Region))
		if src.MediaType == "tv" {
			src.MediaType = MediaShow
		}
	}
	if s.Match == "" {
		s.Match = MatchAll
	}
	if s.Rules == nil {
		s.Rules = []core.CollectionRule{}
	}
	if s.Exclude == nil {
		s.Exclude = []core.CollectionRule{}
	}
	if s.Sort == "" {
		s.Sort = "popularity"
		s.SortDesc = true
	}
	if s.Refresh == "" {
		s.Refresh = RefreshDaily
	}
	if s.Artwork.Mode == "" {
		s.Artwork.Mode = "auto"
	}
	s.Artwork.Accent = strings.TrimSpace(s.Artwork.Accent)
	if s.Rotate != nil && s.Rotate.Size <= 0 {
		s.Rotate = nil
	}
	if s.Rotate != nil && s.Rotate.Every == "" {
		s.Rotate.Every = "daily"
	}
	if !s.Targets.Users.All && s.Targets.Users.IDs == nil {
		// The zero value means nobody was chosen explicitly: everyone.
		s.Targets.Users = core.AllUsers()
	}
	if sc := s.Targets.Schedule; sc != nil && len(sc.Days) == 0 && sc.FromHour == nil && sc.ToHour == nil && len(sc.Devices) == 0 {
		s.Targets.Schedule = nil
	}
}

// Validate checks a normalized spec. It returns a *ValidationError.
func Validate(s *core.CollectionSpec) error {
	ve := &ValidationError{}
	if s.Name == "" {
		ve.add("name", "a name is required")
	} else if len(s.Name) > 120 {
		ve.add("name", "keep the name under 120 characters")
	}
	if len(s.Description) > 2000 {
		ve.add("description", "keep the description under 2000 characters")
	}
	switch s.MediaType {
	case MediaMovie, MediaShow, MediaMixed:
	default:
		ve.add("mediaType", `must be "movie", "show" or "mixed"`)
	}
	if len(s.Sources) > 25 {
		ve.add("sources", "at most 25 sources")
	}
	for i, src := range s.Sources {
		validateSource(ve, fmt.Sprintf("sources[%d]", i), src)
	}
	if s.Match != MatchAll && s.Match != MatchAny {
		ve.add("match", `must be "all" or "any"`)
	}
	if len(s.Rules)+len(s.Exclude) > 50 {
		ve.add("rules", "at most 50 rules")
	}
	for i, r := range s.Rules {
		if _, err := compileRule(r); err != nil {
			ve.add(fmt.Sprintf("rules[%d]", i), err.Error())
		}
	}
	for i, r := range s.Exclude {
		if _, err := compileRule(r); err != nil {
			ve.add(fmt.Sprintf("exclude[%d]", i), err.Error())
		}
	}
	if !sortModes[s.Sort] {
		ve.add("sort", "unknown sort "+strconv.Quote(s.Sort))
	}
	if s.Limit < 0 || s.Limit > maxMembers {
		ve.add("limit", fmt.Sprintf("must be between 0 (no limit) and %d", maxMembers))
	}
	switch s.Refresh {
	case RefreshLive, RefreshHourly, RefreshDaily, RefreshWeekly:
	default:
		ve.add("refresh", `must be "live", "hourly", "daily" or "weekly"`)
	}
	if s.Rotate != nil {
		if s.Rotate.Size > 500 {
			ve.add("rotate.size", "rotate at most 500 titles")
		}
		if s.Rotate.Every != "daily" && s.Rotate.Every != "weekly" {
			ve.add("rotate.every", `must be "daily" or "weekly"`)
		}
	}
	if !artworkModes[s.Artwork.Mode] {
		ve.add("artwork.mode", "unknown artwork mode "+strconv.Quote(s.Artwork.Mode))
	}
	if s.Artwork.Accent != "" && !accentRe.MatchString(s.Artwork.Accent) {
		ve.add("artwork.accent", `use a hex colour like "#e50914"`)
	}
	if s.Artwork.Mode == "custom" {
		if !validCustomArt(s.Artwork.URL) {
			ve.add("artwork.url", "use an http(s) image URL or a TMDB image path")
		}
	}
	if sc := s.Targets.Schedule; sc != nil {
		for _, d := range sc.Days {
			if d < 0 || d > 6 {
				ve.add("targets.schedule.days", "days are 0 (Sunday) to 6 (Saturday)")
			}
		}
		for name, h := range map[string]*int{"fromHour": sc.FromHour, "toHour": sc.ToHour} {
			if h != nil && (*h < 0 || *h > 24) {
				ve.add("targets.schedule."+name, "hours are 0 to 24")
			}
		}
		for _, d := range sc.Devices {
			if !deviceNames[d] {
				ve.add("targets.schedule.devices", "devices are tv, mobile, web or desktop")
			}
		}
	}
	if len(ve.Fields) > 0 {
		return ve
	}
	return nil
}

func validCustomArt(u string) bool {
	u = strings.TrimSpace(u)
	if u == "" {
		return false
	}
	if strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://") {
		return len(u) < 2048
	}
	return ValidTMDBImagePath(u)
}

var tmdbPathRe = regexp.MustCompile(`^/[A-Za-z0-9_\-]{1,128}\.(jpg|jpeg|png|webp|svg)$`)

// ValidTMDBImagePath reports whether p looks like a TMDB image path
// ("/abc123.jpg"). It rejects traversal and anything that is not a single
// file name.
func ValidTMDBImagePath(p string) bool { return tmdbPathRe.MatchString(p) }

func validateSource(ve *ValidationError, path string, src core.CollectionSource) {
	if !sourceKinds[src.Kind] {
		ve.add(path+".kind", "unknown source "+strconv.Quote(src.Kind))
		return
	}
	switch src.Kind {
	case "tmdbList":
		if !tmdbLists[src.List] {
			ve.add(path+".list", "unknown TMDB list "+strconv.Quote(src.List))
		}
		if src.MediaType != "" && src.MediaType != MediaMovie && src.MediaType != MediaShow {
			ve.add(path+".mediaType", `must be "movie" or "show"`)
		}
	case "tmdbCollection", "tmdbKeyword", "tmdbCompany", "tmdbNetwork", "tmdbPerson":
		if src.ID <= 0 && src.Label == "" {
			ve.add(path+".id", "choose a TMDB entry (an id or a name to look up)")
		}
		if src.Kind == "tmdbPerson" && !personRoles[src.Role] {
			ve.add(path+".role", "role is cast, director, writer or crew")
		}
	case "watchProvider":
		if src.ID <= 0 && src.Label == "" {
			ve.add(path+".id", "choose a streaming service")
		}
		if src.Region != "" && !regionRe.MatchString(src.Region) {
			ve.add(path+".region", "use a two-letter country code such as US or GB")
		}
	case "tmdbDiscover":
		if len(src.Params) == 0 {
			ve.add(path+".params", "add at least one discover parameter")
		}
		for k := range src.Params {
			if !discoverParamRe.MatchString(k) {
				ve.add(path+".params", "unsupported discover parameter "+strconv.Quote(k))
			}
		}
	case "traktList":
		if _, err := parseTraktURL(src.URL); err != nil {
			ve.add(path+".url", err.Error())
		}
	case "letterboxdList":
		if _, err := parseLetterboxdURL(src.URL); err != nil {
			ve.add(path+".url", err.Error())
		}
	case "mdblist":
		if _, err := parseMDBListURL(src.URL); err != nil {
			ve.add(path+".url", err.Error())
		}
	case "titles":
		if len(src.Keys) == 0 {
			ve.add(path+".keys", "pick at least one title")
		}
		if len(src.Keys) > maxMembers {
			ve.add(path+".keys", fmt.Sprintf("at most %d titles", maxMembers))
		}
		for _, k := range src.Keys {
			rk, err := core.ParseRatingKey(k)
			if err != nil || (rk.Kind() != core.KindMovie && rk.Kind() != core.KindShow) {
				ve.add(path+".keys", "only movies and shows can be hand-picked ("+k+")")
				break
			}
		}
	}
}

var (
	regionRe        = regexp.MustCompile(`^[A-Z]{2}$`)
	discoverParamRe = regexp.MustCompile(`^[a-z_]+(\.(gte|lte))?$`)
)

// cloneSpec deep-copies a spec through JSON (specs are small).
func cloneSpec(s core.CollectionSpec) core.CollectionSpec {
	b, err := json.Marshal(s)
	if err != nil {
		return s
	}
	var out core.CollectionSpec
	if err := json.Unmarshal(b, &out); err != nil {
		return s
	}
	return out
}
