package livetv

import (
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"

	"github.com/braedonsaunders/ether/internal/core"
)

var dashURL = regexp.MustCompile(`(initialization|media)="([^"]+)"`)

func (s *Service) ServeDASH(w http.ResponseWriter, r *http.Request, id string, u *core.User) {
	ss, err := s.getSession(r.Context(), id, u)
	if err != nil {
		writeStreamError(w, err)
		return
	}
	ss.mu.Lock()
	ss.consumed = true
	if ss.dash == nil {
		if ss.info.Channel.Kind != "iptv" || s.o.Transcoder == nil {
			ss.mu.Unlock()
			writeStreamError(w, core.ErrNotAvailable)
			return
		}
		info := ss.info.MediaInfo
		var e error
		if info == nil {
			info, e = s.o.Transcoder.Probe(r.Context(), ss.info.Channel.StreamURL)
		}
		if e != nil {
			ss.mu.Unlock()
			writeStreamError(w, e)
			return
		}
		copy := *info
		info = &copy
		info.Duration = 0
		ss.info.MediaInfo = info
		dec := s.o.Transcoder.Decide(*info, ss.caps, core.DecideOptions{DirectStream: true, AudioStream: -1, SubtitleStream: -1})
		if dec.Kind == core.DecisionDirectPlay {
			dec.Kind = core.DecisionRemux
			dec.VideoCopy = true
			dec.AudioCopy = true
		}
		hs, e := s.o.Transcoder.StartHLS(s.ctx, core.HLSRequest{SessionID: "livetv-dash-" + id, InputURL: ss.info.Channel.StreamURL, Info: *info, Decision: dec, Protocol: core.ProtocolDASH, SegmentDuration: segmentDuration})
		if e != nil {
			ss.mu.Unlock()
			writeStreamError(w, e)
			return
		}
		ds, ok := hs.(core.DASHSession)
		if !ok {
			hs.Close()
			ss.mu.Unlock()
			writeStreamError(w, core.ErrNotAvailable)
			return
		}
		ss.dash = ds
	}
	ds := ss.dash
	ss.mu.Unlock()
	rec := httptest.NewRecorder()
	ds.ServeManifest(rec, r)
	if rec.Code != 200 {
		http.Error(w, "Live stream unavailable", rec.Code)
		return
	}
	prefix := "session/livetv-dash-" + id + "/"
	body := dashURL.ReplaceAllStringFunc(rec.Body.String(), func(attr string) string {
		parts := dashURL.FindStringSubmatch(attr)
		target := strings.Replace(parts[2], prefix, "/livetv/sessions/"+id+"/0/dash/", 1)
		return parts[1] + `="` + html.EscapeString(withAuth(target, r)) + `"`
	})
	w.Header().Set("Content-Type", "application/dash+xml")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != "HEAD" {
		fmt.Fprint(w, body)
	}
}

func (s *Service) ServeDASHPart(w http.ResponseWriter, r *http.Request, id string, u *core.User, rep, n int, init bool) {
	ss, err := s.getSession(r.Context(), id, u)
	if err != nil {
		writeStreamError(w, err)
		return
	}
	ss.mu.Lock()
	ds := ss.dash
	ss.mu.Unlock()
	if ds == nil {
		writeStreamError(w, core.ErrNotFound)
		return
	}
	if init {
		ds.ServeInit(w, r, rep)
	} else {
		ds.ServeMedia(w, r, rep, n)
	}
}
