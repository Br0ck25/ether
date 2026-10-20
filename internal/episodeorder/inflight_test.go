package episodeorder

import (
	"context"
	"sync"
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
)

type pausedCatalog struct {
	core.Catalog
	once             sync.Once
	entered, release chan struct{}
}

func (c *pausedCatalog) Grandchildren(ctx context.Context, key core.RatingKey) ([]core.Title, error) {
	c.once.Do(func() { close(c.entered); <-c.release })
	return c.Catalog.Grandchildren(ctx, key)
}

func TestPreferenceChangeDuringLayoutFetch(t *testing.T) {
	s := fixture(t)
	ctx := context.Background()
	show := core.ShowKey(10)
	if err := s.Save(ctx, 42, show, Preference{GroupID: "dvd", Specials: "last"}); err != nil {
		t.Fatal(err)
	}
	cat := &pausedCatalog{Catalog: s.catalog, entered: make(chan struct{}), release: make(chan struct{})}
	s.catalog = cat
	done := make(chan error, 1)
	go func() {
		_, err := s.Grandchildren(ctx, &core.User{ID: 42}, show)
		done <- err
	}()
	<-cat.entered
	err := s.Save(ctx, 42, show, Preference{GroupID: "dvd", Specials: "hidden"})
	close(cat.release)
	firstErr := <-done
	if err != nil || firstErr != nil {
		t.Fatalf("save=%v fetch=%v", err, firstErr)
	}
	episodes, err := s.Grandchildren(ctx, &core.User{ID: 42}, show)
	if err != nil {
		t.Fatal(err)
	}
	for _, ep := range episodes {
		if ep.Season == 0 {
			t.Fatal("in-flight layout restored specials after they were hidden")
		}
	}
}
