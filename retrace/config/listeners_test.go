package config

import (
	"os"
	"path/filepath"
	"testing"
)

func loadCfgOrErr(t *testing.T, yaml string) (*Config, error) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "retrace.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	return Load(path)
}

func loadCfg(t *testing.T, yaml string) *Config {
	t.Helper()
	cfg, err := loadCfgOrErr(t, yaml)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg
}

func TestBareUpstreamSynthesizesOneClientEdgeListener(t *testing.T) {
	cfg := loadCfg(t, "app: web\nupstream: http://localhost:4000\n")
	if len(cfg.Listeners) != 1 {
		t.Fatalf("got %d listeners, want 1", len(cfg.Listeners))
	}
	l := cfg.Listeners[0]
	if l.Name != "client-edge" {
		t.Errorf("name = %q, want client-edge", l.Name)
	}
	if l.Upstream != "http://localhost:4000" {
		t.Errorf("upstream = %q, want http://localhost:4000", l.Upstream)
	}
}

func TestBareUpstreamSynthesisCarriesHostAndPort(t *testing.T) {
	cfg := loadCfg(t, "app: web\nupstream: http://localhost:4000\nproxy_host: 127.0.0.1\nproxy_port: 4800\n")
	l := cfg.Listeners[0]
	if l.Host != "127.0.0.1" || l.Port != 4800 {
		t.Errorf("host/port = %q/%d, want 127.0.0.1/4800", l.Host, l.Port)
	}
}

func TestNoUpstreamNoEntrySynthesizesNoListeners(t *testing.T) {
	cfg := loadCfg(t, "app: web\n")
	if len(cfg.Listeners) != 0 {
		t.Fatalf("got %d listeners, want 0", len(cfg.Listeners))
	}
}

func TestExplicitListenersParse(t *testing.T) {
	cfg := loadCfg(t, `app: web
listeners:
  - name: edge
    upstream: http://localhost:4000
  - name: auth
    upstream: http://localhost:4050
    host: 127.0.0.1
    port: 4850
`)
	if len(cfg.Listeners) != 2 {
		t.Fatalf("got %d listeners, want 2", len(cfg.Listeners))
	}
	if cfg.Listeners[0].Name != "edge" || cfg.Listeners[0].Upstream != "http://localhost:4000" {
		t.Errorf("listener 0 = %+v", cfg.Listeners[0])
	}
	if cfg.Listeners[1].Name != "auth" || cfg.Listeners[1].Port != 4850 || cfg.Listeners[1].Host != "127.0.0.1" {
		t.Errorf("listener 1 = %+v", cfg.Listeners[1])
	}
}

func TestListenersWithUpstreamIsALoadError(t *testing.T) {
	_, err := loadCfgOrErr(t, "app: web\nupstream: http://localhost:4000\nlisteners:\n  - name: edge\n    upstream: http://localhost:4001\n")
	if err == nil {
		t.Fatal("want error, got nil")
	}
}

func TestListenersWithProxyPortIsALoadError(t *testing.T) {
	_, err := loadCfgOrErr(t, "app: web\nproxy_port: 4800\nlisteners:\n  - name: edge\n    upstream: http://localhost:4001\n")
	if err == nil {
		t.Fatal("want error, got nil")
	}
}

func TestListenersWithEntryIsALoadError(t *testing.T) {
	_, err := loadCfgOrErr(t, "app: web\nentry: edge\nlisteners:\n  - name: edge\n    upstream: http://localhost:4001\n")
	if err == nil {
		t.Fatal("want error, got nil")
	}
}

func TestEntryWithBareUpstreamFallbackIsUnaffected(t *testing.T) {
	// The sample stack's own pattern: entry: for the ensemble-attached
	// path, upstream: as the --no-ensemble fallback. Must NOT trip the
	// listeners:+entry: mutual-exclusion check, since no listeners: was
	// ever written by hand here.
	cfg := loadCfg(t, "app: web\nentry: edge\nupstream: http://localhost:4000\n")
	if len(cfg.Listeners) != 1 || cfg.Listeners[0].Name != "client-edge" {
		t.Fatalf("got %+v, want one synthesized client-edge listener", cfg.Listeners)
	}
}

func TestListenerEmptyNameIsALoadError(t *testing.T) {
	_, err := loadCfgOrErr(t, "app: web\nlisteners:\n  - name: \"\"\n    upstream: http://localhost:4001\n")
	if err == nil {
		t.Fatal("want error, got nil")
	}
}

func TestListenerDuplicateNameIsALoadError(t *testing.T) {
	_, err := loadCfgOrErr(t, `app: web
listeners:
  - name: edge
    upstream: http://localhost:4000
  - name: edge
    upstream: http://localhost:4001
`)
	if err == nil {
		t.Fatal("want error, got nil")
	}
}

func TestListenerMissingUpstreamIsALoadError(t *testing.T) {
	_, err := loadCfgOrErr(t, "app: web\nlisteners:\n  - name: edge\n")
	if err == nil {
		t.Fatal("want error, got nil")
	}
}

func TestListenerEntryEnvSuffix(t *testing.T) {
	cases := map[string]string{
		"edge":          "EDGE",
		"card-api":      "CARD_API",
		"Auth Service":  "AUTH_SERVICE",
		"--weird__name": "WEIRD_NAME",
	}
	for name, want := range cases {
		got := ListenerEntry{Name: name}.EnvSuffix()
		if got != want {
			t.Errorf("EnvSuffix(%q) = %q, want %q", name, got, want)
		}
	}
}
