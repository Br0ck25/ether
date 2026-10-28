package plex

import "testing"

func TestDiscoveryAvailabilityDoesNotFilterSavedState(t *testing.T) {
	for _, id := range []string{"home.top10", "home.newreleases", "home.trending", "movie.recentlyadded.1", "availability.movies"} {
		if !discoveryAvailabilityHub(id) {
			t.Errorf("discovery bypass: %s", id)
		}
	}
	for _, id := range []string{"home.continue", "continueWatching", "movie.inprogress.1", "tv.upnext", "home.watchlist", "home.playlists", "collection.saved"} {
		if discoveryAvailabilityHub(id) {
			t.Errorf("saved state filtered: %s", id)
		}
	}
}
