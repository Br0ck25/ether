package playback

import (
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/releaseparse"
)

// releaseMatchesTitle applies the same episode identity constraints to fresh,
// warmed, remembered and failover choices. Unknown filename metadata is not
// proof of identity; it only permits the resolver/file selector to continue.
func releaseMatchesTitle(rel core.Release, title *core.Title) bool {
	if title == nil || title.Type != core.TypeEpisode {
		return true
	}
	year := releaseparse.EpisodeSeriesYear(rel.Name, rel.Info.Year)
	if !releaseparse.SeriesYearMatches(year, title.ShowYear) ||
		(year == 0 && !releaseparse.EpisodeTitleMatches(rel.Name, title.Title)) {
		return false
	}
	// A torrent descriptor embeds the expected identity for file selection.
	// Older cached descriptors must be resolved again to acquire that target.
	return rel.Protocol != core.ProtocolTorrent || title.ShowYear <= 0 ||
		(rel.Target != nil && rel.Target.ShowYear == title.ShowYear && rel.Target.EpisodeTitle == title.Title)
}
