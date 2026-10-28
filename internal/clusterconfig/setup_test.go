package clusterconfig

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/store"
)

func TestSetupValidationAndSecretSafeErrors(t *testing.T) {
	for _, req := range []SetupRequest{{}, {Mode: "create", DatabaseURL: "sqlite://file"}, {Mode: "join", Token: "wrong"}} {
		if err := req.Validate(); err == nil {
			t.Fatalf("accepted invalid request: mode %q", req.Mode)
		}
	}
	tok := JoinToken{DatabaseURL: "postgres://owner:secret@db/ether", ID: "id", Secret: "private", Key: make([]byte, 32)}
	req := SetupRequest{Mode: "join", Token: tok.String()}
	if err := req.Validate(); err != nil {
		t.Fatal(err)
	}
	if req.URL() != tok.DatabaseURL {
		t.Fatal("did not use token database")
	}
	req.DatabaseURL = "postgres://other/db"
	if req.URL() != req.DatabaseURL {
		t.Fatal("override ignored")
	}
	for _, err := range []error{errors.New("password=private postgres://owner:secret@db/ether"), ErrTokenExpired, ErrTokenUsed} {
		msg := SetupError(err)
		if msg == "" || strings.Contains(msg, "private") || strings.Contains(msg, "secret@") {
			t.Fatal("unsafe setup error")
		}
	}
}

func TestJoinPreflightDoesNotConsumeToken(t *testing.T) {
	ctx := context.Background()
	db := store.OpenTest(t)
	cfg, _ := node(t, "")
	if err := Attach(ctx, cfg, db, config.SharedOptions{PollInterval: -1}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	tok, _, err := CreateJoinToken(ctx, db, "postgres://db/ether", cfg.CredentialKey(), time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := checkJoin(ctx, db, tok, now); err != nil {
			t.Fatal(err)
		}
	}
	bad := tok
	bad.Secret = "different"
	if !errors.Is(checkJoin(ctx, db, bad, now), ErrTokenInvalid) {
		t.Fatal("accepted wrong secret")
	}
	if !errors.Is(checkJoin(ctx, db, tok, now.Add(2*time.Hour)), ErrTokenExpired) {
		t.Fatal("accepted expired token")
	}
	if err := ConsumeJoinToken(ctx, db, tok, "node", now); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(checkJoin(ctx, db, tok, now), ErrTokenUsed) {
		t.Fatal("accepted used token")
	}
}
