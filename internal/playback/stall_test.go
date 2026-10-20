package playback

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestStallWriterFailsNonReadingClient connects a client that sends a
// request and never reads the body: the handler's writes must error out
// shortly after the socket buffers fill instead of blocking forever.
func TestStallWriterFailsNonReadingClient(t *testing.T) {
	done := make(chan error, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w = newStallWriter(w, 200*time.Millisecond)
		buf := make([]byte, 256<<10)
		for {
			if _, err := w.Write(buf); err != nil {
				done <- err
				return
			}
		}
	}))
	defer srv.Close()

	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: x\r\n\r\n")
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("write succeeded")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("handler still blocked writing to a client that stopped reading")
	}
}

// TestStallWriterKeepsSlowReader checks a client that keeps reading, if
// slowly, is never cut off, and that deadlines do not leak to the next
// request on a kept-alive connection.
func TestStallWriterKeepsSlowReader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w = newStallWriter(w, 300*time.Millisecond)
		w.Header().Set("Content-Length", "8")
		for i := 0; i < 4; i++ {
			w.Write([]byte("ab"))
			w.(http.Flusher).Flush()
			time.Sleep(100 * time.Millisecond)
		}
	}))
	defer srv.Close()
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	br := bufio.NewReader(conn)
	for i := 0; i < 2; i++ {
		fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: x\r\n\r\n")
		resp, err := http.ReadResponse(br, nil)
		if err != nil {
			t.Fatal(err)
		}
		body := make([]byte, 8)
		if _, err := io.ReadFull(resp.Body, body); err != nil || string(body) != "abababab" {
			t.Fatalf("request %d body %q err %v", i, body, err)
		}
		resp.Body.Close()
		time.Sleep(400 * time.Millisecond) // past the last deadline
	}
}
