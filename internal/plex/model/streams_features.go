package model

// Marker and Chapter use millisecond offsets, matching PMS metadata responses.
type Marker struct {
	ID              int64  `plex:"id"`
	Type            string `plex:"type"`
	StartTimeOffset int64  `plex:"startTimeOffset"`
	EndTimeOffset   int64  `plex:"endTimeOffset"`
	Final           *bool  `plex:"final"`
}
type Chapter struct {
	ID              int64  `plex:"id"`
	Index           int    `plex:"index"`
	Title           string `plex:"title,omitempty"`
	StartTimeOffset int64  `plex:"startTimeOffset"`
	EndTimeOffset   int64  `plex:"endTimeOffset"`
}
