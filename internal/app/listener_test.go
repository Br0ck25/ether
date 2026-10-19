package app

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"
)

// scriptedListener returns queued Accept results, then blocks until Close.
type scriptedListener struct {
	mu      sync.Mutex
	results []acceptResult
	calls   int
	closed  chan struct{}
	once    sync.Once
}

type acceptResult struct {
	conn net.Conn
	err  error
}

func newScriptedListener(results ...acceptResult) *scriptedListener {
	return &scriptedListener{results: results, closed: make(chan struct{})}
}

func (l *scriptedListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	l.calls++
	if len(l.results) > 0 {
		r := l.results[0]
		l.results = l.results[1:]
		l.mu.Unlock()
		return r.conn, r.err
	}
	l.mu.Unlock()
	<-l.closed
	return nil, net.ErrClosed
}

func (l *scriptedListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *scriptedListener) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)} }

type tempErr struct{}

func (tempErr) Error() string   { return "temporary" }
func (tempErr) Timeout() bool   { return false }
func (tempErr) Temporary() bool { return true }

func TestDualListenerSurvivesTemporaryAcceptErrors(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()
	emfile := &net.OpError{Op: "accept", Net: "tcp", Err: os.NewSyscallError("accept", syscall.EMFILE)}
	ln := newScriptedListener(
		acceptResult{err: emfile},
		acceptResult{err: tempErr{}},
		acceptResult{err: fmt.Errorf("wrapped: %w", syscall.ECONNABORTED)},
		acceptResult{conn: server},
	)
	dl := newDualListener(ln, func() *tls.Config { return nil })
	defer dl.Close()
	go client.Write([]byte("GET / HTTP/1.1\r\n\r\n"))

	type res struct {
		c   net.Conn
		err error
	}
	got := make(chan res, 1)
	go func() { c, err := dl.Accept(); got <- res{c, err} }()
	select {
	case r := <-got:
		if r.err != nil {
			t.Fatalf("Accept returned %v; temporary errors must be retried", r.err)
		}
		buf := make([]byte, 3)
		if _, err := r.c.Read(buf); err != nil || string(buf) != "GET" {
			t.Fatalf("read %q, %v", buf, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no connection accepted after temporary errors")
	}
}

func TestDualListenerReportsPermanentError(t *testing.T) {
	boom := errors.New("permanent")
	dl := newDualListener(newScriptedListener(acceptResult{err: boom}), func() *tls.Config { return nil })
	defer dl.Close()
	if _, err := dl.Accept(); !errors.Is(err, boom) {
		t.Fatalf("Accept = %v, want permanent error", err)
	}
}

func TestDualListenerCloseStopsBackoff(t *testing.T) {
	results := make([]acceptResult, 1000)
	for i := range results {
		results[i] = acceptResult{err: tempErr{}}
	}
	dl := newDualListener(newScriptedListener(results...), func() *tls.Config { return nil })
	time.Sleep(20 * time.Millisecond)
	dl.Close()
	if _, err := dl.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Accept after Close = %v, want net.ErrClosed", err)
	}
}

func TestIsTemporaryAcceptErr(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{net.ErrClosed, false},
		{&net.OpError{Op: "accept", Err: net.ErrClosed}, false},
		{errors.New("other"), false},
		{tempErr{}, true},
		{os.NewSyscallError("accept", syscall.ENFILE), true},
		{syscall.ECONNRESET, true},
	} {
		if got := isTemporaryAcceptErr(tc.err); got != tc.want {
			t.Errorf("isTemporaryAcceptErr(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}
