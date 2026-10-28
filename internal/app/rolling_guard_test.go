package app

import (
	"testing"

	"github.com/braedonsaunders/ether/internal/cluster"
)

func TestOthersNotServing(t *testing.T) {
	self := "a"
	cases := []struct {
		name  string
		nodes []cluster.Node
		want  bool
	}{
		{"alone", []cluster.Node{{ID: "a", Live: true}}, false},
		{"peer serving", []cluster.Node{{ID: "a", Live: true}, {ID: "b", Live: true}}, false},
		{"peer down", []cluster.Node{{ID: "a", Live: true}, {ID: "b"}}, true},
		{"peer draining", []cluster.Node{{ID: "a", Live: true}, {ID: "b", Live: true, Draining: true}}, true},
		{"one of two peers down", []cluster.Node{{ID: "a", Live: true}, {ID: "b"}, {ID: "c", Live: true}}, false},
	}
	for _, tc := range cases {
		if got := othersNotServing(tc.nodes, self); got != tc.want {
			t.Errorf("%s: othersNotServing = %v, want %v", tc.name, got, tc.want)
		}
	}
}
