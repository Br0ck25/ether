package extras

import (
	"context"
	"testing"

	"github.com/braedonsaunders/ether/internal/config"
)

func TestResolvedURLFollowsResolverConfiguration(t *testing.T) {
	cfg := config.Extras{Enabled: true, ResolverCommand: "fixture", ResolverArgs: []string{"first"}}
	calls := 0
	s := New(Options{Config: func() config.Extras { return cfg }, LookPath: lookOK, Runner: func(_ context.Context, _ string, args []string) ([]byte, error) {
		calls++
		return []byte("https://fixture.example/" + args[0] + ".mp4"), nil
	}})
	e := Extra{Source: SourceCommand, Site: "YouTube", SiteKey: "trlOffEN001"}
	first, err := s.Resolve(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ResolverArgs = []string{"second"}
	next, err := s.Resolve(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	if first.URL == next.URL || calls != 2 {
		t.Fatalf("new resolver config ignored: %q calls=%d", next.URL, calls)
	}
	cfg.Enabled = false
	if _, err := s.Resolve(context.Background(), e); err == nil {
		t.Fatal("disabled extras still resolve cached URLs")
	}
}
