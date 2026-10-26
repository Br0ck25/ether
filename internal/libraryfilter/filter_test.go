package libraryfilter

import (
	"encoding/json"
	"github.com/braedonsaunders/ether/internal/core"
	"testing"
)

func TestInvalidFiltersCannotHideWholeLibrary(t *testing.T) {
	for _, g := range []core.LibraryExclusion{
		{Name: "Empty", MediaType: "mixed", Match: "all"},
		{Name: "SQL", MediaType: "mixed", Match: "all", Rules: []core.CollectionRule{{Field: "country; DROP TABLE", Op: "in", Value: json.RawMessage(`["IN"]`)}}},
		{Name: "Bad range", MediaType: "mixed", Match: "all", Rules: []core.CollectionRule{{Field: "year", Op: "between", Value: json.RawMessage(`[2026,1900]`)}}},
	} {
		if _, err := Compile([]core.LibraryExclusion{g}); err == nil {
			t.Fatal("invalid rule accepted", g)
		}
	}
}
