// pmsdiff records and compares Plex Media Server HTTP responses.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ether-media/ether/internal/pmsconform"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}
func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: pmsdiff record|compare|live [flags]")
	}
	cmd := args[0]
	f := flag.NewFlagSet(cmd, flag.ContinueOnError)
	ref := f.String("reference", "", "fixture directory")
	refServer := f.String("reference-server", "", "live reference PMS URL")
	target := f.String("target", "", "target PMS URL")
	server := f.String("server", "", "PMS URL to record")
	token := f.String("token", "", "PMS access token")
	out := f.String("out", "testdata/pms", "output fixture directory")
	client := f.String("client", "all", "web|ios|appletv|all")
	format := f.String("format", "text", "text|json")
	timeout := f.Duration("timeout", 90*time.Second, "per-request timeout")
	concurrency := f.Int("concurrency", 2, "maximum concurrent requests")
	if e := f.Parse(args[1:]); e != nil {
		return e
	}
	if *timeout <= 0 || *concurrency < 1 {
		return fmt.Errorf("--timeout and --concurrency must be positive")
	}
	var report pmsconform.Report
	var err error
	switch cmd {
	case "record":
		if *server == "" {
			return fmt.Errorf("record requires --server")
		}
		report, err = record(*server, *token, *out, *client, *timeout, *concurrency)
	case "compare":
		if *target == "" || *ref == "" {
			return fmt.Errorf("compare requires --reference and --target")
		}
		report, err = compare(*ref, *target, *token, *client, *timeout, *concurrency)
	case "live":
		if *refServer == "" || *target == "" {
			return fmt.Errorf("live requires --reference-server and --target")
		}
		tmp, e := os.MkdirTemp("", "pmsdiff-reference-")
		if e != nil {
			return e
		}
		defer os.RemoveAll(tmp)
		report, err = record(*refServer, *token, tmp, *client, *timeout, *concurrency)
		if err == nil {
			var targetReport pmsconform.Report
			targetReport, err = compare(tmp, *target, *token, *client, *timeout, *concurrency)
			report = mergeReports(report, targetReport)
		}
	default:
		return fmt.Errorf("unknown command %q", cmd)
	}
	if err != nil {
		return err
	}
	emit(report, *format)
	if len(report.Differences) > 0 {
		return fmt.Errorf("report contains %d errors or blocking differences", len(report.Differences))
	}
	return nil
}
func profiles(sel string) []string {
	if sel == "all" || sel == "" {
		return []string{"web", "ios", "appletv"}
	}
	return strings.Split(sel, ",")
}
func record(base, token, out, sel string, timeout time.Duration, concurrency int) (pmsconform.Report, error) {
	client := &http.Client{Timeout: timeout}
	ctx, cancel := context.WithTimeout(context.Background(), 24*time.Hour)
	defer cancel()
	report := pmsconform.Report{}
	var mu sync.Mutex
	for _, profile := range profiles(sel) {
		ids := pmsconform.Discover(ctx, client, base, token, profile, nil)
		for _, w := range ids.Warnings {
			report.Discovery = append(report.Discovery, profile+": "+w)
		}
		if e := saveDiscovery(out, profile, ids); e != nil {
			return report, e
		}
		type job struct {
			req  pmsconform.Request
			json bool
		}
		jobs := make(chan job)
		var wg sync.WaitGroup
		var fixtureMu sync.Mutex
		for i := 0; i < concurrency; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := range jobs {
					resp, e := pmsconform.Fetch(ctx, client, base, token, j.req, profile, ids.Values, j.json)
					filename := fixtureName(profile, j.req.Path, j.json)
					if e != nil {
						mu.Lock()
						report.Differences = append(report.Differences, errorDifference(profile, j.req, e))
						fmt.Fprintf(os.Stderr, "ERROR %s %s: %v\n", profile, j.req.Path, e)
						mu.Unlock()
						continue
					}
					fixtureMu.Lock()
					n, e := pmsconform.SaveFixtureTrimmed(filepath.Join(out, profile), filename, resp)
					fixtureMu.Unlock()
					mu.Lock()
					if e != nil {
						report.Differences = append(report.Differences, errorDifference(profile, j.req, e))
						fmt.Fprintf(os.Stderr, "ERROR saving %s %s: %v\n", profile, j.req.Path, e)
					} else if n > 0 {
						report.Discovery = append(report.Discovery, fmt.Sprintf("%s %s trimmed to first 50 entries", profile, j.req.Path))
					}
					mu.Unlock()
				}
			}()
		}
		for _, req := range pmsconform.Corpus() {
			for _, jm := range []bool{false, true} {
				jobs <- job{req, jm}
			}
		}
		close(jobs)
		wg.Wait()
	}
	if e := writeManifest(out, report); e != nil {
		return report, e
	}
	return report, nil
}
func compare(dir, base, token, sel string, timeout time.Duration, concurrency int) (pmsconform.Report, error) {
	client := &http.Client{Timeout: timeout}
	ctx, cancel := context.WithTimeout(context.Background(), 24*time.Hour)
	defer cancel()
	out := pmsconform.Report{}
	for _, profile := range profiles(sel) {
		refIDs, e := loadDiscovery(dir, profile)
		if e != nil {
			refIDs = pmsconform.DiscoveredIDs{Values: map[string]string{}, Warnings: []string{"reference discovery unavailable; fallback IDs may be used"}}
		}
		targetIDs := pmsconform.Discover(ctx, client, base, token, profile, &refIDs)
		for _, w := range refIDs.Warnings {
			out.Discovery = append(out.Discovery, profile+" reference: "+w)
		}
		for _, w := range targetIDs.Warnings {
			out.Discovery = append(out.Discovery, profile+" target: "+w)
		}
		type job struct {
			req  pmsconform.Request
			json bool
		}
		type result struct{ diff []pmsconform.Difference }
		jobs := make(chan job)
		results := make(chan result)
		var wg sync.WaitGroup
		for i := 0; i < concurrency; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := range jobs {
					name := fixtureName(profile, j.req.Path, j.json)
					a, e := pmsconform.LoadFixture(filepath.Join(dir, profile, name+".json"))
					if e != nil {
						d := errorDifference(profile, j.req, fmt.Errorf("load fixture: %w", e))
						fmt.Fprintf(os.Stderr, "ERROR %s: %v\n", d.Path, d.Reference)
						results <- result{[]pmsconform.Difference{d}}
						continue
					}
					b, e := pmsconform.Fetch(ctx, client, base, token, j.req, profile, targetIDs.Values, j.json)
					if e != nil {
						d := errorDifference(profile, j.req, e)
						fmt.Fprintf(os.Stderr, "ERROR %s: %v\n", d.Path, d.Reference)
						results <- result{[]pmsconform.Difference{d}}
						continue
					}
					r := pmsconform.Compare(a, b)
					results <- result{r.Differences}
				}
			}()
		}
		go func() {
			for _, req := range pmsconform.Corpus() {
				for _, jm := range []bool{false, true} {
					jobs <- job{req, jm}
				}
			}
			close(jobs)
			wg.Wait()
			close(results)
		}()
		for r := range results {
			out.Differences = append(out.Differences, r.diff...)
		}
	}
	return out, nil
}
func fixtureName(profile, path string, jsonMode bool) string {
	ext := "xml"
	if jsonMode {
		ext = "json"
	}
	n := strings.NewReplacer("/", "_", ":", "", "?", "_", "=", "-").Replace(profile + "_" + ext + path)
	return filepath.Base(n)
}
func errorDifference(profile string, req pmsconform.Request, e error) pmsconform.Difference {
	return pmsconform.Difference{Path: profile + " " + req.Method + " " + req.Path, Kind: "request_error", Reference: string(pmsconform.Redact([]byte(e.Error()))), Blocking: true}
}
func mergeReports(a, b pmsconform.Report) pmsconform.Report {
	a.Differences = append(a.Differences, b.Differences...)
	a.Discovery = append(a.Discovery, b.Discovery...)
	return a
}
func emit(r pmsconform.Report, format string) {
	if format == "json" {
		b, _ := json.MarshalIndent(r, "", "  ")
		fmt.Println(string(b))
		return
	}
	for _, x := range r.Discovery {
		fmt.Printf("INFO: %s\n", x)
	}
	if len(r.Differences) == 0 {
		fmt.Println("No errors or structural differences.")
		return
	}
	for _, d := range r.Differences {
		fmt.Printf("BLOCK %s: %s (%v)\n", d.Path, d.Kind, d.Reference)
	}
}
func writeManifest(dir string, r pmsconform.Report) error {
	if e := os.MkdirAll(dir, 0755); e != nil {
		return e
	}
	trimmed, discovery := []string{}, []string{}
	for _, line := range r.Discovery {
		if strings.Contains(line, "trimmed to first 50") {
			trimmed = append(trimmed, line)
		} else {
			discovery = append(discovery, line)
		}
	}
	counts := map[string]int{}
	for _, profile := range []string{"web", "ios", "appletv"} {
		entries, _ := os.ReadDir(filepath.Join(dir, profile))
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") && !strings.HasPrefix(entry.Name(), "_") {
				counts[profile]++
			}
		}
	}
	data := map[string]any{"fixturePolicy": "Response listings are trimmed to the first 50 entries per repeated container child.", "trimmedResponses": trimmed, "fixtureCounts": counts, "discoveryWarnings": discovery, "requestErrors": r.Differences}
	b, e := json.MarshalIndent(data, "", "  ")
	if e != nil {
		return e
	}
	return os.WriteFile(filepath.Join(dir, "manifest.json"), b, 0644)
}
func saveDiscovery(dir, profile string, ids pmsconform.DiscoveredIDs) error {
	if e := os.MkdirAll(filepath.Join(dir, profile), 0755); e != nil {
		return e
	}
	b, e := json.MarshalIndent(ids, "", "  ")
	if e != nil {
		return e
	}
	return os.WriteFile(filepath.Join(dir, profile, "_discovery.json"), b, 0644)
}
func loadDiscovery(dir, profile string) (pmsconform.DiscoveredIDs, error) {
	b, e := os.ReadFile(filepath.Join(dir, profile, "_discovery.json"))
	if e != nil {
		return pmsconform.DiscoveredIDs{}, e
	}
	var d pmsconform.DiscoveredIDs
	e = json.Unmarshal(b, &d)
	return d, e
}
