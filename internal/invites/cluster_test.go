package invites

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/store"
)

func TestRedeemOnTwoNodesHonoursUseLimit(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ether.db")
	var shares atomic.Int32
	share := func(context.Context, Account, Invite) error { shares.Add(1); return nil }
	var nodes []*Service
	for i := 0; i < 2; i++ {
		db, err := store.OpenPath(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		nodes = append(nodes, New(db, Deps{Share: share}))
	}
	in, _, err := nodes[0].Create(ctx, Invite{Expires: time.Now().Add(time.Hour), MaxUses: 3, Sections: []int{1}})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var ok atomic.Int32
	for i := 0; i < 10; i++ {
		for _, s := range nodes {
			wg.Add(1)
			// The same account redeems on both nodes at once.
			go func(s *Service, id int64) {
				defer wg.Done()
				if s.Redeem(ctx, in, Account{id, "x@example.test"}) == nil {
					ok.Add(1)
				}
			}(s, int64(100+i))
		}
	}
	wg.Wait()
	var uses, claims int
	if err := nodes[1].db.QueryRow(`SELECT uses FROM invites_links WHERE id=?`, in.ID).Scan(&uses); err != nil {
		t.Fatal(err)
	}
	if err := nodes[1].db.QueryRow(`SELECT COUNT(*) FROM invites_claims`).Scan(&claims); err != nil {
		t.Fatal(err)
	}
	if uses != 3 || claims != 3 || shares.Load() != 3 {
		t.Fatalf("uses=%d claims=%d shares=%d", uses, claims, shares.Load())
	}
	if ok.Load() < 3 {
		t.Fatalf("successful redemptions %d", ok.Load())
	}
}
