package store

import (
	"fmt"
	"strings"
	"testing"
)

func TestRebind(t *testing.T) {
	cases := []struct{ in, want string }{
		{`SELECT 1`, `SELECT 1`},
		{`SELECT * FROM t WHERE a=? AND b=?`, `SELECT * FROM t WHERE a=$1 AND b=$2`},
		{`INSERT INTO t(a,b) VALUES(?,?)`, `INSERT INTO t(a,b) VALUES($1,$2)`},
		{`SELECT '?' , ?`, `SELECT '?' , $1`},
		{`SELECT 'it''s ?', ?`, `SELECT 'it''s ?', $1`},
		{`SELECT '''?''', ?`, `SELECT '''?''', $1`},
		{`SELECT "we?ird" FROM t WHERE x=?`, `SELECT "we?ird" FROM t WHERE x=$1`},
		{`SELECT "a""?b" , ?`, `SELECT "a""?b" , $1`},
		{"SELECT ? -- what?\n, ?", "SELECT $1 -- what?\n, $2"},
		{`SELECT ? /* why? /* nested? */ still? */ , ?`, `SELECT $1 /* why? /* nested? */ still? */ , $2`},
		{`SELECT E'a\'?' , ?`, `SELECT E'a\'?' , $1`},
		{`SELECT 'a\' , ?`, `SELECT 'a\' , $1`}, // standard strings: backslash is literal
		{`SELECT $$ ? $$, ?`, `SELECT $$ ? $$, $1`},
		{`SELECT $tag$ ? $tag$, ?`, `SELECT $tag$ ? $tag$, $1`},
		{`SELECT $1, ?`, `SELECT $1, $1`},
		{`title LIKE ? ESCAPE '\' OR original_title LIKE ? ESCAPE '\'`, `title LIKE $1 ESCAPE '\' OR original_title LIKE $2 ESCAPE '\'`},
		{`SELECT 'unterminated ?`, `SELECT 'unterminated ?`},
		{`SELECT ?-- trailing?`, `SELECT $1-- trailing?`},
		{`a=?||?`, `a=$1||$2`},
	}
	for _, c := range cases {
		got, _ := Rebind(c.in)
		if got != c.want {
			t.Errorf("Rebind(%q)\n got %q\nwant %q", c.in, got, c.want)
		}
	}
}

func TestRebindManyParams(t *testing.T) {
	var in, want strings.Builder
	in.WriteString("INSERT INTO t VALUES ")
	want.WriteString("INSERT INTO t VALUES ")
	for i := 1; i <= 1200; i++ {
		if i > 1 {
			in.WriteByte(',')
			want.WriteByte(',')
		}
		in.WriteString("(?)")
		fmt.Fprintf(&want, "($%d)", i)
	}
	got, n := Rebind(in.String())
	if n != 1200 || got != want.String() {
		t.Fatalf("n=%d, rewritten text mismatch", n)
	}
}

func TestRebindCache(t *testing.T) {
	var c rebindCache
	q := `SELECT ? FROM t`
	if got := c.get(q); got != `SELECT $1 FROM t` {
		t.Fatalf("got %q", got)
	}
	if got := c.get(q); got != `SELECT $1 FROM t` {
		t.Fatalf("cached got %q", got)
	}
	for i := 0; i < rebindCacheMax+10; i++ {
		c.get(fmt.Sprintf("SELECT ? + %d", i))
	}
	if len(c.m) > rebindCacheMax {
		t.Fatalf("cache grew to %d", len(c.m))
	}
}

func TestSplitSQLRoundTrip(t *testing.T) {
	for _, q := range []string{
		`SELECT 'a', "b", $x$c$x$ -- d` + "\n/* e */ f",
		`'`, `"`, `--`, `/*`, `$`, `$$`, `E'\'`, `x$1y`,
	} {
		var b strings.Builder
		for _, s := range splitSQL(q) {
			b.WriteString(s.text)
		}
		if b.String() != q {
			t.Errorf("splitSQL(%q) lost text: %q", q, b.String())
		}
	}
}
