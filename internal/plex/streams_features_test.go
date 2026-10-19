package plex

import (
	"context"
	"fmt"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
	"github.com/braedonsaunders/ether/internal/trackprefs"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestPersistentShowChoiceAppliesToNextEpisodeDecision(t *testing.T) {
	f := newFixture()
	defer f.srv.Close()
	db, err := store.OpenMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	prefs := trackprefs.New(db)
	f.srv.d.TrackPreferences = prefs
	r := trackprefs.Default()
	r.AudioLanguages = []string{"en"}
	r.SubtitleLanguages = []string{"en"}
	r.SubtitleMode = "always"
	r.Loudness = true
	if err = prefs.PutRules(context.Background(), 0, r); err != nil {
		t.Fatal(err)
	}
	f.get(t, "/library/metadata/"+bbE1.Key.String())
	frID := streamHash(bbE1.Key, 2)
	f.request(t, http.MethodPut, fmt.Sprintf("/library/parts/%s?audioStreamID=%d&subtitleStreamID=0", bbE1.Key, frID), nil)
	f.get(t, "/video/:/transcode/universal/decision?path=/library/metadata/"+bbE2.Key.String()+"&session=showmemory")
	if f.trans.lastOpts.AudioStream != 2 || f.trans.lastOpts.SubtitleStream != -1 || len(f.trans.lastOpts.AudioFilters) != 1 {
		t.Fatalf("decision did not use show memory %+v", f.trans.lastOpts)
	}
	f.get(t, "/video/:/transcode/universal/decision?path=/library/metadata/"+fightClub.Key.String()+"&session=othermovie")
	if f.trans.lastOpts.AudioStream == 2 {
		t.Fatal("show choice leaked to movie")
	}
}
func TestDecisionIncludesProbedChaptersAndMarkers(t *testing.T) {
	f := newFixture()
	defer f.srv.Close()
	f.playback.info.Chapters = []core.Chapter{{ID: 3, Title: "Intro", Start: 5 * time.Second, End: 45 * time.Second}, {ID: 4, Title: "Credits", Start: f.playback.info.Duration - time.Minute, End: f.playback.info.Duration}}
	rec := f.get(t, "/video/:/transcode/universal/decision?path=/library/metadata/"+bbE1.Key.String()+"&session=markers")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `<Marker id="1" type="intro" startTimeOffset="5000" endTimeOffset="45000" final="0"`) || !strings.Contains(rec.Body.String(), `<Chapter id="3" index="0" title="Intro"`) {
		t.Fatalf("missing feature metadata %s", rec.Body.String())
	}
}

func TestMDECannotBypassRequestedAudioProcessing(t *testing.T) {
	f := newFixture()
	defer f.srv.Close()
	db, err := store.OpenMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	prefs := trackprefs.New(db)
	f.srv.d.TrackPreferences = prefs
	r := trackprefs.Default()
	r.Loudness = true
	if err = prefs.PutRules(context.Background(), 0, r); err != nil {
		t.Fatal(err)
	}
	f.trans.decision = core.Decision{Kind: core.DecisionTranscode, Container: "mpegts", VideoCopy: true, AudioCopy: false, AudioCodec: "aac"}
	rec := f.get(t, "/video/:/transcode/universal/decision?path=/library/metadata/"+fightClub.Key.String()+"&hasMDE=1&session=processing")
	if !strings.Contains(rec.Body.String(), `streamType="2" decision="transcode"`) || strings.Contains(rec.Body.String(), `mdeDecisionCode="1000"`) {
		t.Fatalf("MDE bypassed filter decision: %s", rec.Body.String())
	}
}
