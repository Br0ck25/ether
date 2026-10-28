package collections

import (
	"context"
	"strconv"
	"strings"

	"github.com/braedonsaunders/ether/internal/clusterbus"
)

// Cluster cache coherence. Hints published after local writes:
//
//	index            the browsable-title index changed: re-check it now
//	collection:<id>  a collection's definition or members changed
//
// Member caches also validate themselves against the stored build hash, so
// a missed event costs at most a stale read until the next check.

// SetBus connects the cluster bus (nil disconnects). A standalone server
// never calls it.
func (s *Service) SetBus(bus clusterbus.Bus) {
	iv := clusterbus.NewInvalidator(bus, "collections", s.log, s.applyRemote)
	s.invalMu.Lock()
	old := s.inval
	s.inval = iv
	s.invalMu.Unlock()
	old.Close()
}

func (s *Service) notify(hint string) {
	s.invalMu.Lock()
	iv := s.inval
	s.invalMu.Unlock()
	iv.Notify(hint)
}

// changed drops a collection's cached members here and on other nodes.
func (s *Service) changed(id int64) {
	s.invalidate(id)
	s.notify("collection:" + strconv.FormatInt(id, 10))
}

func (s *Service) applyRemote(_ context.Context, hints []string, all bool) {
	if all {
		s.idx.invalidate()
		s.mu.Lock()
		s.members = map[int64]*memberSet{}
		s.personal = map[string]personalEntry{}
		s.mu.Unlock()
		return
	}
	for _, h := range hints {
		kind, arg, _ := strings.Cut(h, ":")
		switch kind {
		case "index":
			s.idx.invalidate()
		case "collection":
			if id, err := strconv.ParseInt(arg, 10, 64); err == nil {
				s.invalidate(id)
			}
		}
	}
}
