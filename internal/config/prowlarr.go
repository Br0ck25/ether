package config

// Prowlarr is an owner-managed source of Newznab indexers. Discovery is explicit
// and does not search or download from the indexers.
type Prowlarr struct {
	URL        string `json:"url"`
	APIKey     string `json:"apiKey"`
	DailyLimit int    `json:"dailyLimit"`
	DailyGrabs int    `json:"dailyGrabs"`
	Priority   int    `json:"priority"`
}
