package plex

import (
	"context"

	"github.com/braedonsaunders/ether/internal/core"
)

// StopPlayback ends an owner-selected source and tells the official app why.
// On a clustered node a session this node does not serve (a playback or
// live TV session ID from the cluster-wide status) is stopped by the node
// that does.
func (s *Server) StopPlayback(id, reason string) {
	if s.cl == nil {
		s.stopPlayback(id, reason)
		return
	}
	if !s.stopLocalPlayback(id, reason) {
		s.cl.publish(topicTerminate, terminateEvent{Scope: "playback", ID: id, Reason: reason})
	}
}

func (s *Server) stopPlayback(id, reason string) {
	if s.d.Playback == nil {
		return
	}
	for _, e := range s.sessions.list() {
		if ps, ok := s.d.Playback.SessionFor(e.user.ID, e.client.ClientID, e.key); ok && ps.ID == id {
			s.setTermination(e.id, reason)
		}
	}
	s.d.Playback.Stop(id)
}

// stopLocalPlayback stops id if this node serves it, as a live TV viewer
// or a playback session.
func (s *Server) stopLocalPlayback(id, reason string) bool {
	if s.d.LiveTV != nil {
		for _, v := range s.d.LiveTV.ViewingSessions() {
			if v.ID == id {
				_ = s.d.LiveTV.Stop(context.Background(), id, &core.User{IsOwner: true})
				return true
			}
		}
	}
	if s.d.Playback == nil {
		return false
	}
	if _, ok := s.d.Playback.Session(id); !ok {
		return false
	}
	s.stopPlayback(id, reason)
	return true
}

func (s *Server) liveTVAPIVersion() int {
	if s.liveTVEnabled() {
		return 4
	}
	return 0
}
