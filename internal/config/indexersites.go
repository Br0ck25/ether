package config

// IndexerSites configures built-in indexer sites: owner-installed
// definitions in the Cardigann YAML format, run by internal/cardigann.
// Ether bundles no definitions and no definitions source.
type IndexerSites struct {
	// DefinitionsURL is an optional source of definitions the owner trusts
	// and has the right to use: a GitHub contents API URL, a GitHub folder
	// or raw folder URL, or a plain list of definition URLs. Empty = none;
	// definitions are then added only by upload.
	DefinitionsURL string `json:"definitionsUrl,omitempty"`
	// FlareSolverrURL is an optional FlareSolverr service used to pass
	// Cloudflare browser challenges, e.g. http://localhost:8191.
	FlareSolverrURL string `json:"flareSolverrUrl,omitempty"`
}
