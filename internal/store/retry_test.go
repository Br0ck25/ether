package store

import (
	"context"
	"database/sql"
	"net"
	"net/url"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

// closedAddr is a local address nothing listens on.
func closedAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

func TestOpenWithRetryWaitsForUnreachableServer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var attempts atomic.Int32
	_, d, err := OpenWithRetry(ctx, Options{URL: "postgres://u:secret@" + closedAddr(t) + "/db?connect_timeout=1", Logger: quietLog()},
		RetryOptions{MinBackoff: 10 * time.Millisecond, MaxBackoff: 20 * time.Millisecond,
			OnRetry: func(n int, _ time.Duration, err error) {
				if attempts.Add(1) >= 3 {
					cancel()
				}
			}})
	if d != Postgres || err == nil || attempts.Load() < 3 {
		t.Fatalf("dialect %v err %v attempts %d", d, err, attempts.Load())
	}
}

func TestOpenWithRetryGivesUpOnConfigErrors(t *testing.T) {
	start := time.Now()
	var retried bool
	_, _, err := OpenWithRetry(context.Background(), Options{URL: "postgres://%zz", Logger: quietLog()},
		RetryOptions{OnRetry: func(int, time.Duration, error) { retried = true }})
	if err == nil || retried || time.Since(start) > 5*time.Second {
		t.Fatalf("invalid URL: err %v retried %v", err, retried)
	}
	raw := os.Getenv(TestPostgresEnv)
	if raw == "" {
		return
	}
	u, _ := url.Parse(raw)
	u.User = url.UserPassword(u.User.Username(), "definitely-not-the-password")
	_, _, err = OpenWithRetry(context.Background(), Options{URL: u.String(), Logger: quietLog()},
		RetryOptions{OnRetry: func(int, time.Duration, error) { retried = true }})
	if err == nil || retried || !IsPermanent(err) {
		t.Fatalf("bad password: err %v retried %v", err, retried)
	}
}

// TestOpenWithRetryThroughProxy starts with the database cut off and
// restores it while OpenWithRetry is waiting.
func TestOpenWithRetryThroughProxy(t *testing.T) {
	raw := os.Getenv(TestPostgresEnv)
	if raw == "" {
		t.Skip("set " + TestPostgresEnv + " to run PostgreSQL tests")
	}
	ctx := context.Background()
	schemaDB, err := OpenTestPostgres(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	defer schemaDB.Close()
	var schema string
	if err := schemaDB.QueryRowContext(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(raw)
	proxy, err := NewTestProxy(u.Host)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	proxy.Cut()
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	pu, _ := proxy.URL(u.String())
	var attempts atomic.Int32
	go func() {
		for attempts.Load() < 2 {
			time.Sleep(10 * time.Millisecond)
		}
		proxy.Restore()
	}()
	var db *sql.DB
	db, _, err = OpenWithRetry(ctx, Options{URL: pu, Logger: quietLog()}, RetryOptions{
		MinBackoff: 20 * time.Millisecond, MaxBackoff: 50 * time.Millisecond,
		OnRetry: func(int, time.Duration, error) { attempts.Add(1) },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
}
