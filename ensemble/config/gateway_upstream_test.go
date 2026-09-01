package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestGatewayUpstreamsFieldValid(t *testing.T) {
	c := &Config{
		Services: map[string]Service{
			"svc": {Run: "true", Port: 9001},
		},
		Gateways: map[string]Gateway{
			"public": {
				Port: 9100,
				Routes: []GatewayRoute{
					{Prefix: "/a", Service: "svc"},
				},
				Upstreams: []GatewayUpstream{
					{Name: "qa", URL: "https://qa.example.com"},
				},
			},
		},
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(c.Gateways["public"].Upstreams) != 1 {
		t.Fatalf("want 1 upstream, got %d", len(c.Gateways["public"].Upstreams))
	}
}

func TestValidateGatewayUpstreamsRejectsDuplicateNames(t *testing.T) {
	c := &Config{
		Services: map[string]Service{"svc": {Run: "true", Port: 9001}},
		Gateways: map[string]Gateway{
			"public": {
				Port:   9100,
				Routes: []GatewayRoute{{Prefix: "/a", Service: "svc"}},
				Upstreams: []GatewayUpstream{
					{Name: "qa", URL: "https://qa.example.com"},
					{Name: "qa", URL: "https://qa2.example.com"},
				},
			},
		},
	}
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), `duplicate name "qa"`) {
		t.Fatalf("want duplicate-name error, got %v", err)
	}
}

func TestValidateGatewayUpstreamsRejectsMissingName(t *testing.T) {
	c := &Config{
		Services: map[string]Service{"svc": {Run: "true", Port: 9001}},
		Gateways: map[string]Gateway{
			"public": {
				Port:      9100,
				Routes:    []GatewayRoute{{Prefix: "/a", Service: "svc"}},
				Upstreams: []GatewayUpstream{{URL: "https://qa.example.com"}},
			},
		},
	}
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "name is required") {
		t.Fatalf("want name-required error, got %v", err)
	}
}

func TestValidateGatewayUpstreamsRejectsInvalidURL(t *testing.T) {
	c := &Config{
		Services: map[string]Service{"svc": {Run: "true", Port: 9001}},
		Gateways: map[string]Gateway{
			"public": {
				Port:      9100,
				Routes:    []GatewayRoute{{Prefix: "/a", Service: "svc"}},
				Upstreams: []GatewayUpstream{{Name: "qa", URL: "not-a-url"}},
			},
		},
	}
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "not a valid http(s) URL") {
		t.Fatalf("want invalid-url error, got %v", err)
	}
}

func TestValidateGatewayUpstreamsClientKeyEnvWithoutCertFile(t *testing.T) {
	c := &Config{
		Services: map[string]Service{"svc": {Run: "true", Port: 9001}},
		Gateways: map[string]Gateway{
			"public": {
				Port:   9100,
				Routes: []GatewayRoute{{Prefix: "/a", Service: "svc"}},
				Upstreams: []GatewayUpstream{
					{Name: "qa", URL: "https://qa.example.com", ClientKeyEnv: "QA_KEY"},
				},
			},
		},
	}
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "client_cert_file") {
		t.Fatalf("want client_cert_file error, got %v", err)
	}
}

func TestValidateGatewayUpstreamsClientCertFileWithoutKeyEnv(t *testing.T) {
	dir := t.TempDir()
	cert := writeTestCert(t, dir, "qa.crt")
	c := &Config{
		Dir:      dir,
		Services: map[string]Service{"svc": {Run: "true", Port: 9001}},
		Gateways: map[string]Gateway{
			"public": {
				Port:   9100,
				Routes: []GatewayRoute{{Prefix: "/a", Service: "svc"}},
				Upstreams: []GatewayUpstream{
					{Name: "qa", URL: "https://qa.example.com", ClientCertFile: filepath.Base(cert)},
				},
			},
		},
	}
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "client_key_env") {
		t.Fatalf("want client_key_env error, got %v", err)
	}
}

func TestValidateGatewayUpstreamsClientCertFileMissingOnDisk(t *testing.T) {
	dir := t.TempDir()
	c := &Config{
		Dir:      dir,
		Services: map[string]Service{"svc": {Run: "true", Port: 9001}},
		Gateways: map[string]Gateway{
			"public": {
				Port:   9100,
				Routes: []GatewayRoute{{Prefix: "/a", Service: "svc"}},
				Upstreams: []GatewayUpstream{
					{Name: "qa", URL: "https://qa.example.com", ClientCertFile: "does-not-exist.crt", ClientKeyEnv: "QA_KEY"},
				},
			},
		},
	}
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "does-not-exist.crt") {
		t.Fatalf("want missing-file error, got %v", err)
	}
}

func TestValidateGatewayUpstreamsClientKeyEnvNotSet(t *testing.T) {
	dir := t.TempDir()
	writeSelfSignedCert(t, dir, "qa.crt")
	c := &Config{
		Dir:      dir,
		Services: map[string]Service{"svc": {Run: "true", Port: 9001}},
		Gateways: map[string]Gateway{
			"public": {
				Port:   9100,
				Routes: []GatewayRoute{{Prefix: "/a", Service: "svc"}},
				Upstreams: []GatewayUpstream{
					{Name: "qa", URL: "https://qa.example.com", ClientCertFile: "qa.crt", ClientKeyEnv: "QA_KEY_NOT_SET"},
				},
			},
		},
	}
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "QA_KEY_NOT_SET") {
		t.Fatalf("want missing-env error, got %v", err)
	}
}

func TestGatewayUpstreamClientCertCachedAndNamespacedFromServiceCerts(t *testing.T) {
	dir := t.TempDir()
	_, keyPEM := writeSelfSignedCert(t, dir, "qa.crt")
	t.Setenv("QA_CLIENT_KEY", string(keyPEM))
	c := &Config{
		Dir: dir,
		// A service happens to be named "qa" too (the gateway's upstream
		// tag) — the gateway's cert cache must be keyed by gateway name,
		// not by the bare upstream/service name, so this can never
		// collide with Config.ClientCert("qa").
		Services: map[string]Service{
			"qa": {Run: "true", Port: 9001},
		},
		Gateways: map[string]Gateway{
			"public": {
				Port:   9100,
				Routes: []GatewayRoute{{Prefix: "/a", Service: "qa"}},
				Upstreams: []GatewayUpstream{
					{Name: "qa", URL: "https://qa.example.com", ClientCertFile: "qa.crt", ClientKeyEnv: "QA_CLIENT_KEY"},
				},
			},
		},
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := c.GatewayUpstreamClientCert("public", "qa"); !ok {
		t.Fatal("want gateway upstream cert to be cached")
	}
	if _, ok := c.ClientCert("qa"); ok {
		t.Fatal("service \"qa\" declared no client cert of its own; ClientCert must not see the gateway's")
	}
}
