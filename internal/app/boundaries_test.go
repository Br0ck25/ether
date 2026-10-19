package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// Component consumers must use owned APIs rather than compose cross-schema SQL.
func TestOwnedStorageBoundaries(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Dir(filepath.Dir(file))
	sqlWord := regexp.MustCompile(`(?i)\b(SELECT|FROM|JOIN|INSERT|UPDATE|DELETE)\b`)
	for _, component := range []string{"app", "collections", "migrate"} {
		foreign := regexp.MustCompile(`\b(catalog_|state_|auth_|collections_)\w+`)
		err := filepath.WalkDir(filepath.Join(root, component), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			src, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(src, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				text, err := strconv.Unquote(lit.Value)
				if err != nil || !sqlWord.MatchString(text) {
					return true
				}
				for _, table := range foreign.FindAllString(text, -1) {
					if !strings.HasPrefix(table, component+"_") {
						t.Errorf("%s queries foreign table %s", path, table)
					}
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
