package debrid

// knownTorrentFinder is a package-specific optional capability.
type knownTorrentFinder interface{ findKnown(string) string }
