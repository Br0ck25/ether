package plextv

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"encoding/xml"
	"errors"
	"fmt"
	"math/big"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ErrCertUnavailable means plex.tv will not issue a plex.direct certificate
// for this device (no subject, or the endpoint is refused).
var ErrCertUnavailable = errors.New("plextv: plex.direct certificate not available for this server")

// CertRequest tunes RequestPlexDirectCertificate.
type CertRequest struct {
	// Key is the private key to certify; a fresh RSA-2048 key when nil
	// (RSA for compatibility with older TV clients).
	Key crypto.Signer
	// Reason is "missing" (no certificate yet) or "expiring", as PMS sends.
	Reason string
	// InvalidIn is seconds until the current certificate stops being valid
	// (negative when already expired; 0 when missing), as PMS sends.
	InvalidIn int
	// PollInterval between download attempts (default 3s) and Timeout for
	// the whole download phase (default 90s, PMS's window).
	PollInterval time.Duration
	Timeout      time.Duration
}

// CertBundle is an issued (or self-signed) certificate and its key.
type CertBundle struct {
	CertPEM []byte // leaf first, then any chain
	KeyPEM  []byte // PKCS#8
	Leaf    *x509.Certificate
	Hash    string // plex.direct hash ("" for self-signed without one)
	Subject string // e.g. "*.<hash>.plex.direct"
}

var plexDirectRe = regexp.MustCompile(`(?i)\*\.([0-9a-z]+)\.plex\.direct`)

// CertificateSubject asks plex.tv for the certificate subject of this
// device: GET /api/v2/devices/{machineIdentifier}/certificate/subject. The
// subject carries the plex.direct hash ("/CN=*.<hash>.plex.direct"); the
// hash is assigned by plex.tv, not derived locally.
func (c *Client) CertificateSubject(ctx context.Context, serverToken string) (cn, hash string, err error) {
	s, err := c.certificateSubject(ctx, serverToken)
	return s.cn, s.hash, err
}

// certSubject is the parsed GET …/certificate/subject response.
type certSubject struct {
	cn, hash string
	// rawSubject is the DER X.509 Name carried base64-encoded in the
	// response's "subject" attribute. PMS decodes it (EVP_DecodeBlock, then
	// d2i_X509_NAME) and sets it verbatim as the CSR subject. nil when the
	// attribute is absent or not a DER Name.
	rawSubject []byte
}

func (c *Client) certificateSubject(ctx context.Context, serverToken string) (certSubject, error) {
	resp, err := c.do(ctx, request{
		method: http.MethodGet,
		host:   c.serversHost(),
		path:   "/api/v2/devices/" + url.PathEscape(c.id.ClientIdentifier) + "/certificate/subject",
		token:  serverToken,
		// The endpoint returns an XML <subject> document. Requesting JSON
		// gets Plex's 406 code 1006 response (verified against the reference).
		accept: "application/xml",
	})
	if err != nil {
		var ae *APIError
		if errors.As(err, &ae) && (ae.Status == http.StatusNotFound || ae.Status == http.StatusForbidden) {
			return certSubject{}, fmt.Errorf("%w: %v", ErrCertUnavailable, err)
		}
		return certSubject{}, err
	}
	return parseCertificateSubject(resp.body)
}

// parseCertificateSubject reads
// <subject commonName="*.<hash>.plex.direct" subject="<base64 DER Name>"/>.
func parseCertificateSubject(b []byte) (certSubject, error) {
	var doc struct {
		CommonName string `xml:"commonName,attr"`
		Subject    string `xml:"subject,attr"`
	}
	_ = xml.Unmarshal(b, &doc) // any root element; the text scan below is the fallback
	m := plexDirectRe.FindStringSubmatch(doc.CommonName)
	if m == nil {
		m = plexDirectRe.FindStringSubmatch(string(b))
	}
	if m == nil {
		return certSubject{}, fmt.Errorf("%w: subject response has no plex.direct name", ErrCertUnavailable)
	}
	s := certSubject{hash: strings.ToLower(m[1])}
	s.cn = "*." + s.hash + ".plex.direct"
	if der, err := base64.StdEncoding.DecodeString(strings.TrimSpace(doc.Subject)); err == nil && len(der) > 0 {
		var rdn pkix.RDNSequence
		if rest, err := asn1.Unmarshal(der, &rdn); err == nil && len(rest) == 0 && len(rdn) > 0 {
			s.rawSubject = der
		}
	}
	return s, nil
}

// csrUploadBody encodes the CSR as PMS's libcurl form does. PMS adds a
// MultipartFieldData{name "file", data <PEM>, type application/x-pem-file,
// filename ""}; its HTTP layer turns a field with an empty filename into
// curl_formadd(NAMELENGTH, PTRNAME "file", CONTENTLEN, PTRCONTENTS,
// CONTENTTYPE): one multipart/form-data part named "file", without a
// filename parameter, Content-Type application/x-pem-file, body the PEM CSR.
func csrUploadBody(csrPEM []byte) (body []byte, contentType string, err error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="file"`)
	h.Set("Content-Type", "application/x-pem-file")
	pw, err := mw.CreatePart(h)
	if err != nil {
		return nil, "", err
	}
	if _, err := pw.Write(csrPEM); err != nil {
		return nil, "", err
	}
	if err := mw.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), mw.FormDataContentType(), nil
}

// RequestPlexDirectCertificate runs PMS's certificate flow end to end:
//
//  1. GET  /api/v2/devices/{id}/certificate/subject   -> "*.<hash>.plex.direct"
//  2. PUT  /api/v2/devices/{id}/certificate/csr?reason=…&invalidIn=…
//     multipart/form-data, one part "file" (no filename,
//     Content-Type application/x-pem-file) holding the PEM CSR; 2xx
//  3. GET  /api/v2/devices/{id}/certificate/download  (202 while pending,
//     200 with a PEM chain: leaf first, then the intermediate)
//
// The request formats follow the PMS 1.43.4 executable (see
// docs/plextv-notes.md §6); downloads are still parsed liberally (PEM, DER
// or JSON-wrapped PEM).
// plex.tv rate-limits step 2 (HTTP 429, code 1003) per device, so callers
// must not retry quickly: see CertManager.
//
// It is deliberately self-contained so the spike can call it directly.
func (c *Client) RequestPlexDirectCertificate(ctx context.Context, serverToken string, req CertRequest) (*CertBundle, error) {
	if req.Reason == "" {
		req.Reason = "missing"
	}
	if req.PollInterval <= 0 {
		req.PollInterval = 3 * time.Second
	}
	if req.Timeout <= 0 {
		req.Timeout = 90 * time.Second
	}
	subj, err := c.certificateSubject(ctx, serverToken)
	if err != nil {
		return nil, err
	}
	cn, hash := subj.cn, subj.hash
	key := req.Key
	if key == nil {
		// PMS: EVP_PKEY_CTX_new_id(EVP_PKEY_RSA) + set_rsa_keygen_bits(2048);
		// P-256 only with its "strong ciphers" preference.
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			return nil, err
		}
		key = k
	}
	// PMS sets the subject plex.tv returned (DER, verbatim) and adds no
	// extensions: no SANs. The CA puts the wildcard SAN in the certificate.
	tmpl := &x509.CertificateRequest{}
	if subj.rawSubject != nil {
		tmpl.RawSubject = subj.rawSubject
	} else {
		tmpl.Subject = pkix.Name{CommonName: cn}
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, tmpl, key) // SHA-256 signature, as PMS
	if err != nil {
		return nil, fmt.Errorf("plextv: create csr: %w", err)
	}
	// PEM_write_bio_X509_REQ_NEW writes the "NEW CERTIFICATE REQUEST" label.
	csrPEM := pem.EncodeToMemory(&pem.Block{Type: "NEW CERTIFICATE REQUEST", Bytes: csrDER})
	body, contentType, err := csrUploadBody(csrPEM)
	if err != nil {
		return nil, fmt.Errorf("plextv: encode csr upload: %w", err)
	}

	devPath := "/api/v2/devices/" + url.PathEscape(c.id.ClientIdentifier) + "/certificate"
	// Exactly one upload per attempt (no transport retries): plex.tv rate
	// limits this endpoint per device.
	if _, err := c.once(ctx, request{
		method:      http.MethodPut,
		host:        c.serversHost(),
		path:        devPath + "/csr",
		query:       url.Values{"reason": {req.Reason}, "invalidIn": {strconv.Itoa(req.InvalidIn)}},
		token:       serverToken,
		body:        body,
		contentType: contentType,
	}); err != nil {
		return nil, fmt.Errorf("plextv: upload csr: %w", err)
	}

	dctx, cancel := context.WithTimeout(ctx, req.Timeout)
	defer cancel()
	t := time.NewTicker(req.PollInterval)
	defer t.Stop()
	for {
		resp, err := c.once(dctx, request{
			method:   http.MethodGet,
			host:     c.serversHost(),
			path:     devPath + "/download",
			token:    serverToken,
			accept:   "application/x-pem-file, application/json;q=0.9, */*;q=0.8",
			okStatus: []int{http.StatusAccepted},
		})
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if dctx.Err() != nil {
				return nil, fmt.Errorf("plextv: certificate not issued within %s", req.Timeout)
			}
			var ae *APIError
			if errors.As(err, &ae) && ae.Status < 500 {
				return nil, fmt.Errorf("plextv: download certificate: %w", err)
			}
			c.log.Debug("certificate download retry", "err", err)
		} else if resp.status == http.StatusOK || (resp.status != http.StatusAccepted && len(resp.body) > 0) {
			chain, err := parseCertificates(resp.body)
			if err != nil {
				return nil, fmt.Errorf("plextv: download certificate: %w", err)
			}
			return bundleFor(chain, key, hash, cn)
		}
		select {
		case <-dctx.Done():
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("plextv: certificate not issued within %s", req.Timeout)
		case <-t.C:
		}
	}
}

func bundleFor(chain []*x509.Certificate, key crypto.Signer, hash, cn string) (*CertBundle, error) {
	leaf := chain[0]
	if !publicKeysEqual(leaf.PublicKey, key.Public()) {
		return nil, errors.New("plextv: issued certificate does not match our key")
	}
	if cn != "" {
		if err := leaf.VerifyHostname("x" + strings.TrimPrefix(cn, "*")); err != nil {
			return nil, fmt.Errorf("plextv: issued certificate does not cover %s: %w", cn, err)
		}
	}
	var certPEM bytes.Buffer
	for _, c := range chain {
		_ = pem.Encode(&certPEM, &pem.Block{Type: "CERTIFICATE", Bytes: c.Raw})
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	return &CertBundle{
		CertPEM: certPEM.Bytes(),
		KeyPEM:  pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
		Leaf:    leaf,
		Hash:    hash,
		Subject: cn,
	}, nil
}

func publicKeysEqual(a, b crypto.PublicKey) bool {
	type eq interface{ Equal(crypto.PublicKey) bool }
	if e, ok := a.(eq); ok {
		return e.Equal(b)
	}
	return false
}

// parseCertificates accepts PEM, DER, or JSON {"certificate": "<PEM>"}.
func parseCertificates(b []byte) ([]*x509.Certificate, error) {
	trim := bytes.TrimSpace(b)
	if bytes.HasPrefix(trim, []byte("{")) {
		var v map[string]any
		if err := json.Unmarshal(trim, &v); err == nil {
			for _, k := range []string{"certificate", "cert", "pem", "chain"} {
				if s, ok := v[k].(string); ok && s != "" {
					return parseCertificates([]byte(s))
				}
			}
		}
		return nil, errors.New("json response without certificate")
	}
	var chain []*x509.Certificate
	rest := trim
	for {
		var blk *pem.Block
		blk, rest = pem.Decode(rest)
		if blk == nil {
			break
		}
		if blk.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(blk.Bytes)
		if err != nil {
			return nil, err
		}
		chain = append(chain, c)
	}
	if len(chain) > 0 {
		return chain, nil
	}
	// DER is binary: parse the untrimmed bytes (TrimSpace could eat a
	// trailing byte that happens to be whitespace).
	if cs, err := x509.ParseCertificates(b); err == nil && len(cs) > 0 {
		return cs, nil
	}
	return nil, errors.New("no certificate in response")
}

// GenerateSelfSigned makes a self-signed certificate for hosts (DNS names
// or IPs) valid for validity. It is Ether's fallback when no plex.direct
// certificate can be obtained. Official apps do not trust it: they fall
// back to plain http on the LAN (when secure connections are "preferred")
// and refuse it for https connections, so it mostly serves browsers and
// third-party clients that let users accept it.
func GenerateSelfSigned(hosts []string, hash string, validity time.Duration) (*CertBundle, error) {
	if validity <= 0 {
		validity = 365 * 24 * time.Hour
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, err
	}
	cn := "Ether"
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(validity),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	if hash != "" {
		cn = "*." + strings.ToLower(hash) + ".plex.direct"
		tmpl.DNSNames = append(tmpl.DNSNames, cn)
	}
	seen := map[string]bool{}
	for _, h := range append([]string{"localhost", "127.0.0.1"}, hosts...) {
		h = strings.TrimSpace(h)
		if h == "" || seen[h] {
			continue
		}
		seen[h] = true
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	tmpl.Subject = pkix.Name{CommonName: cn, Organization: []string{"Ether (self-signed)"}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		return nil, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return bundleFor([]*x509.Certificate{leaf}, key, hash, "")
}

// Fingerprint is the hex SHA-256 of a certificate's DER.
func Fingerprint(c *x509.Certificate) string {
	sum := sha256.Sum256(c.Raw)
	return hex.EncodeToString(sum[:])
}
