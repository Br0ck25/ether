package resolver

import (
	"github.com/braedonsaunders/ether/internal/core"
	"testing"
	"time"
)

func TestLanguageRankingRequiresExplicitPreference(t *testing.T) {
	s := New(&fakeIndexer{}, nil, nil, testCfg(), Options{Now: func() time.Time { return time.Unix(1800000000, 0) }})
	defer s.Close()
	r := withParsed(rel("language", "The.Signal.1999.1080p.WEB-DL.H264-GRP", 4000))
	score := func(lang string, prefs ...string) float64 {
		r.Info.Languages = []string{lang}
		return s.score(r, core.QualityProfile{PreferredLanguages: prefs}, matrixTitle())
	}
	for _, lang := range []string{"french", "german", "japanese", "multi"} {
		if score(lang) != score("english") {
			t.Errorf("default language penalty for %s", lang)
		}
	}
	if score("french", "french") <= score("english", "french") {
		t.Fatal("explicit French preference did not outrank English")
	}
	if score("english", "english") <= score("french", "english") {
		t.Fatal("explicit English preference ignored")
	}
}
