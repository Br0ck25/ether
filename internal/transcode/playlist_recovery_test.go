package transcode

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFinalPlaylistRecoversFailedMuxerRename(t *testing.T) {
	const first = "#EXTM3U\n#EXT-X-PLAYLIST-TYPE:EVENT\n#EXTINF:4,\n00000.mkv\n"
	const complete = first + "#EXTINF:4,\n00001.mkv\n#EXT-X-ENDLIST\n"
	for _, tc := range []struct {
		name, pending string
		published     bool
		want          int
	}{
		{"complete", complete, true, 2},
		{"first publication failed", complete, false, 2},
		{"still writing", strings.TrimSuffix(complete, "#EXT-X-ENDLIST\n"), true, 1},
		{"wrong prefix", strings.Replace(complete, "00000.mkv", "00002.mkv", 1), true, 1},
		{"missing duration", strings.Replace(complete, "#EXTINF:4,\n00001", "00001", 1), true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "run.m3u8")
			if tc.published {
				if err := os.WriteFile(path, []byte(first), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(path+".tmp", []byte(tc.pending), 0o600); err != nil {
				t.Fatal(err)
			}
			live, err := readFFmpegPlaylist(path, false)
			if tc.published && (err != nil || len(live.entries) != 1 || live.ended) {
				t.Fatalf("live reader used uncommitted temporary playlist: %+v, %v", live, err)
			}
			if !tc.published && !os.IsNotExist(err) {
				t.Fatalf("live reader should wait for first publication: %v", err)
			}
			final, err := readFFmpegPlaylist(path, true)
			if err != nil || len(final.entries) != tc.want || final.ended != (tc.want == 2) {
				t.Fatalf("final playlist = %+v, %v; want %d entries", final, err, tc.want)
			}
		})
	}
}
