package config

// Personalize holds the owner's switches for the Netflix-style per-viewer
// hooks (roadmap items #30–#32). All are on by default (see Defaults); a
// config written before this section existed keeps those defaults because
// loading unmarshals over them.
type Personalize struct {
	// PostPlay ranks the post-play "hook queue" per viewer instead of
	// returning generic related rows (#30). The ranked endpoint itself is
	// cheap; the switch exists for owners who prefer plain PMS behaviour.
	PostPlay bool `json:"postPlay"`
	// PostPlayPrebuffer pre-warms and pre-buffers the #1 post-play pick in
	// the last minutes of a movie or finale (#30).
	PostPlayPrebuffer bool `json:"postPlayPrebuffer"`
	// MicroReasons decorates single-item detail pages with the viewer's
	// decision header ("96% Match · … · Ends at 10:45 PM") (#31).
	MicroReasons bool `json:"microReasons"`
	// SessionIntent adapts /hubs to what the viewer is browsing tonight
	// (detail opens that did not turn into playback) (#32).
	SessionIntent bool `json:"sessionIntent"`
	// ClockFormat is the finish-time style of the decision header:
	// "12h", "24h", or "" to derive it from Catalog.Language.
	ClockFormat string `json:"clockFormat"`
}
