package plextv

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPresenceStreamAndEvents(t *testing.T) {
	sse := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sub/eventsource/42/machine-1" || r.URL.Query().Get("X-Plex-Token") != "srv-token" {
			http.Error(w, "bad path", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		fl := w.(http.Flusher)
		fmt.Fprint(w, ": \n\n: -1\n\n")
		fl.Flush()
		fmt.Fprint(w, `data: <Message address="1.2.3.4" port="32400" asyncIdentifier="abc" connectivity="1" command="notifyConnectivity"/>`+"\n\n")
		fl.Flush()
		<-r.Context().Done()
	}))
	defer sse.Close()
	host := strings.TrimPrefix(sse.URL, "http://")
	tv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/services/pubsub/servers" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, `<Servers size="1"><Server host=%q region="yyz"/></Servers>`, host)
	}))
	defer tv.Close()
	c, err := New(Options{BaseURL: tv.URL})
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan PubsubMessage, 1)
	p := NewPresence(PresenceOptions{
		Client:       c,
		Credentials:  func() (int64, string, string, bool) { return 42, "machine-1", "srv-token", true },
		OnMessage:    func(m PubsubMessage) { got <- m },
		StreamClient: sse.Client(),
		StreamURL: func(h string, owner int64, machine, token string) string {
			return fmt.Sprintf("http://%s/sub/eventsource/%d/%s?X-Plex-Token=%s", h, owner, machine, token)
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()
	select {
	case m := <-got:
		if m.Command != "notifyConnectivity" || m.AsyncIdentifier != "abc" || !m.Reachable() {
			t.Fatalf("message %+v", m)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no event")
	}
	if st := p.Status(); !st.Connected || st.Host != host {
		t.Fatalf("status %+v", st)
	}
	cancel()
	<-done
}

func TestRegistrarTrustsOnlyOutstandingConnectivityAnswer(t *testing.T) {
	r := &Registrar{next: map[string]time.Time{}, wake: make(chan struct{}, 1), log: discardLogger()}
	r.asyncID = "want"
	r.onPubsub(PubsubMessage{Command: "notifyConnectivity", AsyncIdentifier: "other", Connectivity: "1"})
	if r.st.PlexReachable != nil {
		t.Fatal("accepted an answer to someone else's request")
	}
	r.onPubsub(PubsubMessage{Command: "notifyConnectivity", AsyncIdentifier: "want", Connectivity: "0"})
	if r.st.PlexReachable == nil || *r.st.PlexReachable {
		t.Fatalf("reachable = %v", r.st.PlexReachable)
	}
}

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }
