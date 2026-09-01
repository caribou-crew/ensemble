package config

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeTestCert writes an arbitrary (not necessarily valid) file for cases
// that never reach certificate parsing — see writeSelfSignedCert for one
// that must actually parse.
func writeTestCert(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("not-a-real-cert"), 0o600); err != nil {
		t.Fatalf("write test cert: %v", err)
	}
	return path
}

// writeSelfSignedCert writes a real self-signed cert to dir/name and returns
// its PEM-encoded private key, for the validation success path that calls
// tls.X509KeyPair.
func writeSelfSignedCert(t *testing.T, dir, name string) (certPath string, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, certPEM, 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return path, keyPEM
}

func TestValidateServiceUpstreamAloneValid(t *testing.T) {
	c := &Config{Services: map[string]Service{
		"edge": {Upstream: "https://qa.example.com", Proxy: 9001},
	}}
	if err := c.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateServiceUpstreamMissingProxy(t *testing.T) {
	c := &Config{Services: map[string]Service{
		"edge": {Upstream: "https://qa.example.com"},
	}}
	err := c.Validate()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "edge") || !strings.Contains(err.Error(), "proxy") {
		t.Errorf("error does not name the service/field: %v", err)
	}
}

func TestValidateServiceUpstreamInvalidURL(t *testing.T) {
	c := &Config{Services: map[string]Service{
		"edge": {Upstream: "not-a-url", Proxy: 9001},
	}}
	err := c.Validate()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "upstream") {
		t.Errorf("error does not mention upstream: %v", err)
	}
}

func TestValidateServicePassthroughWithoutUpstream(t *testing.T) {
	c := &Config{Services: map[string]Service{
		"edge": {Run: "./edge", Port: 8080, Passthrough: "qa"},
	}}
	err := c.Validate()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "passthrough") {
		t.Errorf("error does not mention passthrough: %v", err)
	}
}

func TestValidateServiceRunAndUpstreamBothDeclaredValid(t *testing.T) {
	// A "flippable" service: local placement AND a passthrough placement.
	c := &Config{Services: map[string]Service{
		"edge": {
			Run: "./edge", Port: 8080, Proxy: 9001,
			Upstream: "https://qa.example.com", Passthrough: "qa",
		},
	}}
	if err := c.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateServiceMissingRunDockerAndUpstream(t *testing.T) {
	c := &Config{Services: map[string]Service{
		"bff": {},
	}}
	err := c.Validate()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "bff") {
		t.Errorf("error does not name the service: %v", err)
	}
}

func TestValidateServiceClientKeyEnvWithoutCertFile(t *testing.T) {
	c := &Config{Services: map[string]Service{
		"edge": {Upstream: "https://qa.example.com", Proxy: 9001, ClientKeyEnv: "QA_KEY"},
	}}
	err := c.Validate()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "client_cert_file") {
		t.Errorf("error does not mention client_cert_file: %v", err)
	}
}

func TestValidateServiceClientCertFileWithoutKeyEnv(t *testing.T) {
	dir := t.TempDir()
	cert := writeTestCert(t, dir, "qa.crt")
	c := &Config{Dir: dir, Services: map[string]Service{
		"edge": {Upstream: "https://qa.example.com", Proxy: 9001, ClientCertFile: filepath.Base(cert)},
	}}
	err := c.Validate()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "client_key_env") {
		t.Errorf("error does not mention client_key_env: %v", err)
	}
}

func TestValidateServiceClientCertFileMissingOnDisk(t *testing.T) {
	dir := t.TempDir()
	c := &Config{Dir: dir, Services: map[string]Service{
		"edge": {
			Upstream: "https://qa.example.com", Proxy: 9001,
			ClientCertFile: "does-not-exist.crt", ClientKeyEnv: "QA_KEY",
		},
	}}
	err := c.Validate()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "does-not-exist.crt") {
		t.Errorf("error does not name the missing file: %v", err)
	}
}

func TestValidateServiceClientCertFileCleanRelativeToConfigDir(t *testing.T) {
	dir := t.TempDir()
	_, keyPEM := writeSelfSignedCert(t, dir, "qa.crt")
	t.Setenv("QA_KEY", string(keyPEM))
	c := &Config{Dir: dir, Services: map[string]Service{
		"edge": {
			Upstream: "https://qa.example.com", Proxy: 9001,
			ClientCertFile: "qa.crt", ClientKeyEnv: "QA_KEY",
		},
	}}
	if err := c.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := c.ClientCert("edge"); !ok {
		t.Error("expected ClientCert(\"edge\") to be resolved after Validate")
	}
}

func TestValidateServiceClientKeyEnvNotSet(t *testing.T) {
	dir := t.TempDir()
	writeSelfSignedCert(t, dir, "qa.crt")
	c := &Config{Dir: dir, Services: map[string]Service{
		"edge": {
			Upstream: "https://qa.example.com", Proxy: 9001,
			ClientCertFile: "qa.crt", ClientKeyEnv: "QA_KEY_NOT_SET",
		},
	}}
	err := c.Validate()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "QA_KEY_NOT_SET") {
		t.Errorf("error does not name the missing env var: %v", err)
	}
}
