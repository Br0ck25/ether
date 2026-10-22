package core

import "time"

// PausedSessionRetention bounds a silent pause without treating it as an
// abandoned stream. Explicit stop still closes immediately.
const PausedSessionRetention = 30 * time.Minute

// SessionActivity is an optional output-session extension. Only authenticated,
// ownership-checked player timelines may renew it; playlist polls may not.
type SessionActivity interface{ PlaybackState(state string) }
