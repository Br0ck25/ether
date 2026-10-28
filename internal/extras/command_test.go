package extras

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
)

func TestValidateSiteKey(t *testing.T) {
	ok := [][2]string{{"YouTube", "vKQi3bBA1y8"}, {"youtube", "abc-DEF_123"}, {"Vimeo", "123456789"}}
	for _, c := range ok {
		if !ValidateSiteKey(c[0], c[1]) {
			t.Errorf("want ok %v", c)
		}
	}
	bad := [][2]string{
		{"YouTube", "short"}, {"YouTube", "vKQi3bBA1y8;rm"}, {"YouTube", "$(reboot)xx"},
		{"Vimeo", "abc"}, {"Dailymotion", "x123456"}, {"", "vKQi3bBA1y8"},
	}
	for _, c := range bad {
		if ValidateSiteKey(c[0], c[1]) {
			t.Errorf("want bad %v", c)
		}
		if _, ok := PageURL(c[0], c[1]); ok {
			t.Errorf("page url for bad %v", c)
		}
	}
	if u, ok := PageURL("YouTube", "vKQi3bBA1y8"); !ok || u != "https://www.youtube.com/watch?v=vKQi3bBA1y8" {
		t.Errorf("page url %q", u)
	}
}

func TestRunResolverFakeRunner(t *testing.T) {
	cfg := config.Extras{ResolverCommand: "yt-dlp"}
	e := Extra{Site: "YouTube", SiteKey: "vKQi3bBA1y8", Source: SourceCommand}
	var gotName string
	var gotArgs []string
	runner := func(_ context.Context, name string, args []string) ([]byte, error) {
		gotName, gotArgs = name, append([]string(nil), args...)
		return []byte("https://cdn.example/video.mp4?expire=" + fmt.Sprint(testNow.Add(2*time.Hour).Unix()) + "\n"), nil
	}
	r, err := runResolver(context.Background(), cfg, e, runner, func() time.Time { return testNow })
	if err != nil {
		t.Fatal(err)
	}
	if gotName != "yt-dlp" || len(gotArgs) == 0 || gotArgs[len(gotArgs)-1] != "https://www.youtube.com/watch?v=vKQi3bBA1y8" {
		t.Errorf("invocation %q %v", gotName, gotArgs)
	}
	if !strings.HasPrefix(r.URL, "https://cdn.example/") || !r.Expires.Equal(testNow.Add(2*time.Hour)) || r.Container != "mp4" {
		t.Errorf("resolved %+v", r)
	}

	// JSON shape with headers.
	runner = func(_ context.Context, _ string, _ []string) ([]byte, error) {
		return []byte(`{"url":"https://cdn.example/v.webm","headers":{"User-Agent":"x"},"expires":1893456000}`), nil
	}
	r, err = runResolver(context.Background(), cfg, e, runner, func() time.Time { return testNow })
	if err != nil || r.Headers["User-Agent"] != "x" || r.Container != "webm" {
		t.Fatalf("json resolved %v %+v", err, r)
	}
	// Expiry is capped at 6h.
	if r.Expires.After(testNow.Add(maxURLTTL)) {
		t.Errorf("expiry not capped: %v", r.Expires)
	}

	// Multiple URLs (split audio/video) and junk are errors.
	for _, out := range []string{"https://a.example/v.mp4\nhttps://a.example/a.m4a\n", "nope", "", "file:///etc/passwd\n"} {
		runner = func(_ context.Context, _ string, _ []string) ([]byte, error) { return []byte(out), nil }
		if _, err := runResolver(context.Background(), cfg, e, runner, func() time.Time { return testNow }); err == nil {
			t.Errorf("want error for %q", out)
		}
	}
	// Invalid site key never reaches the runner.
	called := false
	runner = func(_ context.Context, _ string, _ []string) ([]byte, error) { called = true; return nil, nil }
	bad := Extra{Site: "YouTube", SiteKey: "x; rm -rf /", Source: SourceCommand}
	if _, err := runResolver(context.Background(), cfg, bad, runner, func() time.Time { return testNow }); err == nil || called {
		t.Errorf("bad key reached runner: %v %v", err, called)
	}
}

func TestRunResolverTimeoutAndCache(t *testing.T) {
	cfg := config.Extras{Enabled: true, ResolverCommand: "slow", ResolverTimeout: config.Duration(50 * time.Millisecond)}
	s := New(Options{Config: func() config.Extras { return cfg }, LookPath: lookOK,
		Now: func() time.Time { return testNow },
		Runner: func(ctx context.Context, _ string, _ []string) ([]byte, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}})
	e := Extra{Site: "YouTube", SiteKey: "vKQi3bBA1y8", Source: SourceCommand}
	start := time.Now()
	if _, err := s.Resolve(context.Background(), e); err == nil {
		t.Fatal("want timeout error")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("timeout not enforced")
	}

	calls := 0
	s2 := New(Options{Config: enabledCfg("yt-dlp"), LookPath: lookOK,
		Now: func() time.Time { return testNow },
		Runner: func(_ context.Context, _ string, _ []string) ([]byte, error) {
			calls++
			return []byte("https://cdn.example/v.mp4"), nil
		}})
	for i := 0; i < 3; i++ {
		if _, err := s2.Resolve(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Errorf("resolver calls %d, want cached 1", calls)
	}
}

// TestExecRunnerWithScript runs a real (offline) executable through the
// no-shell runner, yt-dlp style.
func TestExecRunnerWithScript(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-resolver")
	body := "#!/bin/sh\nprintf 'https://cdn.example/from-script.mp4\\n'\n"
	if runtime.GOOS == "windows" {
		// Windows runs batch files directly; a shebang script is not executable.
		script += ".cmd"
		body = "@echo https://cdn.example/from-script.mp4\r\n"
	}
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.Extras{Enabled: true, ResolverCommand: script}
	s := New(Options{Config: func() config.Extras { return cfg }, Now: func() time.Time { return testNow }})
	r, err := s.Resolve(context.Background(), Extra{Site: "YouTube", SiteKey: "vKQi3bBA1y8", Source: SourceCommand})
	if err != nil {
		t.Fatal(err)
	}
	if r.URL != "https://cdn.example/from-script.mp4" {
		t.Errorf("url %q", r.URL)
	}
	if got := s.PrewarmBytes(); got != 48<<20 {
		t.Errorf("prewarm bytes %d", got)
	}
}
