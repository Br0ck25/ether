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
	refServer := f.String("reference-server", "", "live reference PMS URL (live mode)")
	target := f.String("target", "", "target PMS URL")
	server := f.String("server", "", "PMS URL to record")
	token := f.String("token", "", "PMS access token")
	out := f.String("out", "testdata/pms", "output fixture directory")
	client := f.String("client", "all", "web|ios|appletv|all")
	format := f.String("format", "text", "text|json")
	if e := f.Parse(args[1:]); e != nil {
		return e
	}
	if cmd == "record" {
		if *server == "" {
			return fmt.Errorf("record requires --server")
		}
		return record(*server, *token, *out, *client)
	}
	if cmd != "compare" && cmd != "live" {
		return fmt.Errorf("unknown command %q", cmd)
	}
	if *target == "" {
		return fmt.Errorf("%s requires --target", cmd)
	}
	if cmd == "live" {
		if *refServer == "" {
			return fmt.Errorf("live requires --reference-server")
		}
		tmp, e := os.MkdirTemp("", "pmsdiff-reference-")
		if e != nil {
			return e
		}
		defer os.RemoveAll(tmp)
		if e = record(*refServer, *token, tmp, *client); e != nil {
			return fmt.Errorf("record live reference: %w", e)
		}
		*ref = tmp
	}
	if *ref == "" {
		return fmt.Errorf("%s requires --reference fixture directory", cmd)
	}
	report, err := compare(*ref, *target, *token, *client)
	if err != nil {
		return err
	}
	if *format == "json" {
		b, _ := json.MarshalIndent(report, "", "  ")
		fmt.Println(string(b))
	} else {
		printReport(report)
	}
	if len(report.Differences) > 0 {
		return fmt.Errorf("blocking differences: %d", len(report.Differences))
	}
	return nil
}
func profiles(sel string) []string {
	if sel == "all" || sel == "" {
		return []string{"web", "ios", "appletv"}
	}
	return strings.Split(sel, ",")
}
func record(base, token, out, sel string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	c := &http.Client{Timeout: 20 * time.Second}
	for _, profile := range profiles(sel) {
		for _, req := range pmsconform.Corpus() {
			for _, jm := range []bool{false, true} {
				r, e := pmsconform.Fetch(ctx, c, base, token, req, profile, nil, jm)
				if e != nil {
					return fmt.Errorf("%s %s: %w", profile, req.Path, e)
				}
				ext := "xml"
				if jm {
					ext = "json"
				}
				name := strings.NewReplacer("/", "_", ":", "", "?", "_", "=", "-").Replace(profile + "_" + ext + req.Path)
				name = filepath.Base(name)
				if e = pmsconform.SaveFixture(filepath.Join(out, profile), name, r); e != nil {
					return e
				}
			}
		}
	}
	return nil
}
func compare(dir, base, token, sel string) (pmsconform.Report, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	c := &http.Client{Timeout: 20 * time.Second}
	out := pmsconform.Report{}
	for _, profile := range profiles(sel) {
		for _, req := range pmsconform.Corpus() {
			for _, jm := range []bool{false, true} {
				ext := "xml"
				if jm {
					ext = "json"
				}
				name := strings.NewReplacer("/", "_", ":", "", "?", "_", "=", "-").Replace(profile + "_" + ext + req.Path)
				name = filepath.Base(name)
				a, e := pmsconform.LoadFixture(filepath.Join(dir, profile, name+".json"))
				if e != nil {
					continue
				}
				b, e := pmsconform.Fetch(ctx, c, base, token, req, profile, nil, jm)
				if e != nil {
					return out, fmt.Errorf("%s %s: %w", profile, req.Path, e)
				}
				r := pmsconform.Compare(a, b)
				out.Differences = append(out.Differences, r.Differences...)
			}
		}
	}
	return out, nil
}
func printReport(r pmsconform.Report) {
	if len(r.Differences) == 0 {
		fmt.Println("No structural differences.")
		return
	}
	for _, d := range r.Differences {
		fmt.Printf("BLOCK %s: %s (reference=%v target=%v)\n", d.Path, d.Kind, d.Reference, d.Target)
	}
}
