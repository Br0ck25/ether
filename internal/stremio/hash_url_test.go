package stremio

import (
	"strings"
	"testing"
)

func TestAddonURLHashIgnoresCredentials(t *testing.T) {
	key := strings.Repeat("e", 40)
	for _, tc := range []struct{ name, url, want string }{
		{"resolver key before hash", "https://addon.test/resolve/realdebrid/" + key + "/" + hashA + "/null/0/movie.mkv", hashA},
		{"query key is not hash", "https://addon.test/playback?token=" + key, ""},
		{"opaque path is not hash", "https://addon.test/playback/" + key + "/file.mkv", ""},
		{"named query wins over path credential", "https://addon.test/auth/" + key + "/play?info_hash=" + hashA, hashA},
		{"conflicting named hashes", "https://addon.test/play?info_hash=" + hashA + "&infoHash=" + hashB, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := hashFromURL(tc.url); got != tc.want {
				t.Fatalf("hash=%q want %q", got, tc.want)
			}
		})
	}
}
