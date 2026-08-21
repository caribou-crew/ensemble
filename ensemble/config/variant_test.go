package config

import (
	"strings"
	"testing"
)

func variantBase() *Config {
	return &Config{Services: map[string]Service{
		"monolith": {
			Port: 8081, Proxy: 9081, Health: "/healthz", DependsOn: []string{"pg"}, Entry: true,
			Default: "stub",
			Variants: map[string]Variant{
				"stub": {Dir: "./stub", Build: "go build -o stub .", Run: "./stub", Env: map[string]string{"MODE": "stub"}},
				"real": {Dir: "../java", Build: "./gradlew bootJar", Run: "java -jar app.jar", StartupTimeoutS: 120, Watch: []string{"src/**"}},
			},
		},
		"plain": {Run: "./plain", Port: 8090},
	}, Databases: map[string]Database{"pg": {Image: "postgres:16", Port: 55432}}}
}

func TestValidateVariantsClean(t *testing.T) {
	if err := variantBase().Validate(); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestResolveService(t *testing.T) {
	c := variantBase()
	got, err := c.ResolveService("monolith", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Run != "./stub" || got.Dir != "./stub" || got.Env["MODE"] != "stub" || got.Port != 8081 || got.Proxy != 9081 || got.Health != "/healthz" || !got.Entry || len(got.DependsOn) != 1 {
		t.Errorf("default resolve = %+v", got)
	}
	if got.Variants != nil || got.Default != "" {
		t.Errorf("resolved service must not carry variants: %+v", got)
	}
	real, err := c.ResolveService("monolith", "real")
	if err != nil {
		t.Fatal(err)
	}
	if real.Run != "java -jar app.jar" || real.StartupTimeoutS != 120 || len(real.Watch) != 1 || real.Env != nil || real.Port != 8081 {
		t.Errorf("real resolve = %+v", real)
	}
	if _, err := c.ResolveService("monolith", "prod"); err == nil || !strings.Contains(err.Error(), `"prod"`) || !strings.Contains(err.Error(), "real, stub") {
		t.Errorf("unknown variant: %v", err)
	}
	plain, err := c.ResolveService("plain", "")
	if err != nil || plain.Run != "./plain" {
		t.Errorf("plain: %+v %v", plain, err)
	}
	if _, err := c.ResolveService("plain", "x"); err == nil || !strings.Contains(err.Error(), "no variants") {
		t.Errorf("plain with variant: %v", err)
	}
	if _, err := c.ResolveService("nope", ""); err == nil {
		t.Error("unknown service should error")
	}
}

func TestDefaultVariantSingleNeedsNoDefault(t *testing.T) {
	c := variantBase()
	svc := c.Services["monolith"]
	svc.Default = ""
	delete(svc.Variants, "real")
	c.Services["monolith"] = svc
	if err := c.Validate(); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if got := c.Services["monolith"].DefaultVariant(); got != "stub" {
		t.Errorf("DefaultVariant = %q", got)
	}
	if names := c.Services["monolith"].VariantNames(); strings.Join(names, ",") != "stub" {
		t.Errorf("VariantNames = %v", names)
	}
}

func TestValidateVariantRejections(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Service)
		want []string
	}{
		{"backing on both levels", func(s *Service) { s.Run = "./x" }, []string{"monolith", "on the variants, not the service"}},
		{"dir on both levels", func(s *Service) { s.Dir = "./x" }, []string{"on the variants, not the service"}},
		{"unknown default", func(s *Service) { s.Default = "prod" }, []string{`default "prod"`, "real, stub"}},
		{"missing default", func(s *Service) { s.Default = "" }, []string{"default is required"}},
		{"variant without run/docker", func(s *Service) { s.Variants["empty"] = Variant{} }, []string{`variant "empty"`, "run or docker"}},
		{"variant run without port", func(s *Service) { s.Port = 0 }, []string{`variant "stub"`, "port is 0"}},
		{"negative timeout", func(s *Service) { v := s.Variants["real"]; v.StartupTimeoutS = -1; s.Variants["real"] = v }, []string{`variant "real"`, "startup_timeout_s"}},
		{"default without variants", func(s *Service) { s.Variants = nil; s.Run = "./x" }, []string{"default is set but no variants"}},
	}
	for _, tc := range cases {
		c := variantBase()
		svc := c.Services["monolith"]
		tc.mut(&svc)
		c.Services["monolith"] = svc
		err := c.Validate()
		if err == nil {
			t.Errorf("%s: expected error", tc.name)
			continue
		}
		for _, w := range tc.want {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("%s: %q lacks %q", tc.name, err, w)
			}
		}
	}
}

func TestValidateDockerArgsEmptyEntry(t *testing.T) {
	c := &Config{Services: map[string]Service{
		"box": {Docker: &DockerPlacement{Image: "x", Args: []string{"--network=host", " "}}},
	}}
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "docker.args[1] is empty") {
		t.Fatalf("err = %v", err)
	}
	c = variantBase()
	svc := c.Services["monolith"]
	svc.Variants["box"] = Variant{Docker: &DockerPlacement{Image: "x", Args: []string{""}}}
	c.Services["monolith"] = svc
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), `variant "box": docker.args[0] is empty`) {
		t.Fatalf("variant err = %v", err)
	}
}
