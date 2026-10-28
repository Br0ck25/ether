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
		{ExtraKey(42), KindExtra, 42, 0, 0},
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
	if ExtraKey(123_456_789).ExtraID() != 123_456_789 || MovieKey(1).ExtraID() != 0 {
		t.Error("extra id")
	}
	if ExtraKey(999_999_999_999_999) >= RatingKey(1<<53) {
		t.Error("extra key exceeds 2^53")
	}
	if _, err := ParseRatingKey(ExtraKey(7).String()); err != nil {
		t.Errorf("parse extra key: %v", err)
	}
}

func TestEphemeralSmartKeysAvoidYearInReview(t *testing.T) {
	if k := EphemeralSmartKey(1); !k.IsEphemeral() || k.Kind() != KindSmart {
		t.Fatalf("EphemeralSmartKey(1) = %d not ephemeral", k)
	}
	if k := EphemeralSmartKey(EphemeralSmartEnd - EphemeralSmartBase - 1); !k.IsEphemeral() {
		t.Fatalf("last ephemeral key %d not ephemeral", k)
	}
	// personalize's Year in Review keys start at payload 9*10^14
	// (base + year*10 + kind) and must never look ephemeral.
	review := SmartKey(EphemeralSmartEnd + 2026*10 + 1)
	if review.IsEphemeral() {
		t.Fatalf("Year in Review key %d reported ephemeral", review)
	}
	if SmartKey(42).IsEphemeral() || MovieKey(42).IsEphemeral() {
		t.Fatal("database smart key or movie key reported ephemeral")
	}
}
