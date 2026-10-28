package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunClusterUsageAndErrors(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		args []string
		code int
		want string
	}{
		{nil, 2, "usage:"},
		{[]string{"bogus"}, 2, "usage:"},
		{[]string{"join"}, 2, "usage:"},
		{[]string{"join", "not-a-token", "--data-dir", dir}, 1, "must start with"},
		{[]string{"token", "--data-dir", dir}, 1, "clustering is not enabled"},
		{[]string{"token", "--ttl", "2h", "--data-dir", dir}, 1, "--ttl"},
		{[]string{"init", "--data-dir", dir, "--database-url", "mysql://x"}, 1, "postgres://"},
	} {
		var out, errb bytes.Buffer
		t.Setenv("ETHER_DATABASE_URL", "")
		code := runCluster(tc.args, dir, &out, &errb)
		if code != tc.code || !strings.Contains(out.String()+errb.String(), tc.want) {
			t.Errorf("%v: code %d, output %q %q", tc.args, code, out.String(), errb.String())
		}
	}
}
