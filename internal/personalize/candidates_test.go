package personalize

import (
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

func TestCapItemsKeepsBothTypes(t *testing.T) {
	cat := newFakeCatalog(testNow)
	var its []candItem
	for j := 0; j < 30; j++ { // 30 films outrank…
		its = append(its, candItem{t: cat.titles[movieKey(0, j)], base: 2 - float64(j)*0.01})
	}
	for j := 0; j < 5; j++ { // …5 shows
		its = append(its, candItem{t: cat.titles[showKey(1, j)], base: 1 - float64(j)*0.01})
	}
	out := capItems(its, 12)
	shows := 0
	for i, it := range out {
		if it.t.Type == core.TypeShow {
			shows++
		}
		if i > 0 && out[i-1].base < it.base {
			t.Fatal("capItems must keep score order")
		}
	}
	if len(out) != 12 || shows != 4 {
		t.Fatalf("capItems kept %d items, %d shows; want 12 with 4 shows", len(out), shows)
	}
	if got := capItems(its[:30], 12); len(got) != 12 || got[11].t.Key != movieKey(0, 11) {
		t.Fatal("single-type rows are a plain cap")
	}
}

func TestItemFeatures(t *testing.T) {
	cheap := core.Title{Rating: 9.5, VoteCount: 3}
	solid := core.Title{Rating: 8.2, VoteCount: 20000}
	if quality(&cheap) >= quality(&solid) {
		t.Fatalf("a 9.5 from 3 votes (%v) should not beat an 8.2 from 20,000 (%v)", quality(&cheap), quality(&solid))
	}
	if q := quality(&core.Title{}); q != 0.3 {
		t.Fatalf("unrated quality = %v", q)
	}
	if popularity(&core.Title{Popularity: 500}) != 1 || popularity(&core.Title{}) != 0 {
		t.Fatal("popularity scale")
	}
	now := testNow
	if r := recency(&core.Title{Released: now}, now); r != 1 {
		t.Fatalf("recency today = %v", r)
	}
	if r := recency(&core.Title{Released: now.Add(48 * time.Hour)}, now); r != 0 {
		t.Fatalf("recency unreleased = %v", r)
	}
	if rankScore(0, 10) != 1 || rankScore(9, 10) >= 0.2 {
		t.Fatal("rank score")
	}
}
