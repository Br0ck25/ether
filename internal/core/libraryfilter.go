package core

// LibraryExclusion hides matching catalogue titles everywhere in Plex. Groups
// are ORed; each group combines its conditions with all or any. Watch history
// and the underlying metadata are retained.
type LibraryExclusion struct {
	Name      string           `json:"name"`
	MediaType string           `json:"mediaType"` // movie, show, mixed
	Match     string           `json:"match"`     // all, any
	Rules     []CollectionRule `json:"rules"`
}
