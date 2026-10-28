package app

import (
	"context"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestUsenetProxyConfiguration(t *testing.T) {
	d, err := usenetProxyDialer("")
	if d != nil || err != nil {
		t.Fatalf("default must retain direct dialer: %v", err)
	}
	for _, raw := range []string{"https://user:secret@localhost:443", "socks5://localhost", "socks5://localhost:0", "socks5://localhost:65536", "socks5://localhost:1080/path", "socks5://localhost:1080?secret=value"} {
		d, err := usenetProxyDialer(raw)
		if d != nil || err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("invalid configuration not safely refused: %v", err)
		}
	}
}

func TestUsenetProxyPreservesProviderAddress(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	result := make(chan string, 1)
	go func() {
		c, err := l.Accept()
		if err != nil {
			result <- err.Error()
			return
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(3 * time.Second))
		g := make([]byte, 3)
		if _, err = io.ReadFull(c, g); err != nil {
			result <- err.Error()
			return
		}
		if string(g) != "\x05\x01\x00" {
			result <- "bad greeting"
			return
		}
		_, _ = c.Write([]byte{5, 0})
		h := make([]byte, 5)
		if _, err = io.ReadFull(c, h); err != nil {
			result <- err.Error()
			return
		}
		if string(h[:4]) != "\x05\x01\x00\x03" {
			result <- "address was resolved before the gateway"
			return
		}
		addr := make([]byte, int(h[4])+2)
		if _, err = io.ReadFull(c, addr); err != nil {
			result <- err.Error()
			return
		}
		if string(addr[:len(addr)-2]) != "news.example.invalid" || addr[len(addr)-2] != 2 || addr[len(addr)-1] != 51 {
			result <- "provider host or port changed"
			return
		}
		_, _ = c.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 1})
		_, _ = c.Write([]byte("200 proxy transport reached\r\n"))
		result <- "ok"
	}()
	dial, err := usenetProxyDialer("socks5://" + l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, err := dial(ctx, "tcp", "news.example.invalid:563")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	b := make([]byte, len("200 proxy transport reached\r\n"))
	if _, err := io.ReadFull(c, b); err != nil {
		t.Fatal(err)
	}
	if got := <-result; got != "ok" {
		t.Fatal(got)
	}
}

func TestUsenetProxyCancellationDoesNotDialDirect(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := l.Accept()
		if err == nil {
			accepted <- c
		}
	}()
	dial, err := usenetProxyDialer("socks5://" + l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	c, err := dial(ctx, "tcp", "news.example.invalid:563")
	if c != nil {
		c.Close()
		t.Fatal("unexpected direct fallback")
	}
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("handshake did not cancel promptly: %v", err)
	}
	select {
	case c := <-accepted:
		c.Close()
	case <-time.After(time.Second):
		t.Fatal("proxy was not contacted")
	}
}
