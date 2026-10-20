package config

import (
	"encoding/json"
	"testing"
)

// The Personalize switches default on, and a config written before the
// section existed keeps that (loading unmarshals over Defaults).
func TestPersonalizeDefaults(t *testing.T) {
	d := Defaults(t.TempDir())
	if !d.Personalize.PostPlay || !d.Personalize.PostPlayPrebuffer ||
		!d.Personalize.MicroReasons || !d.Personalize.SessionIntent {
		t.Fatalf("defaults = %+v, want all on", d.Personalize)
	}
	old := Defaults(t.TempDir())
	if err := json.Unmarshal([]byte(`{"server":{"port":32400}}`), &old); err != nil {
		t.Fatal(err)
	}
	if !old.Personalize.MicroReasons {
		t.Error("old config without the section turned micro-reasons off")
	}
	off := Defaults(t.TempDir())
	if err := json.Unmarshal([]byte(`{"personalize":{"postPlay":false,"microReasons":false}}`), &off); err != nil {
		t.Fatal(err)
	}
	if off.Personalize.PostPlay || off.Personalize.MicroReasons || !off.Personalize.SessionIntent {
		t.Errorf("explicit opt-out = %+v", off.Personalize)
	}
}
