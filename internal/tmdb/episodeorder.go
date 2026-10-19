package tmdb

import (
	"context"
	"fmt"
	"net/url"
)

// EpisodeGroupSummary describes a TMDB alternate order (type 1 original air,
// 2 absolute, 3 DVD, 4 digital, 5 story arc, 6 production, 7 TV).
type EpisodeGroupSummary struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	Type         int    `json:"type"`
	EpisodeCount int    `json:"episode_count"`
	GroupCount   int    `json:"group_count"`
}
type OrderedEpisode struct {
	Episode
	Order int `json:"order"`
}
type EpisodeSubgroup struct {
	ID       string           `json:"id"`
	Name     string           `json:"name"`
	Order    int              `json:"order"`
	Episodes []OrderedEpisode `json:"episodes"`
}
type EpisodeGroup struct {
	EpisodeGroupSummary
	Groups []EpisodeSubgroup `json:"groups"`
}

func (c *Client) EpisodeGroups(ctx context.Context, show int64) ([]EpisodeGroupSummary, error) {
	var out struct {
		Results []EpisodeGroupSummary `json:"results"`
	}
	err := c.getJSON(ctx, fmt.Sprintf("/tv/%d/episode_groups", show), url.Values{}, &out)
	return out.Results, err
}
func (c *Client) EpisodeGroup(ctx context.Context, id string) (*EpisodeGroup, error) {
	var out EpisodeGroup
	err := c.getJSON(ctx, "/tv/episode_group/"+url.PathEscape(id), url.Values{}, &out)
	return &out, err
}
