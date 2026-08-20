// Package config parses and validates ensemble.yaml, the user-supplied
// description of a local stack's topology: services, databases, stubs,
// entities, latency defaults, seeds, and profiles.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config is the parsed contents of ensemble.yaml.
type Config struct {
	Services  map[string]Service  `yaml:"services"`
	Databases map[string]Database `yaml:"databases"`
	Stubs     map[string]Stub     `yaml:"stubs"`
	Entities  map[string]Entity   `yaml:"entities"`
	Latency   Latency             `yaml:"latency"`
	Seeds     map[string]Seed     `yaml:"seeds"`
	Profiles  map[string][]string `yaml:"profiles"`
	Redact    []string            `yaml:"redact"` // extra redaction keys
	Dir       string              `yaml:"-"`      // dir containing the config file (set by Load)
}

// Service describes one process or container ensemble supervises.
type Service struct {
	Dir       string            `yaml:"dir"`
	Build     string            `yaml:"build"`
	Watch     []string          `yaml:"watch"` // globs for build freshness
	Run       string            `yaml:"run"`
	Port      int               `yaml:"port"`  // real service port
	Proxy     int               `yaml:"proxy"` // intercept port (0 = auto-assign later)
	Env       map[string]string `yaml:"env"`
	Health    string            `yaml:"health"` // path, e.g. /healthz
	DependsOn []string          `yaml:"depends_on"`
	Docker    *DockerPlacement  `yaml:"docker"`
	Entry     bool              `yaml:"entry"`   // clients call this directly
	Profile   string            `yaml:"profile"` // "" = always on
}

// DockerPlacement runs a Service as a container instead of a native process.
type DockerPlacement struct {
	Image string            `yaml:"image"`
	Ports []string          `yaml:"ports"`
	Env   map[string]string `yaml:"env"`
}

// Database is a datastore ensemble provisions alongside services.
// Type defaults from Image when left empty; see Validate.
type Database struct {
	Image    string            `yaml:"image"`
	Port     int               `yaml:"port"`
	Type     string            `yaml:"type"` // postgres|mysql|redis|dynamodb|localstack
	Seed     string            `yaml:"seed"`
	Env      map[string]string `yaml:"env"`
	Services []string          `yaml:"services"`
}

// Stub is a config-defined fake HTTP service.
type Stub struct {
	Port   int         `yaml:"port"`
	Routes []StubRoute `yaml:"routes"`
}

// StubRoute pairs a request matcher with a canned response.
type StubRoute struct {
	Match   StubMatch   `yaml:"match"`
	Respond StubRespond `yaml:"respond"`
}

// StubMatch selects which requests a StubRoute answers.
type StubMatch struct {
	Method string `yaml:"method"`
	Path   string `yaml:"path"`
}

// StubRespond is the canned response for a matched request.
type StubRespond struct {
	Status   int               `yaml:"status"`
	Headers  map[string]string `yaml:"headers"`
	Body     string            `yaml:"body"`
	BodyFile string            `yaml:"body_file"`
	Template bool              `yaml:"template"`
}

// Entity is a dashboard plugin slot: a generic CRUD page over one resource.
type Entity struct {
	Base string `yaml:"base"`
	ID   string `yaml:"id"`
}

// Latency holds the config-defined latency injection rules.
type Latency struct {
	Defaults []LatencyDefault `yaml:"defaults"`
}

// LatencyDefault is one latency rule: fixed delay or a p50/p95/p99 distribution.
type LatencyDefault struct {
	Target  string  `yaml:"target"`
	Path    string  `yaml:"path"`
	FixedMs float64 `yaml:"fixed_ms"`
	P50     float64 `yaml:"p50"`
	P95     float64 `yaml:"p95"`
	P99     float64 `yaml:"p99"`
	Enabled bool    `yaml:"enabled"`
}

// Seed is a named seed target: SQL files and/or HTTP calls to prime a stack.
type Seed struct {
	SQL  []SeedSQL  `yaml:"sql"`
	HTTP []SeedHTTP `yaml:"http"`
}

// SeedSQL loads File (relative to Config.Dir) into Database.
type SeedSQL struct {
	Database string `yaml:"database"`
	File     string `yaml:"file"`
}

// SeedHTTP issues one HTTP request as part of a seed.
type SeedHTTP struct {
	Method  string            `yaml:"method"`
	URL     string            `yaml:"url"`
	Body    string            `yaml:"body"`
	Headers map[string]string `yaml:"headers"`
}

// Load reads, parses, and validates the ensemble.yaml at path. Dir is set to
// the directory containing path, for resolving relative references (e.g.
// SeedSQL.File) in later stages.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}

	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	c.Dir = filepath.Dir(path)

	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// ServicesForProfiles returns the services that should be active given the
// set of active profile names: every service with an empty Profile, plus
// any whose Profile appears in active.
func (c *Config) ServicesForProfiles(active []string) map[string]Service {
	activeSet := make(map[string]bool, len(active))
	for _, p := range active {
		activeSet[p] = true
	}

	out := make(map[string]Service, len(c.Services))
	for name, svc := range c.Services {
		if svc.Profile == "" || activeSet[svc.Profile] {
			out[name] = svc
		}
	}
	return out
}
