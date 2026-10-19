package app

import (
	"context"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/extras"
	"github.com/braedonsaunders/ether/internal/localmedia"
)

// localTrailerLister adapts the local-media index to the extras
// service's file listing.
type localTrailerLister struct{ svc *localmedia.Service }

func (l localTrailerLister) LocalFiles(ctx context.Context) ([]extras.LocalFile, error) {
	if l.svc == nil {
		return nil, nil
	}
	files, err := l.svc.Files(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]extras.LocalFile, 0, len(files))
	for _, f := range files {
		if f.Error != "" {
			continue
		}
		out = append(out, extras.LocalFile{
			Path: f.Path, Key: f.Key, Size: f.Size,
			Duration: f.Info.Duration, Container: f.Info.Container,
		})
	}
	return out, nil
}

// buildExtras wires the trailers/extras service. It stays off (and says
// why, once) until the owner enables it and configures a playable source.
func (a *App) buildExtras() {
	a.extras = extras.New(extras.Options{
		Videos: a.tmdb,
		Local:  localTrailerLister{svc: a.localmedia},
		Config: func() config.Extras { return a.cfg.Get().Extras },
		Logger: a.log.With("component", "extras"),
	})
	if reason := a.extras.DisabledReason(); reason != "" {
		a.log.Info("extras off", "reason", reason)
	} else {
		a.log.Info("extras on", "command", a.extras.CommandAvailable(), "local", a.extras.LocalAvailable())
	}
}
