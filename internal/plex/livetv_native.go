package plex

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/livetv"
	"github.com/braedonsaunders/ether/internal/plex/model"
)

// PMS's channel-tune route serializes a rolling subscription, rather than an
// ordinary play queue. Its grab's Video and Media are single objects, unlike
// normal library metadata. Keep these shapes separate from catalogue models.
type nativeLiveVideo struct {
	Metadata model.Metadata `plex:",inline"`
	Media    model.Media    `plex:"Media,object"`
}

type nativeLiveGrab struct {
	Key            string          `plex:"key"`
	ID             string          `plex:"id"`
	SubscriptionID int64           `plex:"mediaSubscriptionID"`
	MediaIndex     int             `plex:"mediaIndex"`
	Status         string          `plex:"status"`
	Rolling        bool            `plex:"rolling"`
	Video          nativeLiveVideo `plex:"Video,object"`
}

type nativeLiveSubscription struct {
	Key      string           `plex:"key"`
	Type     int              `plex:"type"`
	Title    string           `plex:"title"`
	Channel  string           `plex:"channelIdentifier"`
	Grab     []nativeLiveGrab `plex:"MediaGrabOperation"`
	Timeline liveTimeline     `plex:"Timeline,object"`
}

type nativeLiveContainer struct {
	Size          int                      `plex:"size"`
	Subscriptions []nativeLiveSubscription `plex:"MediaSubscription"`
	Grabs         []nativeLiveGrab         `plex:"MediaGrabOperation"`
}

func nativeLiveRequest(r *http.Request) bool {
	pr := FromContext(r.Context())
	platform := strings.ToLower(pr.Platform)
	return strings.Contains(strings.ToLower(pr.Product), "apple") ||
		platform == "tvos" || platform == "ios" || platform == "android"
}

func (s *Server) nativeLiveSubscriptionFor(info livetv.SessionInfo) nativeLiveSubscription {
	md := s.liveSessionMetadata(info)
	media := md.Media[0]
	md.Media = nil
	web := s.liveSubscriptionFor(info)
	return nativeLiveSubscription{
		Key: strconv.FormatInt(info.SubscriptionID, 10), Type: 4,
		Title: info.Channel.Name, Channel: info.Channel.ID, Timeline: web.Timeline,
		Grab: []nativeLiveGrab{{
			Key: "/media/grabbers/operations/" + info.ID, ID: info.ID,
			SubscriptionID: info.SubscriptionID, Status: "inprogress", Rolling: true,
			Video: nativeLiveVideo{Metadata: md, Media: media},
		}},
	}
}

func (s *Server) writeNativeLiveTune(w http.ResponseWriter, r *http.Request, info livetv.SessionInfo) {
	w.Header().Set("X-Plex-Activity", info.ActivityID)
	s.write(w, r, &nativeLiveContainer{Size: 1, Subscriptions: []nativeLiveSubscription{s.nativeLiveSubscriptionFor(info)}})
}

// A subscription key is not a stream UUID. Resolve it only through sessions the
// authenticated viewer can access; knowing a numeric ID never grants access.
func (s *Server) liveSubscriptionSession(r *http.Request, key string) (livetv.SessionInfo, error) {
	user := FromContext(r.Context()).User
	if id, err := strconv.ParseInt(key, 10, 64); err == nil {
		for _, info := range s.d.LiveTV.Sessions(r.Context(), user) {
			if info.SubscriptionID == id {
				return info, nil
			}
		}
		return livetv.SessionInfo{}, core.ErrNotFound
	}
	return s.d.LiveTV.Session(r.Context(), key, user)
}
