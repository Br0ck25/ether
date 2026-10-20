package core

import (
	"context"
	"time"
)

// Device classes for HubContext.Device.
const (
	DeviceTV      = "tv"
	DeviceMobile  = "mobile"
	DeviceWeb     = "web"
	DeviceDesktop = "desktop"
)

// Dayparts for HubContext.Daypart, in the server's local time.
const (
	DaypartMorning   = "morning"   // 05:00–11:59
	DaypartAfternoon = "afternoon" // 12:00–16:59
	DaypartEvening   = "evening"   // 17:00–21:59
	DaypartLate      = "late"      // 22:00–04:59
)

// HubContext is the situation a Plex client asked for rows in: which kind of
// device, when, and for which section (empty = Home).
type HubContext struct {
	Device    string    `json:"device"`
	Daypart   string    `json:"daypart"`
	Weekend   bool      `json:"weekend"`
	Now       time.Time `json:"now"`
	SectionID int       `json:"sectionId,omitempty"`
	Product   string    `json:"product,omitempty"`  // X-Plex-Product
	Platform  string    `json:"platform,omitempty"` // X-Plex-Platform
	ClientID  string    `json:"clientId,omitempty"`
}

// ContextualRecommender is implemented by recommenders that adapt rows to
// the request context. internal/plex prefers it over Recommender when
// available.
type ContextualRecommender interface {
	HomeHubsFor(ctx context.Context, user *User, hc HubContext, limit int) ([]Hub, error)
	SectionHubsFor(ctx context.Context, user *User, t MediaType, hc HubContext, limit int) ([]Hub, error)
}

// HubImpression is one row as served to a user, for the personalization
// feedback loop.
type HubImpression struct {
	UserID   int64       `json:"userId"`
	HubID    string      `json:"hubId"`
	Position int         `json:"position"`
	Keys     []RatingKey `json:"keys"`
	Context  HubContext  `json:"context"`
	At       time.Time   `json:"at"`
}

// ImpressionSink receives served rows. Implementations must not block.
type ImpressionSink interface {
	RecordImpressions(user *User, hc HubContext, hubs []Hub)
}

// CollectionRow is an owner-designed collection offered as a Home or
// section row for a user in a context (targeting and schedules already
// applied).
type CollectionRow struct {
	Hub          Hub    `json:"hub"`
	CollectionID string `json:"collectionId"`
	HomeRow      bool   `json:"homeRow"`
	SectionRow   bool   `json:"sectionRow"`
}

// CollectionRows is implemented by the collection engine for the
// personalization engine.
type CollectionRows interface {
	HomeRowsFor(ctx context.Context, user *User, hc HubContext) ([]CollectionRow, error)
}

// PersonalScorer scores titles for a user (higher = better fit). The
// collection engine uses it for the "personal" sort.
type PersonalScorer interface {
	ScoreTitles(ctx context.Context, user *User, titles []Title) (map[RatingKey]float64, error)
}

// SectionView restricts a Home hub to one library section's media type,
// so mixed rows that belong on a section too (the watchlist, Rediscover)
// can appear there with only that section's titles. A typed hub passes
// through when its type matches. Recaps (Year in Review) stay on Home.
// ok is false when nothing of the type remains.
func SectionView(h Hub, t MediaType) (Hub, bool) {
	if h.Context == "hub.home.yearinreview" {
		return h, false
	}
	match := func(x MediaType) bool {
		if t == TypeShow {
			return x == TypeShow || x == TypeSeason || x == TypeEpisode
		}
		return x == t
	}
	if h.Type != "" {
		return h, match(h.Type)
	}
	items := make([]Title, 0, len(h.Items))
	for _, it := range h.Items {
		if match(it.Type) {
			items = append(items, it)
		}
	}
	if len(items) == 0 {
		return h, false
	}
	h.Items = items
	h.Type = t
	return h, true
}
