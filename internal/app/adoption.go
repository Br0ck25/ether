package app

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"sync"

	"github.com/braedonsaunders/ether/internal/backup"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/invites"
	"github.com/braedonsaunders/ether/internal/localmedia"
	"github.com/braedonsaunders/ether/internal/migrate"
	"github.com/braedonsaunders/ether/internal/plextv"
)

func (a *App) buildAdoption() {
	var probe localmedia.Prober
	if a.transcoder != nil {
		probe = a.transcoder
	}
	a.localmedia = localmedia.New(a.db, a.catalog, probe)
	a.backups = backup.New(a.db, a.cfg.Path(), filepath.Join(a.opts.DataDir, "backups"), 7)
	if a.clustered() {
		// Record which node holds each bundle (and allow a shared backup
		// directory): scheduled backups run on whichever node leads.
		self := a.cluster.Self()
		a.backups.SetCluster(backup.Node{ID: self.ID, Name: self.Name})
	}
	a.migration = migrate.New(a.db, a.catalog, migrate.Options{State: a.state})
}

func (a *App) buildInvites() {
	login := &plexSignIn{app: a, pins: map[string]*plextv.PIN{}}
	a.invites = invites.New(a.db, invites.Deps{
		Start: func(ctx context.Context) (string, string, string, error) { return login.StartPIN(ctx, "") },
		Poll: func(ctx context.Context, id string) (*invites.Account, error) {
			token, err := login.PINToken(ctx, id)
			if err != nil {
				return nil, err
			}
			if token == "" {
				return nil, nil
			}
			account, err := a.plextv.User(ctx, token)
			if err != nil {
				return nil, err
			}
			return &invites.Account{PlexID: account.ID, Email: account.Email}, nil
		},
		Share: func(ctx context.Context, user invites.Account, in invites.Invite) error {
			// Stage the policy before the external share so a new access token can
			// never race its local restrictions.
			p := in.Policy
			p.StreamLimit = in.StreamLimit
			if err := a.safety.Put(ctx, -user.PlexID, p); err != nil {
				return err
			}
			return a.plextv.ShareWith(ctx, a.cfg.Get().Plex.Token, user.Email, in.Sections)
		},
		Apply: func(ctx context.Context, user invites.Account, in invites.Invite) error {
			if err := a.registrar.SyncFriendsNow(ctx); err != nil {
				return fmt.Errorf("refresh library sharing: %w", err)
			}
			users, err := a.users.Users(ctx)
			if err != nil {
				return err
			}
			for _, u := range users {
				if u.PlexID == user.PlexID {
					policy := a.safety.ForUser(&u)
					u = *a.invitedUser(&u, in.Sections)
					if err := a.users.UpdateUser(ctx, u); err != nil {
						return err
					}
					return a.safety.Put(ctx, u.ID, policy)
				}
			}
			// plex.tv may leave a share pending until accepted; staged Plex-ID policy
			// applies immediately when its access token appears.
			return nil
		},
	})
}

// backgroundGroup joins component loops before App.Close releases SQLite.
type backgroundGroup struct{ sync.WaitGroup }

func (g *backgroundGroup) start(fn func()) { g.Add(1); go func() { defer g.Done(); fn() }() }

func adoptionHandlers(a *App, m map[string]http.Handler) {
	m["adoption/migration"] = a.migration.Handler()
	m["adoption/libraries"] = a.localmedia.Handler()
	m["adoption/backups"] = a.backups.Handler()
	m["safety/invites"] = a.invites.Handler()
}

// Invitation defaults cannot widen restrictions already assigned to a viewer.
func (a *App) invitedUser(u *core.User, sections []int) *core.User {
	out := a.safety.UserPolicy(u)
	if len(out.AllowedSections) == 0 {
		out.AllowedSections = append([]int(nil), sections...)
		return out
	}
	allowed := []int{}
	for _, existing := range out.AllowedSections {
		for _, invited := range sections {
			if existing == invited {
				allowed = append(allowed, existing)
				break
			}
		}
	}
	if len(allowed) == 0 {
		allowed = []int{0}
	} // Explicit deny; an empty list means unrestricted.
	out.AllowedSections = allowed
	return out
}
