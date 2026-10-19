package personalize

import (
	"sort"
	"strings"

	"github.com/braedonsaunders/ether/internal/core"
)

// Row kinds. A kind is the stable, owner-facing identity of a row family:
// layout rules, insights and the bandit all work per kind. Hub ids are per
// row ("home.because.<key>"), kinds are not.
const (
	KindContinue      = "continue-watching"
	KindUpNext        = "up-next"
	KindTop10         = "top-10"
	KindBecause       = "because-you-watched"
	KindPerson        = "more-from-person"
	KindTrendingTaste = "trending-in-your-taste"
	KindHiddenGems    = "hidden-gems"
	KindNewThisWeek   = "new-this-week"
	KindComfort       = "comfort-rewatches"
	KindDifferent     = "something-different"
	KindShortOnTime   = "short-on-time"
	KindFranchise     = "finish-the-franchise"
	KindLLM           = "llm"
	KindTrending      = "trending"
	KindPopularServer = "popular-on-server"
	KindNewReleases   = "new-releases"
	KindTopRated      = "top-rated"
	KindPopular       = "popular"
	KindAiringToday   = "airing-today"
	KindTraktRecs     = "trakt-recommendations"
	KindTraktWatch    = "trakt-watchlist"
	KindRecentlyAdded = "recently-added" // built by the Plex front end in sections
	KindPlaylists     = "playlists"      // built by the Plex front end on Home
	// KindCollectionPrefix + collection id is an owner collection row.
	KindCollectionPrefix = "collection:"
)

// kindInfo describes a row kind.
type kindInfo struct {
	kind        string
	title       string // generic title for the owner UI
	description string
	// prior is the expected play rate of a row of this kind before any
	// evidence (plays per row-day shown). It seeds the bandit.
	prior float64
	// slug is the hub id stem; dynamic rows append ".<id>".
	slug string
	// context is the Plex hub context stem ("hub.<section>.<context>").
	context string
	// cap is the most rows of this kind on one screen (0 = 1).
	cap int
	// dynamic kinds have one row per seed (person, title, franchise...).
	dynamic bool
	source  string
}

var kinds = []kindInfo{
	{kind: KindContinue, title: "Continue Watching", description: "Titles the viewer is part-way through (built by Plex; the engine only ranks it — it can be pinned or buried, not hidden).",
		prior: 0.30, slug: "continue", context: "continue", source: core.RowSourcePlex},
	{kind: KindUpNext, title: "Up Next", description: "The next episode of shows the viewer recently finished an episode of.",
		prior: 0.22, slug: "ondeck", context: "ondeck"},
	{kind: KindTop10, title: "Top 10 for You", description: "The ten titles the taste model rates highest right now.",
		prior: 0.11, slug: "top10", context: "toppicks"},
	{kind: KindBecause, title: "Because You Watched…", description: "Titles similar to something the viewer recently finished. One row per recent favourite, rotating daily.",
		prior: 0.09, slug: "because", context: "because", cap: 3, dynamic: true},
	{kind: KindPerson, title: "More from…", description: "More work from a director, writer or actor the viewer keeps coming back to.",
		prior: 0.07, slug: "person", context: "person", cap: 2, dynamic: true},
	{kind: KindTrendingTaste, title: "Trending in Your Taste", description: "This week's trending titles, filtered and ranked by the viewer's taste.",
		prior: 0.08, slug: "trending.taste", context: "trending.taste"},
	{kind: KindHiddenGems, title: "Hidden Gems for You", description: "Highly rated, little-known titles that fit the viewer's taste.",
		prior: 0.06, slug: "hiddengems", context: "hiddengems"},
	{kind: KindNewThisWeek, title: "New This Week", description: "Fresh releases in the viewer's favourite genres.",
		prior: 0.07, slug: "newthisweek", context: "newthisweek"},
	{kind: KindComfort, title: "Comfort Rewatches", description: "Favourites the viewer has finished, rewatched or rated highly and not seen in a while.",
		prior: 0.05, slug: "comfort", context: "comfort"},
	{kind: KindDifferent, title: "Something Different", description: "Exploration: acclaimed titles outside the viewer's usual genres.",
		prior: 0.04, slug: "different", context: "different"},
	{kind: KindShortOnTime, title: "Short on Time", description: "Shorter films and episodes, fitted to how long the viewer usually watches in this context.",
		prior: 0.06, slug: "shortontime", context: "shortontime"},
	{kind: KindFranchise, title: "Finish the Franchise", description: "The remaining films of a franchise the viewer has started.",
		prior: 0.09, slug: "franchise", context: "franchise", cap: 2, dynamic: true},
	{kind: KindLLM, title: "Themed Rows (AI)", description: "Natural-language themed rows from the owner's LLM, seeded by recent favourites.",
		prior: 0.06, slug: "llm", context: "llm", cap: 2, dynamic: true},
	{kind: KindTrending, title: "Trending This Week", description: "What is trending on TMDB this week.",
		prior: 0.06, slug: "trending", context: "trending"},
	{kind: KindPopularServer, title: "Popular on This Server", description: "What other viewers on this server watched most in the last 30 days.",
		prior: 0.07, slug: "popular.server", context: "popular.server"},
	{kind: KindNewReleases, title: "New Releases", description: "The newest released titles in the catalogue.",
		prior: 0.05, slug: "newreleases", context: "recentlyreleased"},
	{kind: KindTopRated, title: "Top Rated", description: "TMDB's top rated titles.",
		prior: 0.045, slug: "toprated", context: "toprated"},
	{kind: KindPopular, title: "Popular", description: "TMDB's most popular titles.",
		prior: 0.045, slug: "popular", context: "popular"},
	{kind: KindAiringToday, title: "Airing Today", description: "Shows with an episode airing today.",
		prior: 0.04, slug: "airing", context: "airing"},
	{kind: KindTraktRecs, title: "Trakt Recommendations", description: "Personal recommendations from the viewer's connected Trakt account.",
		prior: 0.06, slug: "trakt.recommendations", context: "trakt"},
	{kind: KindTraktWatch, title: "Your Trakt Watchlist", description: "The viewer's Trakt watchlist.",
		prior: 0.08, slug: "trakt.watchlist", context: "trakt.watchlist"},
	{kind: KindRecentlyAdded, title: "Recently Added", description: "Built by the Plex front end at the top of each library section.",
		prior: 0.05, slug: "recentlyadded", context: "recentlyadded", source: core.RowSourcePlex},
	{kind: KindPlaylists, title: "Recent Playlists", description: "The viewer's playlists, built by the Plex front end at the end of Home.",
		prior: 0.03, slug: "playlists", context: "playlists", source: core.RowSourcePlex},
}

var kindByName = func() map[string]kindInfo {
	m := make(map[string]kindInfo, len(kinds))
	for _, k := range kinds {
		if k.cap == 0 {
			k.cap = 1
		}
		if k.source == "" {
			k.source = core.RowSourceEngine
		}
		m[k.kind] = k
	}
	return m
}()

// infoFor returns the description of a kind; owner collection kinds share
// one generic description.
func infoFor(kind string) kindInfo {
	if k, ok := kindByName[kind]; ok {
		return k
	}
	if strings.HasPrefix(kind, KindCollectionPrefix) {
		return kindInfo{kind: kind, title: "Owner collection", description: "A collection designed in Collection Studio.",
			prior: 0.06, slug: "collection", context: "collection", cap: 1, source: core.RowSourceOwner}
	}
	return kindInfo{kind: kind, title: kind, prior: 0.05, slug: kind, context: kind, cap: 1, source: core.RowSourceEngine}
}

// banditKind is the kind used for the bandit's global prior pool: owner
// collections share one global prior but keep per-collection evidence.
func priorKind(kind string) string {
	if strings.HasPrefix(kind, KindCollectionPrefix) {
		return "collection"
	}
	return kind
}

// descriptors lists every row kind for the Home Designer.
func descriptors(extra []core.RowDescriptor) []core.RowDescriptor {
	out := make([]core.RowDescriptor, 0, len(kinds)+len(extra))
	for _, k := range kinds {
		out = append(out, core.RowDescriptor{HubID: k.kind, Title: k.title, Kind: k.kind, Description: k.description})
	}
	sort.SliceStable(extra, func(i, j int) bool { return extra[i].Title < extra[j].Title })
	return append(out, extra...)
}

// sectionPrefix returns the hub id / context prefix for a section type.
func sectionPrefix(t core.MediaType) string {
	switch t {
	case core.TypeMovie:
		return "movie"
	case core.TypeShow:
		return "tv"
	}
	return "home"
}

// sectionName is the layout-rule section name for a section type.
func sectionName(t core.MediaType) string {
	switch t {
	case core.TypeMovie:
		return "movies"
	case core.TypeShow:
		return "shows"
	}
	return "home"
}
