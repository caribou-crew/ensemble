package proxy

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// generateSelfSignedForTest returns a ready-to-use client tls.Certificate
// and its DER-encoded cert bytes (for building a verifying CA pool) — a
// self-signed cert is its own issuer, so adding it directly to a
// x509.CertPool is enough to make a server trust exactly that one client
// identity.
func generateSelfSignedForTest(t *testing.T) (tls.Certificate, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test-client"},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
		// A self-signed cert used as its own trust root needs the CA bit —
		// crypto/x509's chain verifier rejects a v3 cert as an issuer
		// (even of itself) without BasicConstraintsValid+IsCA.
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	return cert, der
}

func mustParseCert(t *testing.T, der []byte) *x509.Certificate {
	t.Helper()
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}
	return cert
}

func TestPassthroughRefusesWriteByDefault(t *testing.T) {
	rec := NewRecorder(RecorderOpts{Ring: 8})
	p := New(rec)
	defer p.Close()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("upstream should never be called for a refused write, got %s", r.Method)
	}))
	defer upstream.Close()

	addr, err := p.Serve(Target{Name: "edge", Listen: "127.0.0.1:0", Upstream: upstream.URL, Passthrough: true})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post("http://"+addr+"/cards", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("want 502, got %d", resp.StatusCode)
	}
	h := rec.Snapshot()[0]
	if h.Status != http.StatusBadGateway || h.Err == "" {
		t.Fatalf("refusal not recorded as a hop: %+v", h)
	}
}

func TestPassthroughAllowsGETByDefault(t *testing.T) {
	rec := NewRecorder(RecorderOpts{Ring: 8})
	p := New(rec)
	defer p.Close()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	addr, err := p.Serve(Target{Name: "edge", Listen: "127.0.0.1:0", Upstream: upstream.URL, Passthrough: true})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get("http://" + addr + "/cards")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
}

func TestPassthroughAllowWritesPermitsPost(t *testing.T) {
	rec := NewRecorder(RecorderOpts{Ring: 8})
	p := New(rec)
	defer p.Close()

	called := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	addr, err := p.Serve(Target{Name: "edge", Listen: "127.0.0.1:0", Upstream: upstream.URL, Passthrough: true, AllowWrites: true})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post("http://"+addr+"/cards", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !called {
		t.Fatalf("write should have reached upstream: status=%d called=%v", resp.StatusCode, called)
	}
}

func TestNonPassthroughTargetAllowsWritesUnaffected(t *testing.T) {
	rec := NewRecorder(RecorderOpts{Ring: 8})
	p := New(rec)
	defer p.Close()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	addr, err := p.Serve(Target{Name: "svc", Listen: "127.0.0.1:0", Upstream: upstream.URL})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post("http://"+addr+"/cards", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("a plain local target must never be write-guarded: got %d", resp.StatusCode)
	}
}

func TestPassthroughMTLSDialsWithConfiguredClientCert(t *testing.T) {
	clientCert, clientCertPEM := generateSelfSignedForTest(t)
	pool := x509.NewCertPool()
	pool.AddCert(mustParseCert(t, clientCertPEM))

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	srv.TLS = &tls.Config{ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool}
	srv.StartTLS()
	defer srv.Close()

	rec := NewRecorder(RecorderOpts{Ring: 8})
	p := New(rec)
	defer p.Close()
	// httptest's server cert is self-signed and not in any trust store;
	// production dials a real CA-signed QA cert, so this is test-only setup,
	// not something transportFor exposes as a config knob.
	p.transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}

	addr, err := p.Serve(Target{Name: "edge", Listen: "127.0.0.1:0", Upstream: srv.URL, TLS: &clientCert})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get("http://" + addr + "/cards")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200 with a valid client cert, got %d", resp.StatusCode)
	}
}

func TestPassthroughMTLSFailsWithoutClientCert(t *testing.T) {
	_, clientCertPEM := generateSelfSignedForTest(t)
	pool := x509.NewCertPool()
	pool.AddCert(mustParseCert(t, clientCertPEM))

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	srv.TLS = &tls.Config{ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool}
	srv.StartTLS()
	defer srv.Close()

	rec := NewRecorder(RecorderOpts{Ring: 8})
	p := New(rec)
	defer p.Close()
	p.transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}

	// No TLS set on the Target — the shared, no-client-cert transport is
	// used, and the server must reject the handshake.
	addr, err := p.Serve(Target{Name: "edge", Listen: "127.0.0.1:0", Upstream: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get("http://" + addr + "/cards")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("want 502 (TLS handshake should fail with no client cert), got %d", resp.StatusCode)
	}
}
