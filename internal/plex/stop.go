package plex

// StopPlayback ends an owner-selected source and tells the official app why.
func (s *Server) StopPlayback(id, reason string) {
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

func (s *Server) liveTVAPIVersion() int {
	if s.liveTVEnabled() {
		return 4
	}
	return 0
}
