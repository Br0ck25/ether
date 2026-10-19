package personalize

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

func ip(v int) *int { return &v }

func TestNormalizeLayoutValidates(t *testing.T) {
	_, err := normalizeLayout([]core.LayoutRule{{
		Section: "kitchen", MaxRows: -1,
		Rows: []core.LayoutRowRule{
			{HubID: "", Action: "pin"},
			{HubID: "top-10", Action: "explode", Position: ip(-2)},
			{HubID: "[", Action: "hide"},
			{HubID: "x", Action: "bury", When: &core.LayoutWhen{Days: []int{9}, FromHour: ip(30), ToHour: ip(25), Devices: []string{"fridge"}}},
		},
	}})
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("want ValidationError, got %v", err)
	}
	fields := map[string]bool{}
	for _, f := range ve.InvalidFields() {
		fields[f[0]] = true
	}
	for _, want := range []string{"rules[0].section", "rules[0].maxRows", "rules[0].rows[0].hubId", "rules[0].rows[1].action",
		"rules[0].rows[1].position", "rules[0].rows[2].hubId", "rules[0].rows[3].when.days", "rules[0].rows[3].when.fromHour",
		"rules[0].rows[3].when.toHour", "rules[0].rows[3].when.devices"} {
		if !fields[want] {
			t.Errorf("missing problem for %s (got %v)", want, ve.InvalidFields())
		}
	}

	ok, err := normalizeLayout([]core.LayoutRule{{Rows: []core.LayoutRowRule{{HubID: "because.*", Action: "bury"}}}, {ID: "keep", Section: "movies", Scope: core.LayoutScope{UserIDs: []int64{2}}}})
	if err != nil {
		t.Fatal(err)
	}
	if ok[0].Section != "home" || ok[0].ID == "" || !ok[0].Scope.All || ok[1].ID != "keep" || ok[1].Rows == nil {
		t.Fatalf("defaults not filled: %+v", ok)
	}
}

func TestWhenMatches(t *testing.T) {
	wed21 := core.NewHubContext(time.Date(2026, 10, 14, 21, 0, 0, 0, time.UTC), "Plex for Roku", "Roku", "")
	sat10 := core.NewHubContext(time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC), "Plex for iOS", "iOS", "")
	weeknights := &core.LayoutWhen{Days: []int{1, 2, 3, 4}}
	if !whenMatches(weeknights, wed21) || whenMatches(weeknights, sat10) {
		t.Fatal("days")
	}
	evening := &core.LayoutWhen{FromHour: ip(18), ToHour: ip(23)}
	if !whenMatches(evening, wed21) || whenMatches(evening, sat10) {
		t.Fatal("hours")
	}
	overnight := &core.LayoutWhen{FromHour: ip(20), ToHour: ip(6)} // wraps midnight
	if !whenMatches(overnight, wed21) || whenMatches(overnight, sat10) {
		t.Fatal("wrapping hours")
	}
	tvOnly := &core.LayoutWhen{Devices: []string{"tv"}}
	if !whenMatches(tvOnly, wed21) || whenMatches(tvOnly, sat10) {
		t.Fatal("devices")
	}
	if !whenMatches(nil, sat10) {
		t.Fatal("nil when")
	}
}

func TestBanditPosterior(t *testing.T) {
	// No evidence: the hand-set prior.
	mean, _ := rowRate(0.1, arm{}, arm{}, 0, nil)
	if !approx(mean, 0.1, 1e-9) {
		t.Fatalf("prior mean = %v", mean)
	}
	// Server-wide evidence moves the estimate for everyone…
	globalGood := arm{shows: 300, reward: 90}
	mean, _ = rowRate(0.1, globalGood, arm{}, 0, nil)
	if mean < 0.25 {
		t.Fatalf("global evidence ignored: %v", mean)
	}
	// …and a viewer's own evidence moves theirs further.
	userBad := arm{shows: 200, reward: 0}
	meanBad, _ := rowRate(0.1, globalGood, userBad, 0, nil)
	if meanBad > 0.1 {
		t.Fatalf("viewer who never plays this row still rated %v", meanBad)
	}
	// Thompson samples are deterministic per seed and spread around the mean.
	_, s1 := rowRate(0.1, arm{}, arm{}, 1, seeded("u", 1, "day", 20261014))
	_, s2 := rowRate(0.1, arm{}, arm{}, 1, seeded("u", 1, "day", 20261014))
	if s1 != s2 {
		t.Fatal("same seed, different sample")
	}
	sum, sumSq, n := 0.0, 0.0, 2000
	for i := 0; i < n; i++ {
		_, s := rowRate(0.1, arm{}, arm{}, 1, seeded("u", i))
		sum += s
		sumSq += s * s
	}
	m := sum / float64(n)
	sd := math.Sqrt(sumSq/float64(n) - m*m)
	if !approx(m, 0.1, 0.01) || sd < 0.02 || sd > 0.08 {
		t.Fatalf("sample mean %v sd %v", m, sd)
	}
	// More evidence, less exploration.
	sumSq, sum = 0, 0
	for i := 0; i < n; i++ {
		_, s := rowRate(0.1, arm{}, arm{shows: 2000, reward: 200}, 1, seeded("v", i))
		sum += s
		sumSq += s * s
	}
	m2 := sum / float64(n)
	sd2 := math.Sqrt(sumSq/float64(n) - m2*m2)
	if sd2 >= sd/2 {
		t.Fatalf("evidence should shrink exploration: sd %v vs %v", sd2, sd)
	}
}
