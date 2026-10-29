package config

// DefaultJellyfinPort is the port Jellyfin servers use, so their
// apps find Ether without being told.
const DefaultJellyfinPort = 8096

// Jellyfin configures the Jellyfin front end that runs next to
// the Plex one. Jellyfin and Plex apps all play from the same
// catalogue, and watch progress is shared.
type Jellyfin struct {
	Enabled bool `json:"enabled"`
	// Port the Jellyfin API listens on; Jellyfin apps default to 8096.
	Port int `json:"port"`
	// Discovery answers the apps' LAN "find servers" broadcast (UDP 7359).
	Discovery bool `json:"discovery"`
}
