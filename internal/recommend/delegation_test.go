package recommend

import (
	"context"
	"github.com/braedonsaunders/ether/internal/core"
	"testing"
)

type homeSource struct {
	user  int64
	limit int
	typ   core.MediaType
}

func (h *homeSource) HomeHubs(_ context.Context, u *core.User, limit int) ([]core.Hub, error) {
	h.user = u.ID
	h.limit = limit
	return []core.Hub{{ID: "canonical-home"}}, nil
}
func (h *homeSource) SectionHubs(_ context.Context, u *core.User, typ core.MediaType, limit int) ([]core.Hub, error) {
	h.typ = typ
	return h.HomeHubs(context.Background(), u, limit)
}
func (h *homeSource) Related(context.Context, *core.User, core.RatingKey, int) ([]core.Hub, error) {
	panic("detail recommendations must not delegate back to Home")
}
func TestHomeUsesCanonicalEngine(t *testing.T) {
	r := New(Options{})
	h := &homeSource{}
	r.SetHomeSource(h)
	u := &core.User{ID: 7}
	if rows, err := r.HomeHubs(context.Background(), u, 18); err != nil || len(rows) != 1 || rows[0].ID != "canonical-home" {
		t.Fatal(rows, err)
	}
	if _, err := r.SectionHubs(context.Background(), u, core.TypeShow, 12); err != nil {
		t.Fatal(err)
	}
	if h.user != 7 || h.limit != 12 || h.typ != core.TypeShow {
		t.Fatal("Home request context changed", h)
	}
}
