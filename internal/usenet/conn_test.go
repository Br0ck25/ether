package usenet

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math/rand"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ether-media/ether/internal/usenet/nntptest"
	"github.com/ether-media/ether/internal/yenc"
)

func randData(seed int64, n int) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(seed)).Read(b)
	return b
}

func dialServer(t *testing.T, s *nntptest.Server) *Conn {
	t.Helper()
	p := s.Provider("t", 1)
	c, err := Dial(context.Background(), DialOptions{
		Host: p.Host, Port: p.Port, TLS: p.TLS, Username: p.Username, Password: p.Password,
		TLSConfig: s.ClientTLSConfig(), Timeout: 2 * time.Second, CommandTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestConnBodyStatGroupDate(t *testing.T) {
	s := nntptest.Start(t, nntptest.Options{Username: "user", Password: "pw", TLS: true})
	data := randData(1, 750_000)
	if _, err := s.AddYenc("seg1@test", data, yenc.EncodeOptions{Name: "a.bin", Part: 1, Total: 2, Begin: 1, FileSize: 1_000_000}); err != nil {
		t.Fatal(err)
	}
	s.AddArticle("text@test", []byte(".dot first\n..double\nplain\n"))
	c := dialServer(t, s)
	if !c.PostingAllowed {
		t.Fatal("expected 200 greeting")
	}

	part, err := c.BodyDecode(context.Background(), "seg1@test", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(part.Data, data) || part.Header.End != 750_000 || part.Header.Size != 1_000_000 {
		t.Fatalf("header %+v", part.Header)
	}

	br, err := c.Body(context.Background(), "<text@test>")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(br)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != ".dot first\r\n..double\r\nplain\r\n" {
		t.Fatalf("raw %q", raw)
	}
	br.Close()

	// ReadLine path, then early Close drains the rest.
	br, err = c.Body(context.Background(), "text@test")
	if err != nil {
		t.Fatal(err)
	}
	line, err := br.ReadLine()
	if err != nil || string(line) != ".dot first" {
		t.Fatalf("line %q %v", line, err)
	}
	if err := br.Close(); err != nil {
		t.Fatal(err)
	}

	ok, err := c.Stat(context.Background(), "seg1@test")
	if err != nil || !ok {
		t.Fatalf("stat %v %v", ok, err)
	}
	ok, err = c.Stat(context.Background(), "nope@test")
	if err != nil || ok {
		t.Fatalf("stat missing %v %v", ok, err)
	}
	if _, err := c.Body(context.Background(), "nope@test"); !errors.Is(err, ErrNoSuchArticle) {
		t.Fatalf("err = %v", err)
	}
	if err := c.Group(context.Background(), "alt.binaries.test"); err != nil || c.CurrentGroup() != "alt.binaries.test" {
		t.Fatal(err)
	}
	if d, err := c.Date(context.Background()); err != nil || time.Since(d) > time.Hour {
		t.Fatalf("date %v %v", d, err)
	}
	if c.Broken() {
		t.Fatal("connection should be healthy")
	}
	if err := c.Quit(); err != nil {
		t.Fatal(err)
	}
}

func TestConnInvalidMessageID(t *testing.T) {
	s := nntptest.Start(t, nntptest.Options{})
	c := dialServer(t, s)
	for _, id := range []string{"", "a b@c", "a\r\nQUIT@x", "<>"} {
		if _, err := c.Body(context.Background(), id); !errors.Is(err, ErrInvalidMessageID) {
			t.Fatalf("%q: err = %v", id, err)
		}
	}
	if c.Broken() {
		t.Fatal("invalid ids must not break the connection")
	}
}

func TestConnAuthFailure(t *testing.T) {
	s := nntptest.Start(t, nntptest.Options{Username: "user", Password: "pw"})
	p := s.Provider("t", 1)
	_, err := Dial(context.Background(), DialOptions{Host: p.Host, Port: p.Port, Username: "user", Password: "bad"})
	if !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("err = %v", err)
	}
	// No credentials: the first article command gets 480.
	c, err := Dial(context.Background(), DialOptions{Host: p.Host, Port: p.Port})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Stat(context.Background(), "x@y"); !errors.Is(err, ErrAuthRequired) {
		t.Fatalf("err = %v", err)
	}
}

func TestConnAuthConnectionLimitIsNotBadPassword(t *testing.T) {
	s := nntptest.Start(t, nntptest.Options{Username: "user", Password: "pw"})
	s.SetFault(func(cmd, arg string) nntptest.Fault {
		if cmd == "AUTHINFO" && len(arg) > 4 && arg[:4] == "PASS" {
			return nntptest.Fault{Code: 481, Text: "too many connections for your account"}
		}
		return nntptest.Fault{}
	})
	p := s.Provider("t", 1)
	_, err := Dial(context.Background(), DialOptions{Host: p.Host, Port: p.Port, Username: p.Username, Password: p.Password})
	if !errors.Is(err, ErrServiceUnavailable) || errors.Is(err, ErrAuthFailed) {
		t.Fatalf("err = %v", err)
	}
}

func TestConnGreetingRefused(t *testing.T) {
	s := nntptest.Start(t, nntptest.Options{MaxConns: 1})
	_ = dialServer(t, s)
	p := s.Provider("t", 1)
	_, err := Dial(context.Background(), DialOptions{Host: p.Host, Port: p.Port})
	if !errors.Is(err, ErrServiceUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

func TestConnCancelMidCommand(t *testing.T) {
	s := nntptest.Start(t, nntptest.Options{})
	s.AddArticle("slow@x", []byte("data"))
	c := dialServer(t, s)
	s.SetLatency(600 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.Body(ctx, "slow@x")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 400*time.Millisecond {
		t.Fatal("cancellation did not interrupt the read")
	}
	if !c.Broken() {
		t.Fatal("interrupted connection must be broken")
	}
	if _, err := c.Stat(context.Background(), "slow@x"); !errors.Is(err, ErrConnBroken) {
		t.Fatalf("err = %v", err)
	}
}

func TestConnTruncatedBody(t *testing.T) {
	s := nntptest.Start(t, nntptest.Options{})
	_, _ = s.AddYenc("t@x", randData(2, 100_000), yenc.EncodeOptions{Name: "t"})
	s.SetFault(func(cmd, arg string) nntptest.Fault {
		if cmd == "BODY" {
			return nntptest.Fault{Truncate: true}
		}
		return nntptest.Fault{}
	})
	c := dialServer(t, s)
	_, err := c.BodyDecode(context.Background(), "t@x", nil)
	if err == nil || errors.Is(err, ErrCorrupt) || !isNetworkError(err) {
		t.Fatalf("err = %v", err)
	}
	if !c.Broken() {
		t.Fatal("must be broken")
	}
}

func TestConnCorruptKeepsConnection(t *testing.T) {
	s := nntptest.Start(t, nntptest.Options{})
	data := randData(3, 50_000)
	_, _ = s.AddYenc("c@x", data, yenc.EncodeOptions{Name: "c"})
	_ = s.Corrupt("c@x")
	s.AddArticle("plain@x", []byte("not yenc at all"))
	c := dialServer(t, s)
	_, err := c.BodyDecode(context.Background(), "c@x", nil)
	if !errors.Is(err, ErrCorrupt) || !errors.Is(err, yenc.ErrCRCMismatch) {
		t.Fatalf("err = %v", err)
	}
	if _, err := c.BodyDecode(context.Background(), "plain@x", nil); !errors.Is(err, ErrCorrupt) || !errors.Is(err, yenc.ErrNoHeader) {
		t.Fatalf("err = %v", err)
	}
	if c.Broken() {
		t.Fatal("corrupt articles must not break the connection")
	}
	if ok, err := c.Stat(context.Background(), "c@x"); !ok || err != nil {
		t.Fatal("connection unusable after corrupt article")
	}
}

func TestConnBytesCounted(t *testing.T) {
	s := nntptest.Start(t, nntptest.Options{})
	_, _ = s.AddYenc("b@x", randData(4, 10_000), yenc.EncodeOptions{Name: "b"})
	p := s.Provider("t", 1)
	var o DialOptions
	o.Host, o.Port = p.Host, p.Port
	var n atomic.Int64
	o.BytesRead = &n
	c, err := Dial(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.BodyDecode(context.Background(), "b@x", nil); err != nil {
		t.Fatal(err)
	}
	if n.Load() < 10_000 {
		t.Fatalf("counted %d", n.Load())
	}
}

func TestDialRefused(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().(*net.TCPAddr)
	ln.Close()
	_, err := Dial(context.Background(), DialOptions{Host: "127.0.0.1", Port: addr.Port, Timeout: time.Second})
	if err == nil || !isNetworkError(err) {
		t.Fatalf("err = %v", err)
	}
}
