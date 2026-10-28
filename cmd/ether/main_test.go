package main

import (
	"bytes"
	"flag"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseInterleaved(t *testing.T) {
	for _, tc := range []struct {
		args    []string
		wantPos []string
		wantDir string
	}{
		{[]string{"install", "--data-dir", "/x"}, []string{"install"}, "/x"},
		{[]string{"--data-dir=/y", "install"}, []string{"install"}, "/y"},
		{[]string{"status"}, []string{"status"}, "def"},
		{nil, nil, "def"},
	} {
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		dir := fs.String("data-dir", "def", "")
		got := parseInterleaved(fs, tc.args)
		if !reflect.DeepEqual(got, tc.wantPos) || *dir != tc.wantDir {
			t.Errorf("%v: positional=%v dir=%q, want %v %q", tc.args, got, *dir, tc.wantPos, tc.wantDir)
		}
	}
}

func TestDataDirExplicit(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		env  string
		want bool
	}{
		{"defaulted", []string{"service", "install"}, "", false},
		{"flag after subcommand", []string{"service", "install", "--data-dir", `D:\Ether`}, "", true},
		{"flag before subcommand", []string{"--data-dir=/srv/ether", "service", "install"}, "", true},
		{"environment", []string{"service", "install"}, "/srv/ether", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs := flag.NewFlagSet("t", flag.ContinueOnError)
			fs.SetOutput(io.Discard)
			fs.String("data-dir", "def", "")
			_ = fs.Parse(tc.args)
			parseInterleaved(fs, fs.Args())
			getenv := func(k string) string {
				if k == "ETHER_DATA_DIR" {
					return tc.env
				}
				return ""
			}
			if got := dataDirExplicit(fs, getenv); got != tc.want {
				t.Fatalf("dataDirExplicit = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLogLevelForOnlyReadsConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	doc := []byte(`{"server":{"logLevel":"debug"}}`)
	if err := os.WriteFile(path, doc, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := logLevelFor("", dir); got != slog.LevelDebug {
		t.Fatalf("level = %v, want debug", got)
	}
	if b, _ := os.ReadFile(path); !bytes.Equal(b, doc) {
		t.Fatalf("config.json rewritten while resolving the log level:\n%s", b)
	}
	if got := logLevelFor("warn", dir); got != slog.LevelWarn {
		t.Fatalf("flag level = %v, want warn", got)
	}
	missing := filepath.Join(dir, "missing")
	if got := logLevelFor("", missing); got != slog.LevelInfo {
		t.Fatalf("default level = %v", got)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("data dir created while resolving the log level")
	}
}
