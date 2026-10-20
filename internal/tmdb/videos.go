package tmdb

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Video types TMDB returns from /videos endpoints.
const (
	VideoTrailer         = "Trailer"
	VideoTeaser          = "Teaser"
	VideoClip            = "Clip"
	VideoFeaturette      = "Featurette"
	VideoBehindTheScenes = "Behind the Scenes"
	VideoBloopers        = "Bloopers"
)

// Video is one TMDB /videos result: a trailer, teaser or other clip hosted
// on an external site (usually YouTube or Vimeo). Key is the site-specific
// video id, not a playable URL.
type Video struct {
	ID          string `json:"id"`
	ISO639      string `json:"iso_639_1"`
	ISO3166     string `json:"iso_3166_1"`
	Key         string `json:"key"`
	Name        string `json:"name"`
	Site        string `json:"site"`
	Size        int    `json:"size"` // vertical resolution, e.g. 1080
	Type        string `json:"type"` // Trailer, Teaser, Clip, Featurette, ...
	Official    bool   `json:"official"`
	PublishedAt string `json:"published_at"`
}

// Published parses PublishedAt (zero when empty or invalid).
func (v Video) Published() time.Time {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.000Z"} {
		if t, err := time.Parse(layout, v.PublishedAt); err == nil {
			return t
		}
	}
	return ParseDate(v.PublishedAt)
}

// Videos is a /videos response.
type Videos struct {
	ID      int64   `json:"id"`
	Results []Video `json:"results"`
}

// videoLanguageParam builds include_video_language from the requested
// language (a BCP-47 tag like "de-DE" or an ISO 639-1 code), always
// falling back to English and language-neutral ("null") videos.
func videoLanguageParam(lang string) string {
	seen := map[string]bool{}
	var parts []string
	add := func(s string) {
		s = strings.ToLower(strings.TrimSpace(s))
		if i := strings.IndexAny(s, "-_"); i > 0 {
			s = s[:i]
		}
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		parts = append(parts, s)
	}
	add(lang)
	add("en")
	parts = append(parts, "null")
	return strings.Join(parts, ",")
}

func (c *Client) videos(ctx context.Context, path, lang string) ([]Video, error) {
	if lang == "" {
		lang = c.language
	}
	q := url.Values{}
	q.Set("include_video_language", videoLanguageParam(lang))
	var out Videos
	if err := c.getJSON(ctx, path, q, &out); err != nil {
		return nil, err
	}
	return out.Results, nil
}

// MovieVideos fetches /movie/{id}/videos. lang overrides the client
// language for the include_video_language fallback list.
func (c *Client) MovieVideos(ctx context.Context, id int64, lang string) ([]Video, error) {
	return c.videos(ctx, fmt.Sprintf("/movie/%d/videos", id), lang)
}

// TVVideos fetches /tv/{id}/videos.
func (c *Client) TVVideos(ctx context.Context, id int64, lang string) ([]Video, error) {
	return c.videos(ctx, fmt.Sprintf("/tv/%d/videos", id), lang)
}

// SeasonVideos fetches /tv/{id}/season/{n}/videos.
func (c *Client) SeasonVideos(ctx context.Context, tvID int64, season int, lang string) ([]Video, error) {
	return c.videos(ctx, fmt.Sprintf("/tv/%d/season/%d/videos", tvID, season), lang)
}

// EpisodeVideos fetches /tv/{id}/season/{n}/episode/{e}/videos.
func (c *Client) EpisodeVideos(ctx context.Context, tvID int64, season, episode int, lang string) ([]Video, error) {
	return c.videos(ctx, fmt.Sprintf("/tv/%d/season/%d/episode/%d/videos", tvID, season, episode), lang)
}
