package config

// Availability bounds automatic recent-release monitoring and optional checks.
// Zero budgets disable the corresponding background work.
type Availability struct {
	Monitor        bool     `json:"monitor"`
	Verify         bool     `json:"verify"`
	PreferVerified bool     `json:"preferVerified"`
	NewlyAvailable bool     `json:"newlyAvailable"`
	IndexerIDs     []string `json:"indexerIds"` // empty = all supported enabled indexers
	Debrid         bool     `json:"debrid"`
	PollMinutes    int      `json:"pollMinutes"`
	MaxPages       int      `json:"maxPages"`
	FeedRequests   int      `json:"feedRequests"`  // total per UTC day, across indexers
	CheckRequests  int      `json:"checkRequests"` // indexer + provider API calls per day
	Grabs          int      `json:"grabs"`
	Checks         int      `json:"checks"`
	CheckBytes     int64    `json:"checkBytes"`     // conservative wire reservations for validation
	ReservePercent int      `json:"reservePercent"` // preserve this share of configured indexer caps
}

func DefaultAvailability() Availability {
	return Availability{Monitor: true, PollMinutes: 30, MaxPages: 3, FeedRequests: 100, CheckRequests: 100, Grabs: 20, Checks: 20, CheckBytes: 512 << 20, ReservePercent: 80, Debrid: true, PreferVerified: true, NewlyAvailable: true}
}
