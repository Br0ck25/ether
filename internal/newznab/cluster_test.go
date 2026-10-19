package newznab

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
)

func TestBudgetReserveSharedAcrossNodes(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ether.db")
	cfg := testConfig("http://127.0.0.1:1")
	cfg.DailyLimit = 7
	var clients []*Client
	for i := 0; i < 2; i++ {
		db, err := store.OpenPath(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		clients = append(clients, NewManager(db, []config.Indexer{cfg}, Options{NZBDir: t.TempDir()}).Clients()[0])
	}
	var wg sync.WaitGroup
	var granted, refused atomic.Int32
	for i := 0; i < 15; i++ {
		for _, c := range clients {
			wg.Add(1)
			go func(c *Client) {
				defer wg.Done()
				switch err := c.reserve(ctx, "hit"); {
				case err == nil:
					granted.Add(1)
				case errors.Is(err, core.ErrBudgetExhausted):
					refused.Add(1)
				default:
					t.Error(err)
				}
			}(c)
		}
	}
	wg.Wait()
	hits, _, err := clients[1].counts(ctx)
	if err != nil || granted.Load() != 7 || refused.Load() != 23 || hits != 7 {
		t.Fatalf("granted=%d refused=%d stored=%d err=%v", granted.Load(), refused.Load(), hits, err)
	}
}
