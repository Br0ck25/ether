package plex

import (
	"net/http"
	"strings"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/livetv"
	"github.com/braedonsaunders/ether/internal/plex/model"
)

// Modern native Plex players parse a channel tune through their play-queue
// loader. Analysis-only metadata has no playable item key/type, so that loader
// rejects it before making a media decision. Keep the queue bound to the actual
// live session and its viewer, independently of catalogue rating keys.
func (s *Server) writeModernLiveTune(w http.ResponseWriter, r *http.Request, info livetv.SessionInfo) {
	w.Header().Del("X-Plex-Activity")
	pr := FromContext(r.Context())
	q := s.queues.create(pr.User.ID, pr.ClientID, []core.RatingKey{0}, s.now())
	snap, _ := s.queues.with(q.id, pr.User.ID, s.now(), func(q *playQueue) {
		q.liveSession = info.ID
		q.sourceURI = "server://" + s.identity().MachineIdentifier + "/com.plexapp.plugins.library/livetv/sessions/" + info.ID
	})
	s.write(w, r, s.livePlayQueue(snap, info))
}

func (s *Server) livePlayQueue(pq playQueue, info livetv.SessionInfo) *model.MediaContainer {
	md := s.liveSessionMetadata(info)
	md.PlayQueueItemID = pq.items[0].id
	return &model.MediaContainer{
		Size: 1, Identifier: model.LibraryIdentifier, Metadata: []model.Metadata{md},
		Extra: []model.Attr{
			model.A("playQueueID", pq.id),
			model.A("playQueueSelectedItemID", pq.items[0].id),
			model.A("playQueueSelectedItemOffset", 0),
			model.A("playQueueShuffled", false),
			model.A("playQueueSourceURI", pq.sourceURI),
			model.A("playQueueTotalCount", 1),
			model.A("playQueueVersion", pq.version),
		},
	}
}

func (s *Server) writeLivePlayQueue(w http.ResponseWriter, r *http.Request, pq playQueue) {
	if !s.liveTVEnabled() {
		writeHTTPError(w, http.StatusNotFound)
		return
	}
	info, err := s.d.LiveTV.Session(r.Context(), pq.liveSession, FromContext(r.Context()).User)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.write(w, r, s.livePlayQueue(pq, info))
}

// Live channel progress is not watch history for a catalogue movie/episode.
// Its opaque session key must not go through the numeric rating-key parser.
func (s *Server) handleLiveTimeline(w http.ResponseWriter, r *http.Request) bool {
	key := r.URL.Query().Get("key")
	if !strings.HasPrefix(key, "/livetv/sessions/") {
		return false
	}
	if !s.liveTVEnabled() {
		writeHTTPError(w, http.StatusNotFound)
		return true
	}
	id := strings.TrimPrefix(key, "/livetv/sessions/")
	pr := FromContext(r.Context())
	if _, err := s.d.LiveTV.Session(r.Context(), id, pr.User); err != nil {
		s.writeErr(w, r, err)
		return true
	}
	if r.URL.Query().Get("state") == "stopped" {
		if err := s.d.LiveTV.Stop(r.Context(), id, pr.User); err != nil {
			s.writeErr(w, r, err)
			return true
		}
	}
	s.writeEmpty(w, r)
	return true
}
