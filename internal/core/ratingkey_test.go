package core

import "testing"

func TestRatingKeyRoundTrip(t *testing.T) {
	cases := []struct {
		k       RatingKey
		kind    KeyKind
		tmdb    int64
		season  int
		episode int
	}{
		{MovieKey(603), KindMovie, 603, 0, 0},
		{ShowKey(1399), KindShow, 1399, 0, 0},
		{SeasonKey(1399, 0), KindSeason, 1399, 0, 0},
		{SeasonKey(1399, 8), KindSeason, 1399, 8, 0},
		{EpisodeKey(1399, 8, 6), KindEpisode, 1399, 8, 6},
		{EpisodeKey(99_999_999, 999, 9999), KindEpisode, 99_999_999, 999, 9999},
		{CollectionKey(87359), KindCollection, 87359, 0, 0},
	}
	for _, c := range cases {
		if c.k.Kind() != c.kind || c.k.TMDB() != c.tmdb || c.k.Season() != c.season || c.k.Episode() != c.episode {
			t.Errorf("%d: got kind=%d tmdb=%d s=%d e=%d", c.k, c.k.Kind(), c.k.TMDB(), c.k.Season(), c.k.Episode())
		}
		if int64(c.k) >= 1<<53 {
			t.Errorf("%d exceeds 2^53", c.k)
		}
		p, err := ParseRatingKey(c.k.String())
		if err != nil || p != c.k {
			t.Errorf("parse %s: %v %v", c.k, p, err)
		}
	}
	if EpisodeKey(1399, 8, 6).Parent() != SeasonKey(1399, 8) || SeasonKey(1399, 8).Parent() != ShowKey(1399) {
		t.Error("parent")
	}
}
