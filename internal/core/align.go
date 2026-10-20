package core

import (
	"context"
	"time"
)

// AlignRequest names two releases of the same title whose timelines should
// be matched (for example the failing and the replacement release of a
// mid-stream failover, or a marker source's reference cut and the release
// being played).
type AlignRequest struct {
	Key          RatingKey     `json:"key"`
	From         Release       `json:"from"`
	To           Release       `json:"to"`
	FromDuration time.Duration `json:"fromDuration,omitempty"`
	ToDuration   time.Duration `json:"toDuration,omitempty"`
}

// TimelineAligner maps a moment in one release to the same moment in another
// release of the title using dialogue (subtitle) timing rather than video, so
// nothing has to be downloaded. The mapping may be piecewise (different cuts
// or intros shift later timestamps). ok is false when there is no confident
// alignment; callers then fall back to duration-based alignment.
type TimelineAligner interface {
	AlignTime(ctx context.Context, req AlignRequest, at time.Duration) (mapped time.Duration, ok bool)
}
