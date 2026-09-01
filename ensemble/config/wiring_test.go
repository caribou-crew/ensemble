package config

import "testing"

// TestWiringWarningsSampleStackClean is the spec's integration guarantee:
// sample/ensemble.yaml deliberately wires every cross-service env: value
// through the target's proxy: port, so it must validate with ZERO wiring
// warnings. A regression here means the scan logic itself is wrong — the
// fix belongs in WiringWarnings, never in the sample config (see task
// 3.5's brief).
func TestWiringWarningsSampleStackClean(t *testing.T) {
	c, err := Load("../../sample/ensemble.yaml")
	if err != nil {
		t.Fatalf("Load(sample/ensemble.yaml): %v", err)
	}
	variants := map[string]string{}
	for name, svc := range c.Services {
		if len(svc.Variants) > 0 {
			variants[name] = svc.DefaultVariant()
		}
	}
	if warnings := c.WiringWarnings(variants); len(warnings) != 0 {
		t.Errorf("expected sample/ensemble.yaml to validate with zero wiring warnings, got %+v", warnings)
	}
}

// --- WiringWarnings: proxy-wiring-validation task 3.5 scenarios ---

func baseWiringConfig() *Config {
	return &Config{
		Services: map[string]Service{
			"edge": {
				Run: "x", Port: 8080, Proxy: 9080,
				Env: map[string]string{"CATALOG_URL": "http://127.0.0.1:8081"},
			},
			"catalog": {
				Run: "x", Port: 8081, Proxy: 9081,
			},
		},
	}
}

func TestWiringWarningsRealPortHit(t *testing.T) {
	c := baseWiringConfig()
	warnings := c.WiringWarnings(nil)
	if len(warnings) != 1 {
		t.Fatalf("expected 1 warning, got %d: %+v", len(warnings), warnings)
	}
	w := warnings[0]
	if w.Service != "edge" || w.Env != "CATALOG_URL" || w.Target != "catalog" || w.Port != 8081 || w.ProxyPort != 9081 {
		t.Errorf("unexpected warning: %+v", w)
	}
	if w.Message == "" {
		t.Error("expected a human-readable Message")
	}
}

func TestWiringWarningsProxyPortClean(t *testing.T) {
	c := baseWiringConfig()
	edge := c.Services["edge"]
	edge.Env = map[string]string{"CATALOG_URL": "http://127.0.0.1:9081"}
	c.Services["edge"] = edge

	if warnings := c.WiringWarnings(nil); len(warnings) != 0 {
		t.Errorf("expected no warnings for a correctly-wired proxy port, got %+v", warnings)
	}
}

func TestWiringWarningsNoProxyTargetClean(t *testing.T) {
	c := &Config{Services: map[string]Service{
		"edge":   {Run: "x", Port: 8080, Proxy: 9080, Env: map[string]string{"WORKER_URL": "http://127.0.0.1:8090"}},
		"worker": {Run: "x", Port: 8090}, // no proxy: declared
	}}
	if warnings := c.WiringWarnings(nil); len(warnings) != 0 {
		t.Errorf("expected no warnings when the target declares no proxy, got %+v", warnings)
	}
}

func TestWiringWarningsDatabaseStubGatewayPortsClean(t *testing.T) {
	c := &Config{
		Services: map[string]Service{
			"edge": {Run: "x", Port: 8080, Proxy: 9080, Env: map[string]string{
				"DATABASE_URL": "postgres://u:p@127.0.0.1:55432/db",
				"PAYMENTS_URL": "http://127.0.0.1:9099",
				"GATEWAY_URL":  "http://127.0.0.1:9100",
			}},
		},
		Databases: map[string]Database{"pg": {Port: 55432}},
		Stubs:     map[string]Stub{"payments": {Port: 9099}},
		Gateways:  map[string]Gateway{"public": {Port: 9100, Routes: []GatewayRoute{{Prefix: "/", Service: "edge"}}}},
	}
	if warnings := c.WiringWarnings(nil); len(warnings) != 0 {
		t.Errorf("expected no warnings for database/stub/gateway ports, got %+v", warnings)
	}
}

func TestWiringWarningsUndeclaredPortClean(t *testing.T) {
	c := &Config{Services: map[string]Service{
		"edge": {Run: "x", Port: 8080, Proxy: 9080, Env: map[string]string{"THIRD_PARTY": "http://127.0.0.1:5000"}},
	}}
	if warnings := c.WiringWarnings(nil); len(warnings) != 0 {
		t.Errorf("expected no warnings for a port no node declares, got %+v", warnings)
	}
}

func TestWiringWarningsSelfReferenceClean(t *testing.T) {
	c := &Config{Services: map[string]Service{
		"edge": {Run: "x", Port: 8080, Proxy: 9080, Env: map[string]string{"SELF_URL": "http://127.0.0.1:8080"}},
	}}
	if warnings := c.WiringWarnings(nil); len(warnings) != 0 {
		t.Errorf("expected no warning for a service referencing its OWN real port, got %+v", warnings)
	}
}

func TestWiringWarningsLocalhostAndDockerHostForms(t *testing.T) {
	c := &Config{Services: map[string]Service{
		"edge": {Run: "x", Port: 8080, Proxy: 9080, Env: map[string]string{
			"A": "http://localhost:8081",
			"B": "http://host.docker.internal:8081",
		}},
		"catalog": {Run: "x", Port: 8081, Proxy: 9081},
	}}
	warnings := c.WiringWarnings(nil)
	if len(warnings) != 2 {
		t.Fatalf("expected 2 warnings (localhost + host.docker.internal forms), got %d: %+v", len(warnings), warnings)
	}
}

// TestWiringWarningsVariantScoped covers the spec's "Only one variant
// mis-wired" scenario: the warning appears only while the mis-wired
// variant is the one WiringWarnings is asked to evaluate.
func TestWiringWarningsVariantScoped(t *testing.T) {
	c := &Config{Services: map[string]Service{
		"edge": {
			Port: 8080, Proxy: 9080, Default: "real",
			Variants: map[string]Variant{
				"stub": {Run: "x", Env: map[string]string{"CATALOG_URL": "http://127.0.0.1:9081"}},
				"real": {Run: "x", Env: map[string]string{"CATALOG_URL": "http://127.0.0.1:8081"}},
			},
		},
		"catalog": {Run: "x", Port: 8081, Proxy: 9081},
	}}

	if warnings := c.WiringWarnings(map[string]string{"edge": "stub"}); len(warnings) != 0 {
		t.Errorf("expected no warning while variant %q is active, got %+v", "stub", warnings)
	}

	warnings := c.WiringWarnings(map[string]string{"edge": "real"})
	if len(warnings) != 1 {
		t.Fatalf("expected 1 warning while variant %q is active, got %+v", "real", warnings)
	}
	if warnings[0].Variant != "real" {
		t.Errorf("warning.Variant = %q, want %q", warnings[0].Variant, "real")
	}
}

func TestWiringWarningsDeterministicOrder(t *testing.T) {
	c := &Config{Services: map[string]Service{
		"edge": {Run: "x", Port: 8080, Proxy: 9080, Env: map[string]string{
			"A_URL": "http://127.0.0.1:8081",
			"Z_URL": "http://127.0.0.1:8082",
		}},
		"catalog": {Run: "x", Port: 8081, Proxy: 9081},
		"user":    {Run: "x", Port: 8082, Proxy: 9082},
	}}
	warnings := c.WiringWarnings(nil)
	if len(warnings) != 2 || warnings[0].Env != "A_URL" || warnings[1].Env != "Z_URL" {
		t.Fatalf("expected env-key-sorted order [A_URL Z_URL], got %+v", warnings)
	}
}
