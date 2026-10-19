package plextv

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakePlexTV is an in-memory plex.tv covering the endpoints Ether uses.
type fakePlexTV struct {
	t   *testing.T
	srv *httptest.Server

	mu          sync.Mutex
	users       map[string]Account // token -> account
	pins        map[int64]*PIN
	nextPIN     int64
	claims      map[string]int64 // claim token -> account id
	exchangeErr int              // status to fail claim exchange with
	machineID   string
	published   []string
	putCount    int
	registered  []string // servers.xml bodies
	certHash    string
	csrStatus   int // status to fail CSR upload with
	pendingDL   int // 202s before the certificate is ready
	issued      []byte
	caKey       *ecdsa.PrivateKey
	caCert      *x509.Certificate
	certTTL     time.Duration
	accessXML   string
	accessFail  int // fail access_tokens.xml this many times with 500
	sharedXML   string
	usersXML    string
	requests    []string
	headers     map[string]http.Header // path -> last headers
	resources   map[string][]Resource  // token -> extra resources
}

const fakeHash = "0a1b2c3d4e5f60718293a4b5c6d7e8f9"

func newFakePlexTV(t *testing.T, machineID string) *fakePlexTV {
	f := &fakePlexTV{
		t:         t,
		users:     map[string]Account{},
		pins:      map[int64]*PIN{},
		nextPIN:   1000,
		claims:    map[string]int64{},
		machineID: machineID,
		certHash:  fakeHash,
		certTTL:   90 * 24 * time.Hour,
		headers:   map[string]http.Header{},
		resources: map[string][]Resource{},
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Fake Plex CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(10 * 365 * 24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	f.caKey = key
	f.caCert, _ = x509.ParseCertificate(der)
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakePlexTV) addUser(token string, a Account) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.users[token] = a
}

func (f *fakePlexTV) approvePIN(id int64, token string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pins[id].AuthToken = token
}

func (f *fakePlexTV) header(path string) http.Header {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.headers[path]
}

func (f *fakePlexTV) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.requests {
		if strings.HasPrefix(r, prefix) {
			n++
		}
	}
	return n
}

func (f *fakePlexTV) jsonErr(w http.ResponseWriter, status, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"errors":[{"code":%d,"message":%q,"status":%d}]}`, code, msg, status)
}

func (f *fakePlexTV) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	f.headers[r.URL.Path] = r.Header.Clone()
	token := r.Header.Get("X-Plex-Token")
	if token == "" {
		token = r.URL.Query().Get("auth_token")
	}
	acct, authed := f.users[token]
	devPrefix := "/api/v2/devices/" + f.machineID + "/certificate/"

	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/api/v2/pins":
		if r.URL.Query().Get("strong") != "true" || r.Header.Get("X-Plex-Client-Identifier") == "" {
			f.jsonErr(w, 400, 1000, "bad pin request")
			return
		}
		f.nextPIN++
		p := &PIN{ID: f.nextPIN, Code: "strongcode" + strconv.FormatInt(f.nextPIN, 10),
			ClientIdentifier: r.Header.Get("X-Plex-Client-Identifier"), ExpiresIn: 1800}
		f.pins[p.ID] = p
		w.WriteHeader(201)
		fmt.Fprintf(w, `{"id":%d,"code":%q,"product":"Ether","trusted":false,"clientIdentifier":%q,"expiresIn":1800,"createdAt":"2026-10-18T10:00:00Z","expiresAt":%q,"authToken":null}`,
			p.ID, p.Code, p.ClientIdentifier, time.Now().Add(30*time.Minute).UTC().Format(time.RFC3339))
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v2/pins/"):
		id, _ := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/api/v2/pins/"), 10, 64)
		p, ok := f.pins[id]
		if !ok || p.Code != r.URL.Query().Get("code") {
			f.jsonErr(w, 404, 1020, "Code not found or expired")
			return
		}
		if p.ClientIdentifier != r.Header.Get("X-Plex-Client-Identifier") {
			f.jsonErr(w, 400, 1000, "client identifier mismatch")
			return
		}
		tok := "null"
		if p.AuthToken != "" {
			tok = strconv.Quote(p.AuthToken)
		}
		fmt.Fprintf(w, `{"id":%d,"code":%q,"clientIdentifier":%q,"expiresIn":1700,"authToken":%s}`, p.ID, p.Code, p.ClientIdentifier, tok)
	case r.URL.Path == "/api/v2/user":
		if !authed {
			f.jsonErr(w, 401, 1001, "User could not be authenticated")
			return
		}
		json.NewEncoder(w).Encode(acct)
	case r.URL.Path == "/api/claim/token.json":
		if !authed {
			f.jsonErr(w, 401, 1001, "User could not be authenticated")
			return
		}
		ct := "claim-" + strconv.FormatInt(acct.ID, 10)
		f.claims[ct] = acct.ID
		fmt.Fprintf(w, `{"token":%q}`, ct)
	case r.Method == http.MethodPost && r.URL.Path == "/api/claim/exchange":
		if f.exchangeErr != 0 {
			w.WriteHeader(f.exchangeErr)
			return
		}
		id, ok := f.claims[r.URL.Query().Get("token")]
		if !ok || r.Header.Get("X-Plex-Provides") != "server" || r.Header.Get("X-Plex-Client-Identifier") != f.machineID {
			w.WriteHeader(400)
			io.WriteString(w, `<errors><error>Invalid claim token</error></errors>`)
			return
		}
		delete(f.claims, r.URL.Query().Get("token"))
		var owner Account
		for _, a := range f.users {
			if a.ID == id {
				owner = a
			}
		}
		st := "server-token-" + strconv.FormatInt(id, 10)
		f.users[st] = owner
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<user email="%s" id="%d" uuid="%s" username="%s" title="%s">
  <subscription active="1" status="Active" plan="lifetime"/>
  <authentication-token>%s</authentication-token>
</user>`, owner.Email, owner.ID, owner.UUID, owner.Username, owner.Title, st)
	case r.Method == http.MethodPut && r.URL.Path == "/devices/"+f.machineID:
		if !authed {
			w.WriteHeader(401)
			return
		}
		f.putCount++
		f.published = r.URL.Query()["Connection[][uri]"]
		io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><Device/>`)
	case r.Method == http.MethodPost && r.URL.Path == "/servers.xml":
		if !authed || r.URL.Query().Get("async") != "1" {
			w.WriteHeader(401)
			return
		}
		b, _ := io.ReadAll(r.Body)
		f.registered = append(f.registered, string(b))
		w.WriteHeader(201)
	case r.URL.Path == "/api/v2/resources":
		if !authed {
			f.jsonErr(w, 401, 1001, "User could not be authenticated")
			return
		}
		q := r.URL.Query()
		if q.Get("includeHttps") != "1" || q.Get("includeRelay") != "1" {
			f.jsonErr(w, 400, 1000, "missing includes")
			return
		}
		out := append([]Resource{}, f.resources[token]...)
		if strings.HasPrefix(token, "server-token-") || token == "owner-token" {
			var conns []ResourceConnection
			for _, u := range f.published {
				if u == "" {
					continue
				}
				hostport := strings.TrimPrefix(u, "http://")
				host, port, _ := strings.Cut(hostport, ":")
				p, _ := strconv.Atoi(port)
				conns = append(conns,
					ResourceConnection{Protocol: "https", Address: host, Port: p, Local: strings.HasPrefix(host, "192.168."),
						URI: "https://" + strings.ReplaceAll(host, ".", "-") + "." + f.certHash + ".plex.direct:" + port},
					ResourceConnection{Protocol: "http", Address: host, Port: p, URI: u, Local: strings.HasPrefix(host, "192.168.")})
			}
			out = append(out, Resource{Name: "Ether", Product: ServerProduct, ClientIdentifier: f.machineID, Provides: "server",
				Owned: true, PublicAddress: "203.0.113.7", Connections: conns})
		}
		json.NewEncoder(w).Encode(out)
	case r.URL.Path == "/servers/"+f.machineID+"/access_tokens.xml":
		if !authed || r.URL.Query().Get("auth_token") == "" {
			w.WriteHeader(401)
			return
		}
		if f.accessFail > 0 {
			f.accessFail--
			w.WriteHeader(500)
			return
		}
		io.WriteString(w, f.accessXML)
	case r.URL.Path == "/api/servers/"+f.machineID+"/shared_servers":
		if !authed {
			w.WriteHeader(401)
			return
		}
		io.WriteString(w, f.sharedXML)
	case r.URL.Path == "/api/users":
		if !authed {
			w.WriteHeader(401)
			return
		}
		if f.usersXML == "" {
			w.WriteHeader(500)
			return
		}
		io.WriteString(w, f.usersXML)
	case r.URL.Path == devPrefix+"subject":
		if !authed {
			w.WriteHeader(401)
			return
		}
		fmt.Fprintf(w, "/CN=*.%s.plex.direct", f.certHash)
	case r.URL.Path == devPrefix+"csr" && r.Method == http.MethodPut:
		if !authed {
			w.WriteHeader(401)
			return
		}
		if f.csrStatus != 0 {
			f.jsonErr(w, f.csrStatus, 1003, "API rate limit exceeded")
			return
		}
		if q := r.URL.Query(); q.Get("reason") == "" || q.Get("invalidIn") == "" {
			w.WriteHeader(400)
			return
		}
		b, _ := io.ReadAll(r.Body)
		blk, _ := pem.Decode(b)
		if blk == nil || blk.Type != "CERTIFICATE REQUEST" {
			w.WriteHeader(400)
			return
		}
		csr, err := x509.ParseCertificateRequest(blk.Bytes)
		if err != nil || csr.CheckSignature() != nil || csr.Subject.CommonName != "*."+f.certHash+".plex.direct" {
			w.WriteHeader(400)
			return
		}
		tmpl := &x509.Certificate{
			SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: csr.Subject, DNSNames: csr.DNSNames,
			NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(f.certTTL),
			KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, f.caCert, csr.PublicKey, f.caKey)
		if err != nil {
			w.WriteHeader(500)
			return
		}
		f.issued = append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
			pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.caCert.Raw})...)
		w.WriteHeader(204)
	case r.URL.Path == devPrefix+"download":
		if !authed {
			w.WriteHeader(401)
			return
		}
		if f.issued == nil {
			w.WriteHeader(404)
			return
		}
		if f.pendingDL > 0 {
			f.pendingDL--
			w.WriteHeader(202)
			return
		}
		w.Write(f.issued)
	default:
		w.WriteHeader(404)
	}
}
