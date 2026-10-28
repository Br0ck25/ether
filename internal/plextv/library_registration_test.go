package plextv

import (
	"context"
	"encoding/xml"
	"net"
	"reflect"
	"testing"
)

// A server in resources without these Directory siblings cannot be selected
// for library sharing. Merely asserting successful /servers.xml HTTP status
// missed this: plex.tv accepts a server with no libraries.
func TestRegistrarPublishesLocalLibraries(t *testing.T) {
	f := newFakePlexTV(t, testMachineID)
	f.addUser("server-token-42", owner())
	cfg := testConfig()
	cfg.Plex.Token, cfg.Plex.Claimed = "server-token-42", true
	cfg.Plex.RemoteAccess = false
	r, err := NewRegistrar(RegistrarOptions{
		Config: &memConfig{cfg: cfg}, Client: newServerClient(t, f),
		Mapper: &fakeMapper{}, WAN: &fakeWAN{},
		LAN: func() ([]net.IP, error) { return []net.IP{net.ParseIP("192.168.1.5")}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []LibrarySection{
		{Key: 7, Type: "movie", Title: "Movies & cinéma", UUID: "movies-uuid", Language: "en-US"},
		{Key: 12, Type: "show", Title: "TV Shows", UUID: "shows-uuid", Language: "fr"},
	}
	sections := append([]LibrarySection(nil), want...)
	r.SetLibrarySections(sections)
	sections[0].Title = "caller mutation"
	for i := 0; i < 2; i++ {
		if err := r.PublishNow(context.Background()); err != nil {
			t.Fatal(err)
		}
		var body struct {
			Size   int `xml:"size,attr"`
			Server struct {
				MachineID string           `xml:"machineIdentifier,attr"`
				Nested    []LibrarySection `xml:"Directory"`
			} `xml:"Server"`
			Libraries []LibrarySection `xml:"Directory"`
		}
		if err := xml.Unmarshal([]byte(f.registered[i]), &body); err != nil {
			t.Fatal(err)
		}
		if body.Size != 3 || body.Server.MachineID != testMachineID || len(body.Server.Nested) != 0 || !reflect.DeepEqual(body.Libraries, want) {
			t.Fatalf("registration loses or misplaces libraries: %+v", body)
		}
	}
	// No invitation or sharing mutation is part of publishing library metadata.
	if f.count("POST /api/servers/") != 0 {
		t.Fatal("registration must not grant access")
	}
}

func TestLibraryRegistrationRejectsMalformedSections(t *testing.T) {
	valid := LibrarySection{Key: 1, Type: "movie", Title: "Movies", UUID: "movies-uuid"}
	for _, sections := range [][]LibrarySection{{{}}, {valid, valid}, {{Key: 1, Type: "movie", Title: "Movies"}}} {
		if _, err := registrationXML(testMachineID, ServerRegistration{Libraries: sections}); err == nil {
			t.Fatalf("accepted invalid library metadata: %+v", sections)
		}
	}
}
