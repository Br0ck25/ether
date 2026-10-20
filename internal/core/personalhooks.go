package core

import (
	"context"
	"time"
)

// Netflix-style per-viewer hooks (roadmap items #30–#32). All three are
// optional capabilities: internal/plex and internal/prefetch reach them by
// type assertion on the recommender / their deps and behave exactly as
// before when they are absent. Implemented by internal/personalize.

// PostPlayPick is one ranked post-play recommendation with its explained
// score components (item #30): Score = taste factor × similarity factor ×
// runtime factor (× a continuation bonus for the franchise's next entry),
// where TasteFit is the raw taste fit (-1..1), Similarity
// the feature overlap with the just-watched title (0..1) and RuntimeFit how
// well the runtime fits the viewer's remaining time tonight (0..1).
type PostPlayPick struct {
	Title      Title   `json:"title"`
	Score      float64 `json:"score"`
	TasteFit   float64 `json:"tasteFit"`
	Similarity float64 `json:"similarity"`
	RuntimeFit float64 `json:"runtimeFit"`
	Reason     string  `json:"reason,omitempty"`
}

// PostPlayer ranks a personalized post-play "hook queue" after a title.
// Implementations cache the ranking per user and just-watched title for the
// session, so the pick pre-buffered during playback is the pick the
// post-play endpoint returns first.
type PostPlayer interface {
	PostPlay(ctx context.Context, user *User, justWatched RatingKey, hc HubContext, limit int) ([]PostPlayPick, error)
}

// MicroReason is the per-viewer decision header for a detail page
// (item #31): a calibrated match percentage, the strongest taste reason
// and the local finish time, e.g. "96% Match · Directed by Denis
// Villeneuve · Ends at 10:45 PM". Parts that cannot be supported honestly
// (low taste confidence, unknown runtime) are left empty.
type MicroReason struct {
	Header       string    `json:"header,omitempty"`
	MatchPercent int       `json:"matchPercent,omitempty"`
	HasMatch     bool      `json:"hasMatch,omitempty"`
	Reason       string    `json:"reason,omitempty"`
	EndsAt       time.Time `json:"endsAt,omitempty"`
	HasEnd       bool      `json:"hasEnd,omitempty"`
}

// Empty reports whether there is nothing worth showing.
func (m MicroReason) Empty() bool { return m.Header == "" }

// MicroReasoner builds a MicroReason for one viewer and title. The title
// is only read, never mutated, so a shared catalogue record can never leak
// one viewer's header to another.
type MicroReasoner interface {
	MicroReason(ctx context.Context, user *User, t *Title, now time.Time) MicroReason
}

// IntentTracker receives live in-session browsing signals (item #32):
// single-item detail opens that do not turn into playback. Signals are
// in-memory only and decay over ~45 minutes. Implementations must not
// block the caller for long.
type IntentTracker interface {
	// RecordDetailOpen notes that a viewer opened a title's detail page.
	RecordDetailOpen(ctx context.Context, user *User, t *Title)
	// NotePlayback notes that playback of key started; an open for the
	// same title moments earlier was a conversion, not browsing intent.
	NotePlayback(user *User, key RatingKey)
}
