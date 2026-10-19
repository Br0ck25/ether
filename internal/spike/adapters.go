package spike

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"

	"github.com/braedonsaunders/ether/internal/core"
)

// ---------------------------------------------------------------------------
// Auth

// auth accepts the owner's server token (and any extra configured tokens);
// with AcceptAnyToken every non-empty token is the owner.
type auth struct{ lib *Library }

func newAuth(l *Library) *auth { return &auth{lib: l} }

func (a *auth) owner() core.User {
	c := a.lib.cfg()
	name := c.Plex.OwnerUsername
	if name == "" {
		name = "owner"
	}
	return core.User{ID: 1, PlexID: c.Plex.OwnerPlexID, Username: name, Title: name, IsOwner: true}
}

func (a *auth) Authenticate(_ context.Context, token string) (*core.User, error) {
	if token == "" {
		return nil, core.ErrUnauthorized
	}
	if a.lib.opts.AcceptAnyToken {
		u := a.owner()
		return &u, nil
	}
	tokens := append([]string{a.lib.cfg().Plex.Token}, a.lib.opts.Tokens...)
	for _, t := range tokens {
		if t != "" && subtle.ConstantTimeCompare([]byte(t), []byte(token)) == 1 {
			u := a.owner()
			return &u, nil
		}
	}
	return nil, core.ErrUnauthorized
}

func (a *auth) Users(context.Context) ([]core.User, error) { return []core.User{a.owner()}, nil }
func (a *auth) UpdateUser(context.Context, core.User) error {
	return errors.New("spike: users are not editable")
}
func (a *auth) Owner(context.Context) (*core.User, error) {
	u := a.owner()
	return &u, nil
}

// ---------------------------------------------------------------------------
// Collections and recommendations

type noCollections struct{}

func (noCollections) List(context.Context, *core.User, core.MediaType) ([]core.Collection, error) {
	return nil, nil
}
func (noCollections) Get(context.Context, *core.User, core.RatingKey) (*core.Collection, error) {
	return nil, core.ErrNotFound
}
func (noCollections) Items(context.Context, *core.User, core.RatingKey, int, int) (core.Page, error) {
	return core.Page{}, core.ErrNotFound
}
func (noCollections) CreateSmart(context.Context, core.Collection) (*core.Collection, error) {
	return nil, errors.New("spike: collections not supported")
}
func (noCollections) DeleteSmart(context.Context, core.RatingKey) error { return core.ErrNotFound }

// recommender shows the local library as one promoted Home row.
type recommender struct{ lib *Library }

func (r recommender) HomeHubs(ctx context.Context, _ *core.User, limit int) ([]core.Hub, error) {
	p, err := catalog{r.lib}.Browse(ctx, core.BrowseQuery{Sort: core.SortTitle, Limit: limit})
	if err != nil {
		return nil, err
	}
	return []core.Hub{{
		ID: "spike.local", Title: "Local Files", Type: core.TypeMovie, Context: "hub.spike.local",
		Style: "shelf", Promoted: true, Items: p.Items, More: p.Total > len(p.Items),
	}}, nil
}

func (recommender) SectionHubs(context.Context, *core.User, core.MediaType, int) ([]core.Hub, error) {
	return nil, nil
}

func (recommender) Related(context.Context, *core.User, core.RatingKey, int) ([]core.Hub, error) {
	return nil, nil
}

// ---------------------------------------------------------------------------
// Transcoder

// transcoder probes and decides but never transcodes: the spike measures
// direct play. Wire internal/transcode instead to test HLS.
type transcoder struct{ prober prober }

func (t *transcoder) Probe(ctx context.Context, input string) (*core.MediaInfo, error) {
	info, err := t.prober.probe(ctx, input)
	if err != nil {
		return nil, err
	}
	return &info, nil
}

// Decide picks direct play when the client supports the container and
// codecs, remux when only the container is the problem, else transcode.
func (t *transcoder) Decide(info core.MediaInfo, caps core.ClientCaps, opts core.DecideOptions) core.Decision {
	d := core.Decision{AudioStream: opts.AudioStream, SubtitleStream: opts.SubtitleStream, BurnSubtitles: opts.BurnSubtitles}
	v := info.Video()
	videoOK := v == nil || caps.SupportsVideo(v.Codec)
	if v != nil && caps.MaxHeight > 0 && v.Height > caps.MaxHeight {
		videoOK = false
	}
	audioOK := true
	for _, s := range info.Streams {
		if s.Type == "audio" && (opts.AudioStream < 0 && s.Default || s.Index == opts.AudioStream) {
			audioOK = caps.SupportsAudio(s.Codec)
		}
	}
	containerOK := caps.SupportsContainer(info.Container)
	switch {
	case opts.DirectPlay && videoOK && audioOK && containerOK && !opts.BurnSubtitles:
		d.Kind, d.VideoCopy, d.AudioCopy, d.Container = core.DecisionDirectPlay, true, true, info.Container
		d.Reason = "direct play"
	case videoOK && audioOK && opts.DirectStream:
		d.Kind, d.VideoCopy, d.AudioCopy, d.Container = core.DecisionRemux, true, true, "mpegts"
		d.Reason = fmt.Sprintf("No direct play profile exists for container %s.", info.Container)
	default:
		d.Kind, d.Container = core.DecisionTranscode, "mpegts"
		d.VideoCopy, d.AudioCopy = videoOK && !opts.BurnSubtitles, audioOK
		if !d.VideoCopy {
			d.VideoCodec = "h264"
			d.MaxHeight = min(max(caps.MaxHeight, 0), 1080)
			if d.MaxHeight == 0 {
				d.MaxHeight = 1080
			}
		}
		if !d.AudioCopy {
			d.AudioCodec = "aac"
		}
		d.BitrateKbps = opts.MaxBitrateKbps
		if v != nil {
			d.Reason = fmt.Sprintf("No direct play video profile exists for container %s and video codec %s.", info.Container, v.Codec)
		}
	}
	if opts.MaxHeight > 0 && (d.MaxHeight == 0 || opts.MaxHeight < d.MaxHeight) && d.Kind == core.DecisionTranscode {
		d.MaxHeight = opts.MaxHeight
	}
	return d
}

var errNoTranscode = errors.New("spike: transcoding is not available in the spike; wire internal/transcode")

func (t *transcoder) StartHLS(context.Context, core.HLSRequest) (core.HLSSession, error) {
	return nil, errNoTranscode
}
func (t *transcoder) Session(string) (core.HLSSession, bool) { return nil, false }
func (t *transcoder) Stop(string)                            {}
func (t *transcoder) HWAccel() core.HWAccel                  { return core.HWAccel{Name: "none"} }
