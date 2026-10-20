package main

import (
	"flag"
	"io"
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
