package playback

import (
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/releaseparse"
)

// releaseMatchesTitle applies the same episode identity constraints to fresh,
// warmed, remembered and failover choices. Unknown filename metadata is not
// proof of identity; it only permits the resolver/file selector to continue.
func releaseMatchesTitle(rel core.Release, title *core.Title) bool {
	if title == nil {
		return true
	}
	expected := title.IDs
	if title.Type == core.TypeEpisode {
		expected = title.ShowIDs
		if expected.TMDB == 0 && title.Key.Kind() == core.KindEpisode {
			expected.TMDB = title.Key.TMDB()
		}
	}
	idMatch, idConflict := releaseparse.CompareTitleIDs(rel.TitleIDs, expected)
	if idConflict {
		return false
	}
	if title.Type != core.TypeEpisode {
		return true
	}
	season, episode := title.Season, title.Episode
	if title.Key.Kind() == core.KindEpisode {
		if season == 0 {
			season = title.Key.Season()
		}
		if episode == 0 {
			episode = title.Key.Episode()
		}
	}
	info := rel.Info
	if len(info.Episodes) == 0 && !info.FullSeason {
		info = releaseparse.Parse(rel.Name)
	}
	if episode > 0 && (len(info.Episodes) > 0 || info.FullSeason) {
		if info.FullSeason {
			if len(info.Seasons) > 0 {
				found := false
				for _, n := range info.Seasons {
					found = found || n == season
				}
				if !found {
					return false
				}
			}
		} else if !releaseparse.EpisodeMatches(info, season, episode) {
			return false
		}
	}
	if rel.Target != nil && episode > 0 &&
		(rel.Target.Episode != episode || rel.Target.Season != season) {
		return false
	}
	year := releaseparse.EpisodeSeriesYear(rel.Name, rel.Info.Year)
	if !releaseparse.SeriesYearMatches(year, title.ShowYear) ||
		(year == 0 && !idMatch && !releaseparse.EpisodeTitleMatches(rel.Name, title.Title)) {
		return false
	}
	// A torrent descriptor embeds the expected identity for file selection.
	// Older cached descriptors must be resolved again to acquire that target.
	return rel.Protocol != core.ProtocolTorrent || title.ShowYear <= 0 ||
		(rel.Target != nil && rel.Target.ShowYear == title.ShowYear && rel.Target.EpisodeTitle == title.Title)
}
