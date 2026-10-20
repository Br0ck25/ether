package releaseparse

import (
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
)

func TestCompareTitleIDs(t *testing.T) {
	for _, tt := range []struct {
		name               string
		reported, expected core.IDs
		match, conflict    bool
	}{
		{"normalized IMDb", core.IDs{IMDB: " TT0000123 "}, core.IDs{IMDB: "123"}, true, false},
		{"missing is unknown", core.IDs{}, core.IDs{TMDB: 123}, false, false},
		{"different namespaces unknown", core.IDs{TVDB: 123}, core.IDs{TMDB: 123}, false, false},
		{"conflict beats match", core.IDs{TMDB: 123, TVDB: 456}, core.IDs{TMDB: 123, TVDB: 789}, false, true},
		{"IMDb conflict", core.IDs{IMDB: "tt0000123"}, core.IDs{IMDB: "tt0000456"}, false, true},
		{"invalid IDs unknown", core.IDs{IMDB: "garbage", TMDB: -1}, core.IDs{IMDB: "tt123", TMDB: 123}, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			match, conflict := CompareTitleIDs(tt.reported, tt.expected)
			if match != tt.match || conflict != tt.conflict {
				t.Fatalf("match=%v conflict=%v, want %v/%v", match, conflict, tt.match, tt.conflict)
			}
		})
	}
}
