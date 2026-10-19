package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/braedonsaunders/ether/internal/admin"
	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/llm"
	"github.com/braedonsaunders/ether/internal/newznab"
	"github.com/braedonsaunders/ether/internal/plextv"
	"github.com/braedonsaunders/ether/internal/tmdb"
	"github.com/braedonsaunders/ether/internal/usenet"
)

func (a *App) buildAdmin() http.Handler {
	return admin.New(admin.Deps{
		Config:        a.cfg,
		Extensions:    a.extensionHandlers(),
		ValidateExtra: validatePlaybackConfig,
		Logger:        a.log.With("component", "admin"),
		Logs:          a.logs,
		Version:       a.opts.Version,
		PlexAuth:      &plexAuth{app: a, pins: map[string]*plextv.PIN{}},
		SignIn:        &plexSignIn{app: a, pins: map[string]*plextv.PIN{}},
		IsOwnerToken: func(ctx context.Context, token string) bool {
			u, err := a.users.Authenticate(ctx, token)
			return err == nil && u.IsOwner
		},
		TestUsenet: func(ctx context.Context, p config.UsenetProvider) error {
			return usenet.TestProvider(ctx, p)
		},
		TestIndexer: func(ctx context.Context, ix config.Indexer) error {
			return newznab.Test(ctx, ix)
		},
		TestTMDB: func(ctx context.Context, key string) error {
			c := tmdb.New(key, a.log)
			defer c.Close()
			_, err := c.Configuration(ctx)
			return err
		},
		TestLLM: func(ctx context.Context, c config.LLM) error {
			_, err := llm.NewClient(llm.ClientOptions{Config: c, Logger: a.log}).Chat(ctx,
				[]llm.Message{{Role: "user", Content: "Reply with the word ok."}}, false)
			return err
		},
		SessionsStatus:  func(context.Context) any { return (ownerPlayback{Playback: a.playback, app: a}).Sessions() },
		CacheStatus:     a.cacheStatus,
		ProvidersStatus: func(context.Context) any { return a.fetcher.Stats() },
		IndexersStatus: func(ctx context.Context) any {
			r, err := a.indexers.Remaining(ctx)
			if err != nil {
				return map[string]string{"error": err.Error()}
			}
			return r
		},
		CatalogStatus: func(ctx context.Context) any {
			s, err := a.catalog.Stats(ctx)
			if err != nil {
				return map[string]string{"error": err.Error()}
			}
			return s
		},
		RemoteStatus:  func(context.Context) any { return a.registrar.Status() },
		UpdatesStatus: a.updateStatus,
		HWAccelStatus: func(context.Context) any {
			if a.transcoder == nil {
				return map[string]string{"error": "ffmpeg not found"}
			}
			return map[string]any{"hwaccel": a.transcoder.HWAccel(), "ffmpeg": a.transcoder.Version()}
		},
		ExtraStatus: map[string]admin.StatusFunc{
			"stream":   func(context.Context) any { return a.engine.Stats() },
			"widget":   a.widgetStatus,
			"prefetch": func(context.Context) any { return a.prefetch.Stats() },
		},
		ResolverTraces:  func(context.Context) (any, error) { return a.resolver.Traces(), nil },
		Users:           a.users,
		Personalization: a.personalize,
		Collections:     a.collections,
		CatalogHelper:   a.collections,
		Images:          a.images,
	})
}

func (a *App) cacheStatus(context.Context) any {
	used, budget := a.cache.Usage()
	return map[string]int64{"used": used, "budget": budget}
}

// plexAuth adapts the plex.tv PIN sign-in and claim flow to the wizard.
type plexAuth struct {
	app  *App
	mu   sync.Mutex
	pins map[string]*plextv.PIN
}

func (p *plexAuth) client() *plextv.Client { return p.app.registrar.SignInClient() }

func (p *plexAuth) StartPIN(ctx context.Context) (id, code, authURL string, err error) {
	pin, err := p.client().CreatePIN(ctx)
	if err != nil {
		return "", "", "", err
	}
	id = strconv.FormatInt(pin.ID, 10)
	p.mu.Lock()
	p.pins[id] = pin
	p.mu.Unlock()
	return id, pin.Code, p.client().AuthURL(pin, ""), nil
}

func (p *plexAuth) PollPIN(ctx context.Context, id string) (bool, error) {
	p.mu.Lock()
	pin := p.pins[id]
	p.mu.Unlock()
	if pin == nil {
		return false, errors.New("unknown sign-in request; start again")
	}
	got, err := p.client().CheckPIN(ctx, pin.ID, pin.Code)
	if err != nil {
		return false, err
	}
	if got.AuthToken == "" {
		return false, nil
	}
	acct, err := p.client().User(ctx, got.AuthToken)
	if err != nil {
		return false, fmt.Errorf("plex.tv account: %w", err)
	}
	if err := p.app.cfg.Update(func(c *config.Config) error {
		c.Plex.Token = got.AuthToken
		c.Plex.OwnerPlexID = acct.ID
		c.Plex.OwnerUsername = acct.Username
		return nil
	}); err != nil {
		return false, err
	}
	p.mu.Lock()
	delete(p.pins, id)
	p.mu.Unlock()
	return true, p.app.users.BootstrapOwner(ctx, p.app.cfg.Get().Plex)
}

func (p *plexAuth) Claim(ctx context.Context) error {
	tok := p.app.cfg.Get().Plex.Token
	if tok == "" {
		return errors.New("sign in to plex.tv first")
	}
	if _, err := p.app.registrar.Claim(ctx, tok); err != nil {
		return err
	}
	return p.app.users.BootstrapOwner(ctx, p.app.cfg.Get().Plex)
}

func (p *plexAuth) Status() any { return p.app.registrar.Status() }

// plexSignIn is the admin login: a plex.tv PIN whose token the admin
// handler accepts only if it belongs to the owner. Unlike plexAuth it
// changes no settings.
type plexSignIn struct {
	app  *App
	mu   sync.Mutex
	pins map[string]*plextv.PIN
}

func (p *plexSignIn) StartPIN(ctx context.Context, forwardURL string) (id, code, authURL string, err error) {
	c := p.app.registrar.SignInClient()
	pin, err := c.CreatePIN(ctx)
	if err != nil {
		return "", "", "", err
	}
	id = strconv.FormatInt(pin.ID, 10)
	p.mu.Lock()
	for k, v := range p.pins {
		if !v.ExpiresAt.IsZero() && time.Now().After(v.ExpiresAt) {
			delete(p.pins, k)
		}
	}
	p.pins[id] = pin
	p.mu.Unlock()
	return id, pin.Code, c.AuthURL(pin, forwardURL), nil
}

func (p *plexSignIn) PINToken(ctx context.Context, id string) (string, error) {
	p.mu.Lock()
	pin := p.pins[id]
	p.mu.Unlock()
	if pin == nil || (!pin.ExpiresAt.IsZero() && time.Now().After(pin.ExpiresAt)) {
		return "", errors.New("unknown sign-in request")
	}
	if pin.AuthToken != "" {
		return pin.AuthToken, nil
	}
	got, err := p.app.registrar.SignInClient().CheckPIN(ctx, pin.ID, pin.Code)
	if err != nil {
		return "", err
	}
	if got.AuthToken != "" {
		p.mu.Lock()
		pin.AuthToken = got.AuthToken
		p.mu.Unlock()
	}
	return got.AuthToken, nil
}
