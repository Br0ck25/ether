package newznab

import (
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
)

func TestFeedRetainsReportedTitleIDs(t *testing.T) {
	for _, feed := range []string{
		`<rss xmlns:n="urn:newznab"><channel><item><title>Alternate.2020.1080p.WEB</title><n:attr name="imdb" value="123"/><n:attr name="tmdbid" value="456"/><n:attr name="tvdbid" value="789"/></item></channel></rss>`,
		`{"item":[{"title":"Alternate.2020.1080p.WEB","newznab:attr":[{"@attributes":{"name":"imdbid","value":"tt0000123"}},{"@attributes":{"name":"tmdb","value":"456"}},{"@attributes":{"name":"tvdb","value":"789"}}]}]}`,
	} {
		rels, err := parseFeed([]byte(feed), "fake")
		if err != nil || len(rels) != 1 {
			t.Fatalf("parse: %v (%d releases)", err, len(rels))
		}
		if got := rels[0].TitleIDs; got != (core.IDs{IMDB: "tt0000123", TMDB: 456, TVDB: 789}) {
			t.Fatalf("reported IDs lost: %+v", got)
		}
	}
}

func TestMalformedFeedIDsCarryNoIdentityEvidence(t *testing.T) {
	rels, err := parseFeed([]byte(`<rss><channel><item><title>Unidentified.2020.1080p</title><attr name="imdbid" value="not-an-id"/><attr name="tmdbid" value="-1"/><attr name="tvdbid" value="0"/></item></channel></rss>`), "fake")
	if err != nil || len(rels) != 1 || rels[0].TitleIDs != (core.IDs{}) {
		t.Fatalf("malformed IDs became evidence: %+v %v", rels, err)
	}
}
