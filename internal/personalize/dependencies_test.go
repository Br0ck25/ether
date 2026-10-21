package personalize

import (
	"github.com/braedonsaunders/ether/internal/core"
	"testing"
)

func TestDependenciesConstructBeforeModelWorkers(t *testing.T) {
	called := false
	h := newHarness(t, func(o *Options) {
		o.Dependencies = func(scorer core.PersonalScorer) (core.Recommender, core.CollectionRows, error) {
			e := scorer.(*Engine)
			if e.ctx != nil || e.rec == nil || len(e.models) != 0 {
				t.Fatal("dependency factory ran after workers started")
			}
			called = true
			return nil, nil, nil
		}
	})
	if !called || h.e.ctx == nil {
		t.Fatal("dependency construction did not precede engine startup")
	}
}
