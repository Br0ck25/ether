package core

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// CollectionSpec is an owner-designed collection: candidates gathered from
// Sources, filtered by Rules, sorted, limited and published to Plex.
type CollectionSpec struct {
	ID          string             `json:"id,omitempty"` // server-assigned
	Name        string             `json:"name"`
	Description string             `json:"description,omitempty"`
	MediaType   string             `json:"mediaType"` // "movie" | "show" | "mixed"
	Sources     []CollectionSource `json:"sources"`
	Match       string             `json:"match"` // "all" | "any"
	Rules       []CollectionRule   `json:"rules"`
	Exclude     []CollectionRule   `json:"exclude"`
	// Sort: popularity | rating | released | title | added | random | personal | source.
	Sort       string            `json:"sort"`
	SortDesc   bool              `json:"sortDesc"`
	Limit      int               `json:"limit"`   // 0 = no limit
	Refresh    string            `json:"refresh"` // live | hourly | daily | weekly
	Rotate     *CollectionRotate `json:"rotate,omitempty"`
	Artwork    CollectionArtwork `json:"artwork"`
	Targets    CollectionTargets `json:"targets"`
	Enabled    bool              `json:"enabled"`
	LocalOnly  bool              `json:"localOnly,omitempty"` // evaluate rules against the local catalogue only; no remote discovery
	TemplateID string            `json:"templateId,omitempty"`
	// Server-maintained.
	UpdatedAt   *time.Time `json:"updatedAt,omitempty"`
	LastBuiltAt *time.Time `json:"lastBuiltAt,omitempty"`
	ItemCount   int        `json:"itemCount"`

	// Additive fields beyond the contract (read-only for clients).
	BuiltIn   bool     `json:"builtIn,omitempty"`   // shipped with Ether; only targets/artwork/rotate/enabled are editable
	RatingKey string   `json:"ratingKey,omitempty"` // the Plex ratingKey of the published collection
	Warnings  []string `json:"warnings,omitempty"`  // from the last build
	// OwnerUserID is set for private collections created by a Plex user
	// (core.Collections.CreateSmart); 0 = server-wide.
	OwnerUserID int64 `json:"ownerUserId,omitempty"`
}

// CollectionSource is one place candidates come from. Kind selects which
// other fields apply:
//
//	catalog                                   the whole browsable catalogue
//	tmdbList        List, MediaType           trending_day|trending_week|popular|top_rated|now_playing|upcoming|airing_today|on_the_air
//	tmdbCollection  ID, Label                 a TMDB franchise
//	tmdbDiscover    Params                    raw /discover parameters (names allowed for ids)
//	tmdbKeyword | tmdbCompany | tmdbNetwork | tmdbPerson   ID, Label (Role for people)
//	watchProvider   ID, Region, Label         what's streaming on a service
//	traktList | letterboxdList | mdblist      URL
//	stremioCatalog  URL, Params(catalog, type) a Stremio addon's catalog, e.g. My Lists Addon
//	titles          Keys                      hand-picked rating keys
//
// When ID is 0 the Label is resolved to a TMDB id by name.
type CollectionSource struct {
	Kind      string            `json:"kind"`
	List      string            `json:"list,omitempty"`
	MediaType string            `json:"mediaType,omitempty"`
	ID        int64             `json:"id,omitempty"`
	Label     string            `json:"label,omitempty"`
	Params    map[string]string `json:"params,omitempty"`
	Region    string            `json:"region,omitempty"`
	URL       string            `json:"url,omitempty"`
	Keys      []string          `json:"keys,omitempty"`
	// Role narrows tmdbPerson credits: "cast", "director", "writer",
	// "crew" or "" (every credit). Additive to the contract.
	Role string `json:"role,omitempty"`
}

// CollectionRule is one condition. Value is typed by Field; Label is display
// text for ids.
type CollectionRule struct {
	Field string          `json:"field"`
	Op    string          `json:"op"`
	Value json.RawMessage `json:"value"`
	Label string          `json:"label,omitempty"`
}

// CollectionRotate shows a rotating subset of the members.
type CollectionRotate struct {
	Size  int    `json:"size"`
	Every string `json:"every"` // "daily" | "weekly"
}

// CollectionArtwork selects the poster Plex shows for the collection.
type CollectionArtwork struct {
	Mode   string `json:"mode"` // auto | collage | backdrop | tmdb | custom
	URL    string `json:"url,omitempty"`
	Accent string `json:"accent,omitempty"` // "#rrggbb"
}

// CollectionTargets says where and for whom the collection is published.
type CollectionTargets struct {
	CollectionsTab bool                `json:"collectionsTab"`
	HomeRow        bool                `json:"homeRow"`
	SectionRow     bool                `json:"sectionRow"`
	Users          UserTargets         `json:"users"`
	Schedule       *CollectionSchedule `json:"schedule,omitempty"`
}

// CollectionSchedule limits when rows appear. Days use 0 = Sunday … 6 =
// Saturday; hours are local, FromHour inclusive and ToHour exclusive,
// wrapping past midnight when FromHour > ToHour.
type CollectionSchedule struct {
	Days     []int    `json:"days,omitempty"`
	FromHour *int     `json:"fromHour,omitempty"`
	ToHour   *int     `json:"toHour,omitempty"`
	Devices  []string `json:"devices,omitempty"`
}

// UserTargets is either every user ("all" in JSON) or a list of local user
// ids.
type UserTargets struct {
	All bool
	IDs []int64
}

// AllUsers targets everyone.
func AllUsers() UserTargets { return UserTargets{All: true} }

// Includes reports whether a user (nil = anonymous) is targeted.
func (u UserTargets) Includes(user *User) bool {
	if u.All {
		return true
	}
	if user == nil {
		return false
	}
	for _, id := range u.IDs {
		if id == user.ID {
			return true
		}
	}
	return false
}

func (u UserTargets) MarshalJSON() ([]byte, error) {
	if u.All {
		return []byte(`"all"`), nil
	}
	ids := u.IDs
	if ids == nil {
		ids = []int64{}
	}
	return json.Marshal(ids)
}

func (u *UserTargets) UnmarshalJSON(b []byte) error {
	*u = UserTargets{}
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		if s == "all" || s == "" {
			u.All = true
			return nil
		}
		return fmt.Errorf(`users: want "all" or a list of user ids, got %q`, s)
	}
	if string(b) == "null" {
		u.All = true
		return nil
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return fmt.Errorf(`users: want "all" or a list of user ids`)
	}
	for _, r := range raw {
		var n int64
		if err := json.Unmarshal(r, &n); err == nil {
			u.IDs = append(u.IDs, n)
			continue
		}
		var str string
		if err := json.Unmarshal(r, &str); err == nil {
			if n, err := strconv.ParseInt(str, 10, 64); err == nil {
				u.IDs = append(u.IDs, n)
				continue
			}
		}
		return fmt.Errorf("users: %s is not a user id", string(r))
	}
	if u.IDs == nil {
		u.IDs = []int64{}
	}
	return nil
}

// CollectionTemplate is a curated starting point in the Collection Studio.
type CollectionTemplate struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Category    string         `json:"category"`
	Spec        CollectionSpec `json:"spec"`
}

// CollectionPreview is the result of evaluating an unsaved spec.
type CollectionPreview struct {
	Total    int      `json:"total"`
	Items    []Title  `json:"items"`
	Warnings []string `json:"warnings"`
	TookMs   int64    `json:"tookMs"`
}

// LookupResult is one TMDB entity (person, keyword, company, network,
// collection, watch provider) found by name for the collection builder.
type LookupResult struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Subtitle  string `json:"subtitle,omitempty"`
	ImagePath string `json:"imagePath,omitempty"` // TMDB image path
}
