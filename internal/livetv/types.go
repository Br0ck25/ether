// Package livetv implements owner-defined virtual channels and IPTV playout.
// Ether supplies neither programming nor IPTV accounts.
package livetv

import (
	"context"
	"database/sql"
	"net/http"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

const Provider = "tv.plex.providers.epg.xmltv:1"

type Item struct {
	Key        core.RatingKey `json:"key"`
	Title      string         `json:"title"`
	DurationMS int64          `json:"durationMs"`
	Summary    string         `json:"summary,omitempty"`
	Rating     string         `json:"rating,omitempty"`
	Thumb      string         `json:"thumb,omitempty"`
}
type Block struct {
	// Start and End are UTC minutes after midnight. End may be 1440.
	Start   int    `json:"start"`
	End     int    `json:"end"`
	Items   []Item `json:"items"`
	Shuffle bool   `json:"shuffle"`
}
type Channel struct {
	Transcode     bool           `json:"transcode"` // IPTV: normalize codecs through the transcoder
	ID            string         `json:"id"`
	Number        string         `json:"number"`
	Name          string         `json:"name"`
	Kind          string         `json:"kind"` // virtual or iptv
	Enabled       bool           `json:"enabled"`
	Favorite      bool           `json:"favorite"`
	Logo          string         `json:"logo,omitempty"`
	Rating        string         `json:"rating,omitempty"`
	AllowedUsers  []int64        `json:"allowedUsers,omitempty"`
	SourceID      string         `json:"sourceId,omitempty"`
	GuideID       string         `json:"guideId,omitempty"`
	StreamURL     string         `json:"streamUrl,omitempty"`     // owner API only
	StreamSources []StreamSource `json:"streamSources,omitempty"` // ordered inputs; independent of channel/guide identity
	Group         string         `json:"group,omitempty"`
	Epoch         time.Time      `json:"epoch"`
	Seed          int64          `json:"seed"`
	Shuffle       bool           `json:"shuffle"`
	Items         []Item         `json:"items"`
	Blocks        []Block        `json:"blocks,omitempty"`
	// Collection/Filter are expanded into a stable item snapshot when saved.
	Collection core.RatingKey `json:"collection,omitempty"`
	Filter     *core.Filter   `json:"filter,omitempty"`

	// GuideSourceID names the source whose XMLTV supplies GuideID's
	// programmes. Empty means SourceID. Virtual channels ignore it.
	GuideSourceID string `json:"guideSourceId,omitempty"`
}
type Source struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	PlaylistURL string `json:"playlistUrl,omitempty"`
	GuideURL    string `json:"guideUrl,omitempty"`
	// Inline uploads are supplied on import and are not returned by GET.
	Playlist  string    `json:"playlist,omitempty"`
	Guide     string    `json:"guide,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
	// RefreshHours caps the time between automatic refreshes of URL-backed
	// sources: 0 uses the 6 hour default, a negative value means manual
	// refresh only. Sources also refresh before mapped listings run out.
	// Inline uploads never refresh automatically.
	RefreshHours int `json:"refreshHours,omitempty"`
	// LastError/LastErrorAt/RefreshFailures record failed refreshes; a
	// successful import clears them. Existing data is kept on failure.
	LastError       string     `json:"lastError,omitempty"`
	LastErrorAt     *time.Time `json:"lastErrorAt,omitempty"`
	RefreshFailures int        `json:"refreshFailures,omitempty"`
	// Read-only statistics computed by Sources(); ignored on import.
	HasPlaylist       bool       `json:"hasPlaylist"`
	HasGuide          bool       `json:"hasGuide"`
	ChannelCount      int        `json:"channelCount"`
	GuideChannelCount int        `json:"guideChannelCount"`
	ProgrammeCount    int        `json:"programmeCount"`
	GuideFrom         *time.Time `json:"guideFrom,omitempty"`
	GuideUntil        *time.Time `json:"guideUntil,omitempty"`
	NextRefresh       *time.Time `json:"nextRefresh,omitempty"`
}
type Programme struct {
	ID        string    `json:"id"`
	ChannelID string    `json:"channelId"`
	Start     time.Time `json:"start"`
	End       time.Time `json:"end"`
	Item      Item      `json:"item"`
}

func (p Programme) Offset(at time.Time) time.Duration {
	if at.Before(p.Start) {
		return 0
	}
	if !at.Before(p.End) {
		return p.End.Sub(p.Start)
	}
	return at.Sub(p.Start)
}

type Policy struct {
	UserID    int64    `json:"userId"`
	Disabled  bool     `json:"disabled"`
	Channels  []string `json:"channels,omitempty"` // empty permits all
	Favorites []string `json:"favorites,omitempty"`
	MaxRating string   `json:"maxRating,omitempty"`
}
type Options struct {
	CanPlay     func(*core.User, bool, time.Time) bool
	StreamLimit func(*core.User) int
	DB          *sql.DB
	Catalog     core.Catalog
	Collections core.Collections
	Playback    core.Playback
	Transcoder  core.Transcoder
	HTTPClient  *http.Client
	Now         func() time.Time
	// Allowed applies additional owner title/genre policies to every surface.
	Allowed func(*core.User, *core.Title) bool
	// Warm resolves/primes a title without opening a viewer stream.
	Warm func(context.Context, core.RatingKey) error
}
type SessionInfo struct {
	ActiveSourceID string `json:"activeSourceId,omitempty"`
	// Plex's rolling subscription identity is numeric; the stream UUID remains
	// separate. These are ephemeral identities, scoped to this service lifetime.
	SubscriptionID  int64           `json:"-"`
	ActivityID      string          `json:"-"`
	ClientSessionID string          `json:"-"`
	MediaInfo       *core.MediaInfo `json:"-"` // Actual source analysis for native clients.
	ID              string          `json:"id"`
	UserID          int64           `json:"userId"`
	Channel         Channel         `json:"channel"`
	Programme       *Programme      `json:"programme,omitempty"`
	StartedAt       time.Time       `json:"startedAt"`
}
