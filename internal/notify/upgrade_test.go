package notify

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"net/textproto"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/store"
)

func TestMain(m *testing.M) {
	if len(os.Args) == 2 && os.Args[1] == "notification-monitor" {
		if RunMonitor(os.Stdin) != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}
func newTestService(t *testing.T) *Service {
	t.Helper()
	db, err := store.OpenMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return New(db, Options{})
}
func TestConnectionTestIsImmediateAndRetainsSafeFailure(t *testing.T) {
	for _, status := range []int{204, 401} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			s := newTestService(t)
			called := false
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				w.WriteHeader(status)
				_, _ = io.WriteString(w, "private-secret")
			}))
			defer up.Close()
			// Test deliberately works on a paused channel, with a one-week digest.
			if err := s.Save(context.Background(), Channel{ID: "x", Kind: "webhook", Endpoint: up.URL + "/private-secret", DigestMinutes: 10080}); err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, httptest.NewRequest("POST", "/x/test", nil))
			if !called {
				t.Fatal("test only queued")
			}
			if status == 204 && w.Code != 200 || status == 401 && w.Code != 502 {
				t.Fatalf("%d: %s", w.Code, w.Body.String())
			}
			w = httptest.NewRecorder()
			s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
			if strings.Contains(w.Body.String(), "private-secret") || !strings.Contains(w.Body.String(), `"kind":"test"`) {
				t.Fatal(w.Body.String())
			}
			if status == 401 && !strings.Contains(w.Body.String(), "HTTP 401") {
				t.Fatal("no useful failure detail")
			}
		})
	}
}
func TestDeleteRetainsHistoryAndCancelsPending(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	if err := s.Save(ctx, Channel{ID: "x", Kind: "webhook", Endpoint: "https://example.test", Enabled: true, Events: []string{"*"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Emit(ctx, Event{Kind: "playback.error"}); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("DELETE", "/x", nil))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var status string
	if err := s.db.QueryRow(`SELECT status FROM notify_queue`).Scan(&status); err != nil || status != "cancelled" {
		t.Fatalf("%s %v", status, err)
	}
}
func TestLifecycleAndNewsletterDoNotWaitForOrJoinDigest(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	var got []Event
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var e Event
		_ = json.NewDecoder(r.Body).Decode(&e)
		got = append(got, e)
	}))
	defer up.Close()
	c := Channel{ID: "x", Kind: "webhook", Endpoint: up.URL, Enabled: true, Events: []string{"*"}, DigestMinutes: 60}
	if err := s.Save(ctx, c); err != nil {
		t.Fatal(err)
	}
	_ = s.Emit(ctx, Event{Kind: "playback.error", Message: "wait"})
	_ = s.Emit(ctx, Event{Kind: "server.started", Message: "ready"})
	if err := s.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Kind != "server.started" {
		t.Fatalf("%+v", got)
	}
	if deliveryDue(c, Event{Kind: "newsletter.weekly"}, time.Unix(1, 0)).Unix() != 1 {
		t.Fatal("newsletter was delayed")
	}
}
func TestBrowserIdentitySubscriptionRedactionAndExpiredDelivery(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	priv, pub, err := s.pushKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p2, u2, err := New(s.db, Options{}).pushKeys(ctx)
	if err != nil || priv != p2 || pub != u2 {
		t.Fatal("VAPID identity changed")
	}
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	_, _ = rand.Read(auth)
	sub := &PushSubscription{Endpoint: "https://push.example.test/private", Keys: PushKeys{Auth: base64.RawURLEncoding.EncodeToString(auth), P256dh: base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes())}}
	c := Channel{ID: "browser", Kind: "webpush", Enabled: true, Subscription: sub, Events: []string{"server.crash"}}
	if err := s.Save(ctx, c); err != nil {
		t.Fatal(err)
	}
	channels, _ := s.Channels(ctx)
	raw, _ := json.Marshal(channels)
	if strings.Contains(string(raw), "/private") || strings.Contains(string(raw), sub.Keys.Auth) {
		t.Fatal("subscription leaked")
	}
	if err := s.Save(ctx, channels[0]); err != nil {
		t.Fatal(err)
	}
	sent := false
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent = true
		if r.Header.Get("Content-Encoding") != "aes128gcm" || r.Header.Get("Authorization") == "" {
			t.Error("unencrypted or unauthenticated push")
		}
		w.WriteHeader(410)
	}))
	defer up.Close()
	c.Subscription.Endpoint = up.URL
	s.opts.Client = up.Client()
	if err := s.deliver(ctx, c, Event{Title: "Server crashed"}); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("%v", err)
	}
	if !sent {
		t.Fatal("push not sent")
	}
}
func TestIndependentCrashWatcherAndCleanExit(t *testing.T) {
	for _, clean := range []bool{true, false} {
		t.Run(map[bool]string{true: "clean", false: "crash"}[clean], func(t *testing.T) {
			s := newTestService(t)
			ctx := context.Background()
			received := make(chan Event, 4)
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var e Event
				_ = json.NewDecoder(r.Body).Decode(&e)
				received <- e
			}))
			defer up.Close()
			exe, _ := os.Executable()
			finish, err := s.WatchProcess(exe)
			if err != nil {
				t.Fatal(err)
			}
			// Changes made after the watcher starts must be included.
			if err := s.Save(ctx, Channel{ID: "x", Kind: "webhook", Endpoint: up.URL, Enabled: true, Events: []string{"server.crash"}, DigestMinutes: 10080}); err != nil {
				_ = finish(true)
				t.Fatal(err)
			}
			if err := finish(clean); err != nil {
				t.Fatal(err)
			}
			if clean {
				select {
				case <-received:
					t.Fatal("clean exit alerted")
				default:
				}
			} else {
				select {
				case e := <-received:
					if e.Kind != "server.crash" {
						t.Fatal(e)
					}
				default:
					t.Fatal("crash did not alert")
				}
			}
		})
	}
}
func TestFatalParentHelper(t *testing.T) {
	if os.Getenv("ETHER_TEST_CRASH_PARENT") == "" {
		return
	}
	s := newTestService(t)
	if err := s.Save(context.Background(), Channel{ID: "x", Kind: "webhook", Endpoint: os.Getenv("ETHER_TEST_CRASH_URL"), Enabled: true, Events: []string{"server.crash"}}); err != nil {
		os.Exit(4)
	}
	exe, _ := os.Executable()
	if _, err := s.WatchProcess(exe); err != nil {
		os.Exit(5)
	}
	// A fatal exit cannot run any shutdown callbacks.
	os.Exit(2)
}
func TestWatcherSurvivesFatalParent(t *testing.T) {
	received := make(chan struct{}, 1)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received <- struct{}{} }))
	defer up.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	exe, _ := os.Executable()
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestFatalParentHelper$")
	cmd.Env = append(os.Environ(), "ETHER_TEST_CRASH_PARENT=1", "ETHER_TEST_CRASH_URL="+up.URL)
	if err := cmd.Run(); err == nil {
		t.Fatal("parent did not exit")
	}
	select {
	case <-received:
	case <-ctx.Done():
		t.Fatal("watcher did not survive parent")
	}
}
func TestSMTPWithTLSRecipientsAndUnicode(t *testing.T) {
	for _, mode := range []string{"tls", "starttls"} {
		t.Run(mode, func(t *testing.T) {
			fixture := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
			defer fixture.Close()
			cert := fixture.TLS.Certificates[0]
			roots := x509.NewCertPool()
			parsed, _ := x509.ParseCertificate(cert.Certificate[0])
			roots.AddCert(parsed)
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			result := make(chan string, 1)
			recipients := make(chan int, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				tlsCfg := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
				if mode == "tls" {
					conn = tls.Server(conn, tlsCfg)
				}
				tp := textproto.NewConn(conn)
				_ = tp.PrintfLine("220 test SMTP")
				count := 0
				for {
					line, err := tp.ReadLine()
					if err != nil {
						return
					}
					switch {
					case strings.HasPrefix(line, "EHLO"):
						if mode == "starttls" {
							_ = tp.PrintfLine("250-test\r\n250 STARTTLS")
						} else {
							_ = tp.PrintfLine("250 test")
						}
					case line == "STARTTLS":
						_ = tp.PrintfLine("220 ready")
						conn = tls.Server(conn, tlsCfg)
						tp = textproto.NewConn(conn)
					case strings.HasPrefix(line, "MAIL FROM:"):
						_ = tp.PrintfLine("250 sender")
					case strings.HasPrefix(line, "RCPT TO:"):
						count++
						_ = tp.PrintfLine("250 recipient")
					case line == "DATA":
						_ = tp.PrintfLine("354 go")
						body, err := io.ReadAll(tp.DotReader())
						if err != nil {
							return
						}
						result <- string(body)
						recipients <- count
						_ = tp.PrintfLine("250 queued")
					case line == "QUIT":
						_ = tp.PrintfLine("221 bye")
						return
					default:
						_ = tp.PrintfLine("500 unexpected")
					}
				}
			}()
			c := Channel{Kind: "smtp", Endpoint: listener.Addr().String(), SMTPMode: mode, From: "Ether <owner@example.test>", To: "a@example.test, B <b@example.test>"}
			if err := validateChannel(c); err != nil {
				t.Fatal(err)
			}
			if err := sendSMTPWithTLS(context.Background(), c, Event{Title: "Café 🎬", HTML: "<p>Résumé</p>"}, &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}); err != nil {
				t.Fatal(err)
			}
			raw := <-result
			if <-recipients != 2 {
				t.Fatal("missing recipients")
			}
			message, err := mail.ReadMessage(strings.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(message.Header.Get("Subject"), "=?utf-8?") || message.Header.Get("Content-Transfer-Encoding") != "quoted-printable" || !strings.Contains(raw, "text/html") {
				t.Fatal(raw)
			}
		})
	}
}
func TestHeartbeatSecretsAndDelivery(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	calls := 0
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" {
			t.Error(r.Method)
		}
	}))
	defer up.Close()
	s.opts.Client = up.Client()
	h := Heartbeat{Enabled: true, Endpoint: up.URL + "/secret"}
	if err := s.SaveHeartbeat(ctx, h); err != nil {
		t.Fatal(err)
	}
	redacted, err := s.HeartbeatSettings(ctx, true)
	if err != nil || redacted.Endpoint != Redacted {
		t.Fatal(redacted, err)
	}
	if err := s.SaveHeartbeat(ctx, redacted); err != nil {
		t.Fatal(err)
	}
	if err := s.Heartbeat(ctx); err != nil || calls != 1 {
		t.Fatal(err, calls)
	}
	redacted.Enabled = false
	_ = s.SaveHeartbeat(ctx, redacted)
	_ = s.Heartbeat(ctx)
	if calls != 1 {
		t.Fatal("disabled heartbeat sent")
	}
}

func TestCrashDuringSnapshotKeepsLastCompleteDestinations(t *testing.T) {
	called := false
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer up.Close()
	frame, _ := json.Marshal(monitorFrame{Channels: []Channel{{Kind: "webhook", Endpoint: up.URL, Enabled: true, Events: []string{"server.crash"}}}})
	input := string(frame) + "\n" + `{"channels":[{"kind":"webhook"`
	if err := RunMonitor(strings.NewReader(input)); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("truncated update lost crash notification")
	}
}

func TestDrainWaitRespectsShutdownDeadline(t *testing.T) {
	s := newTestService(t)
	s.drainMu <- struct{}{}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := s.Drain(ctx); err != context.DeadlineExceeded {
		t.Fatalf("%v", err)
	}
	<-s.drainMu
}
