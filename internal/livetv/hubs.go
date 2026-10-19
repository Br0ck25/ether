package livetv

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// HomeHub is the domain-level row used by the owner's final-layout preview.
// The Plex adapter preserves its own channel/tune metadata for this row ID;
// these synthetic keys are never sent to catalogue playback.
func (s *Service) HomeHub(ctx context.Context, u *core.User, limit int) (core.Hub, error) {
	hub := core.Hub{ID: "livetv.onnow", Title: "On Now", Context: "hub.livetv.onnow", Style: "shelf", Promoted: true, Items: []core.Title{}}
	cs, err := s.AllowedChannels(ctx, u)
	if err != nil {
		return hub, err
	}
	ps, err := s.Guide(ctx, u, s.o.Now(), s.o.Now().Add(time.Second))
	if err != nil {
		return hub, err
	}
	byChannel := map[string]Programme{}
	for _, p := range ps {
		byChannel[p.ChannelID] = p
	}
	if limit <= 0 {
		limit = 24
	}
	for _, c := range cs {
		h := sha256.Sum256([]byte(c.ID))
		key := core.LocalKey(int64(binary.BigEndian.Uint64(h[:8]) % 1_000_000_000_000))
		t := core.Title{Key: key, Type: core.MediaType("clip"), Title: c.Name, Summary: "Live channel: " + c.Name, ContentRating: c.Rating}
		if p, ok := byChannel[c.ID]; ok {
			t.Title = p.Item.Title
			t.Summary = c.Name + " · " + p.Item.Summary
			t.ContentRating = p.Item.Rating
			t.Runtime = p.End.Sub(p.Start)
		}
		hub.Items = append(hub.Items, t)
		if len(hub.Items) >= limit {
			hub.More = len(cs) > limit
			break
		}
	}
	return hub, nil
}
