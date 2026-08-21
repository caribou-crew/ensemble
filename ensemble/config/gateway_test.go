package config

import (
	"strings"
	"testing"
)

// gatewayBase is a clean config with one proxied service, one unproxied
// service, and one stub — the three kinds of route target.
func gatewayBase() *Config {
	return &Config{
		Services: map[string]Service{
			"catalog": {Run: "./catalog", Port: 8081, Proxy: 9081},
			"legacy":  {Run: "./legacy", Port: 8090},
		},
		Databases: map[string]Database{
			"pg": {Image: "postgres:16", Port: 55432},
		},
		Stubs: map[string]Stub{
			"payments": {Port: 9099, Routes: []StubRoute{{Match: StubMatch{Path: "/charges"}}}},
		},
	}
}

func TestValidateGatewayClean(t *testing.T) {
	c := gatewayBase()
	c.Gateways = map[string]Gateway{
		"public": {Port: 9000, Routes: []GatewayRoute{
			{Prefix: "/products", Service: "catalog"},
			{Prefix: "/old/", Service: "legacy", StripPrefix: true},
			{Prefix: "/pay", Service: "payments"},
			{Prefix: "/", Service: "catalog"},
		}},
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateGatewayAbsentIsNoop(t *testing.T) {
	if err := gatewayBase().Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRoutablePort(t *testing.T) {
	c := gatewayBase()
	cases := []struct {
		name string
		port int
		kind string
		ok   bool
	}{
		{"catalog", 9081, "service", true}, // proxy wins over real port
		{"legacy", 8090, "service", true},  // no proxy: real port
		{"payments", 9099, "stub", true},
		{"pg", 0, "", false},   // databases are not routable
		{"nope", 0, "", false}, // unknown
	}
	for _, tc := range cases {
		port, kind, ok := c.RoutablePort(tc.name)
		if port != tc.port || kind != tc.kind || ok != tc.ok {
			t.Errorf("%s: got (%d, %q, %v), want (%d, %q, %v)", tc.name, port, kind, ok, tc.port, tc.kind, tc.ok)
		}
	}
}

func TestValidateGatewayRejections(t *testing.T) {
	cases := []struct {
		name string
		gw   Gateway
		want []string
	}{
		{"zero port", Gateway{Port: 0, Routes: []GatewayRoute{{Prefix: "/", Service: "catalog"}}}, []string{`gateway "public"`, "port"}},
		{"port collides with proxy", Gateway{Port: 9081, Routes: []GatewayRoute{{Prefix: "/", Service: "catalog"}}}, []string{"duplicate port 9081", "gateway public", "service catalog"}},
		{"port collides with stub", Gateway{Port: 9099, Routes: []GatewayRoute{{Prefix: "/", Service: "catalog"}}}, []string{"duplicate port 9099", "stub payments"}},
		{"no routes", Gateway{Port: 9000}, []string{`gateway "public"`, "routes"}},
		{"empty prefix", Gateway{Port: 9000, Routes: []GatewayRoute{{Prefix: "", Service: "catalog"}}}, []string{"route 0", "prefix"}},
		{"unrooted prefix", Gateway{Port: 9000, Routes: []GatewayRoute{{Prefix: "products", Service: "catalog"}}}, []string{"route 0", "prefix", "products"}},
		{"unknown target", Gateway{Port: 9000, Routes: []GatewayRoute{{Prefix: "/x", Service: "nope"}}}, []string{"route 0", `"nope"`}},
		{"database target", Gateway{Port: 9000, Routes: []GatewayRoute{{Prefix: "/x", Service: "pg"}}}, []string{"route 0", `"pg"`}},
		{"duplicate prefix", Gateway{Port: 9000, Routes: []GatewayRoute{{Prefix: "/cart", Service: "catalog"}, {Prefix: "/cart/", Service: "legacy"}}}, []string{"route 1", "duplicate prefix", "/cart"}},
	}
	for _, tc := range cases {
		c := gatewayBase()
		c.Gateways = map[string]Gateway{"public": tc.gw}
		err := c.Validate()
		if err == nil {
			t.Errorf("%s: expected error, got nil", tc.name)
			continue
		}
		for _, w := range tc.want {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("%s: error %q does not mention %q", tc.name, err, w)
			}
		}
	}
}

func TestValidateGatewayNameCollidesWithNode(t *testing.T) {
	for _, name := range []string{"catalog", "pg", "payments"} {
		c := gatewayBase()
		c.Gateways = map[string]Gateway{name: {Port: 9000, Routes: []GatewayRoute{{Prefix: "/", Service: "legacy"}}}}
		err := c.Validate()
		if err == nil || !strings.Contains(err.Error(), `gateway "`+name+`"`) || !strings.Contains(err.Error(), "same name") {
			t.Errorf("%s: want name-collision error, got %v", name, err)
		}
	}
}

func TestValidateGatewayUnroutableTarget(t *testing.T) {
	c := gatewayBase()
	c.Services["box"] = Service{Docker: &DockerPlacement{Image: "x"}} // no port at all
	c.Gateways = map[string]Gateway{"public": {Port: 9000, Routes: []GatewayRoute{{Prefix: "/", Service: "box"}}}}
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), `"box"`) || !strings.Contains(err.Error(), "no port") {
		t.Errorf("want unroutable-target error, got %v", err)
	}
}
