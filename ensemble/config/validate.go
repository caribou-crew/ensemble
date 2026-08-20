package config

import (
	"errors"
	"fmt"
	"strings"
)

// validDatabaseTypes are the emulators ensemble knows how to provision.
var validDatabaseTypes = map[string]bool{
	"postgres":   true,
	"mysql":      true,
	"redis":      true,
	"dynamodb":   true,
	"localstack": true,
}

// Validate checks a Config for internal consistency, applying defaults
// (currently: Database.Type inferred from Database.Image) along the way.
// All violations found are reported together, joined via errors.Join.
func (c *Config) Validate() error {
	var errs []error

	for name, db := range c.Databases {
		if db.Type == "" {
			db.Type = inferDatabaseType(db.Image)
		}
		if !validDatabaseTypes[db.Type] {
			errs = append(errs, fmt.Errorf("database %q: invalid type %q (image %q)", name, db.Type, db.Image))
		}
		c.Databases[name] = db
	}

	proxyPorts := make(map[int][]string)

	for name, svc := range c.Services {
		if svc.Run == "" && svc.Docker == nil {
			errs = append(errs, fmt.Errorf("service %q: must set run or docker", name))
		}
		if svc.Run != "" && svc.Port == 0 {
			errs = append(errs, fmt.Errorf("service %q: run is set but port is 0", name))
		}
		if svc.Proxy != 0 {
			proxyPorts[svc.Proxy] = append(proxyPorts[svc.Proxy], "service "+name)
		}
		for _, dep := range svc.DependsOn {
			if !c.hasServiceOrDatabase(dep) {
				errs = append(errs, fmt.Errorf("service %q: depends_on references unknown service/database %q", name, dep))
			}
		}
	}

	for name, stub := range c.Stubs {
		if stub.Port != 0 {
			proxyPorts[stub.Port] = append(proxyPorts[stub.Port], "stub "+name)
		}
		for i, route := range stub.Routes {
			if route.Match.Path == "" {
				errs = append(errs, fmt.Errorf("stub %q: route %d: match.path is empty", name, i))
			}
		}
	}

	for port, names := range proxyPorts {
		if len(names) > 1 {
			errs = append(errs, fmt.Errorf("duplicate proxy port %d: %s", port, strings.Join(names, ", ")))
		}
	}

	for name, seed := range c.Seeds {
		for _, sql := range seed.SQL {
			if _, ok := c.Databases[sql.Database]; !ok {
				errs = append(errs, fmt.Errorf("seed %q: sql references unknown database %q", name, sql.Database))
			}
		}
	}

	for name, ent := range c.Entities {
		if ent.Base == "" {
			errs = append(errs, fmt.Errorf("entity %q: base is empty", name))
		}
	}

	return errors.Join(errs...)
}

// hasServiceOrDatabase reports whether name identifies a declared service or
// database, the two kinds of thing depends_on may reference.
func (c *Config) hasServiceOrDatabase(name string) bool {
	if _, ok := c.Services[name]; ok {
		return true
	}
	if _, ok := c.Databases[name]; ok {
		return true
	}
	return false
}

// inferDatabaseType guesses a Database's Type from its Image when Type is
// left unset in ensemble.yaml.
func inferDatabaseType(image string) string {
	switch {
	case strings.Contains(image, "postgres"):
		return "postgres"
	case strings.Contains(image, "mysql"):
		return "mysql"
	case strings.Contains(image, "redis"):
		return "redis"
	case strings.Contains(image, "dynamodb"):
		return "dynamodb"
	case strings.Contains(image, "localstack"):
		return "localstack"
	default:
		return ""
	}
}
