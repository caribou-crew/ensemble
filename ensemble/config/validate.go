package config

import (
	"errors"
	"fmt"
	"regexp"
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

	// usedPorts tracks every port ensemble or a supervised process binds on
	// 127.0.0.1 — Service.Port (the real process), Service.Proxy (the
	// intercept listener), Database.Port, and Stub.Port (a stub IS its own
	// real backend, so its bind port lives in the same space as a
	// service's) — one shared address space, so any two of these
	// colliding is a defect regardless of which kind either one is. Without
	// this, e.g. two services declaring the same real Port validated
	// clean, and the health gate (which polls the real port) then saw
	// whichever service's process happened to be listening and reported
	// BOTH healthy, while wireProxy silently misrouted one service's
	// intercept port at the other's process.
	usedPorts := make(map[int][]string)

	for name, db := range c.Databases {
		if db.Type == "" {
			db.Type = inferDatabaseType(db.Image)
		}
		if !validDatabaseTypes[db.Type] {
			errs = append(errs, fmt.Errorf("database %q: invalid type %q (image %q)", name, db.Type, db.Image))
		}
		if db.Port != 0 {
			usedPorts[db.Port] = append(usedPorts[db.Port], "database "+name)
		}
		c.Databases[name] = db
	}

	for name, svc := range c.Services {
		if len(svc.Variants) > 0 {
			errs = append(errs, c.validateVariants(name, svc)...)
		} else {
			if svc.Default != "" {
				errs = append(errs, fmt.Errorf("service %q: default is set but no variants are declared", name))
			}
			if svc.Run == "" && svc.Docker == nil {
				errs = append(errs, fmt.Errorf("service %q: must set run or docker", name))
			}
			if svc.Run != "" && svc.Port == 0 {
				errs = append(errs, fmt.Errorf("service %q: run is set but port is 0", name))
			}
		}
		if svc.Port != 0 {
			usedPorts[svc.Port] = append(usedPorts[svc.Port], "service "+name)
		}
		if svc.Proxy != 0 {
			usedPorts[svc.Proxy] = append(usedPorts[svc.Proxy], "service "+name)
		}
		if svc.StartupTimeoutS < 0 {
			errs = append(errs, fmt.Errorf("service %q: startup_timeout_s must be >= 0", name))
		}
		errs = append(errs, validateDockerArgs(fmt.Sprintf("service %q", name), svc.Docker)...)
		for _, dep := range svc.DependsOn {
			if !c.hasServiceOrDatabase(dep) {
				errs = append(errs, fmt.Errorf("service %q: depends_on references unknown service/database %q", name, dep))
			}
		}
	}

	for name, stub := range c.Stubs {
		if stub.Port != 0 {
			usedPorts[stub.Port] = append(usedPorts[stub.Port], "stub "+name)
		}
		for i, route := range stub.Routes {
			if route.Match.Path == "" {
				errs = append(errs, fmt.Errorf("stub %q: route %d: match.path is empty", name, i))
			}
		}
	}

	for name, gw := range c.Gateways {
		if c.hasServiceOrDatabase(name) || c.hasStub(name) {
			errs = append(errs, fmt.Errorf("gateway %q: a service, database, or stub has the same name", name))
		}
		if gw.Port <= 0 {
			errs = append(errs, fmt.Errorf("gateway %q: port must be set", name))
		} else {
			usedPorts[gw.Port] = append(usedPorts[gw.Port], "gateway "+name)
		}
		if len(gw.Routes) == 0 {
			errs = append(errs, fmt.Errorf("gateway %q: routes is empty", name))
		}
		seenPrefix := make(map[string]bool, len(gw.Routes))
		seenRegex := make(map[string]bool, len(gw.Routes))
		for i, route := range gw.Routes {
			switch {
			case route.Prefix == "" && route.Regex == "", route.Prefix != "" && route.Regex != "":
				errs = append(errs, fmt.Errorf("gateway %q: route %d: exactly one of prefix or regex must be set", name, i))
			case route.Prefix != "":
				if !strings.HasPrefix(route.Prefix, "/") {
					errs = append(errs, fmt.Errorf("gateway %q: route %d: prefix %q must start with /", name, i, route.Prefix))
					break
				}
				p := normalizeGatewayPrefix(route.Prefix)
				if seenPrefix[p] {
					errs = append(errs, fmt.Errorf("gateway %q: route %d: duplicate prefix %q", name, i, p))
				}
				seenPrefix[p] = true
			default: // route.Regex != ""
				if route.StripPrefix {
					errs = append(errs, fmt.Errorf("gateway %q: route %d: strip_prefix is only valid with prefix", name, i))
				}
				if _, err := regexp.Compile(route.Regex); err != nil {
					errs = append(errs, fmt.Errorf("gateway %q: route %d: invalid regex %q: %v", name, i, route.Regex, err))
				} else if seenRegex[route.Regex] {
					errs = append(errs, fmt.Errorf("gateway %q: route %d: duplicate regex %q", name, i, route.Regex))
				}
				seenRegex[route.Regex] = true
			}
			if _, _, ok := c.RoutablePort(route.Service); !ok {
				switch {
				case route.Service == "":
					errs = append(errs, fmt.Errorf("gateway %q: route %d: service is empty", name, i))
				case c.hasServiceOrDatabase(route.Service) || c.hasStub(route.Service):
					errs = append(errs, fmt.Errorf("gateway %q: route %d: %q has no port to route to (only services and stubs with a port are routable)", name, i, route.Service))
				default:
					errs = append(errs, fmt.Errorf("gateway %q: route %d: references unknown service/stub %q", name, i, route.Service))
				}
			}
		}
	}

	for port, names := range usedPorts {
		if len(names) > 1 {
			errs = append(errs, fmt.Errorf("duplicate port %d: %s", port, strings.Join(names, ", ")))
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

	for _, name := range c.OnReady.Seeds {
		if _, ok := c.Seeds[name]; !ok {
			errs = append(errs, fmt.Errorf("on_ready: seeds references unknown seed %q", name))
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

// validateVariants applies the variant rules to a service declaring
// variants: backing fields live on the variants only, every variant is
// runnable, and the default is unambiguous.
func (c *Config) validateVariants(name string, svc Service) []error {
	var errs []error
	if svc.hasBackingFields() {
		errs = append(errs, fmt.Errorf("service %q: dir/build/watch/run/env/docker/startup_timeout_s must be set on the variants, not the service, when variants are declared", name))
	}
	switch {
	case svc.Default == "" && len(svc.Variants) > 1:
		errs = append(errs, fmt.Errorf("service %q: default is required when more than one variant is declared (have %s)", name, strings.Join(svc.VariantNames(), ", ")))
	case svc.Default != "":
		if _, ok := svc.Variants[svc.Default]; !ok {
			errs = append(errs, fmt.Errorf("service %q: default %q is not a declared variant (have %s)", name, svc.Default, strings.Join(svc.VariantNames(), ", ")))
		}
	}
	for _, vname := range svc.VariantNames() {
		v := svc.Variants[vname]
		if v.Run == "" && v.Docker == nil {
			errs = append(errs, fmt.Errorf("service %q: variant %q: must set run or docker", name, vname))
		}
		if v.Run != "" && svc.Port == 0 {
			errs = append(errs, fmt.Errorf("service %q: variant %q: run is set but the service's port is 0", name, vname))
		}
		if v.StartupTimeoutS < 0 {
			errs = append(errs, fmt.Errorf("service %q: variant %q: startup_timeout_s must be >= 0", name, vname))
		}
		errs = append(errs, validateDockerArgs(fmt.Sprintf("service %q: variant %q", name, vname), v.Docker)...)
	}
	return errs
}

// validateDockerArgs rejects an empty docker.args entry — a stray "" in
// the YAML list would become an empty argv element that docker reads as
// the image name.
func validateDockerArgs(where string, d *DockerPlacement) []error {
	if d == nil {
		return nil
	}
	var errs []error
	for i, a := range d.Args {
		if strings.TrimSpace(a) == "" {
			errs = append(errs, fmt.Errorf("%s: docker.args[%d] is empty", where, i))
		}
	}
	return errs
}

// hasStub reports whether name identifies a declared stub.
func (c *Config) hasStub(name string) bool {
	_, ok := c.Stubs[name]
	return ok
}

// normalizeGatewayPrefix drops a trailing slash from every prefix but "/",
// mirroring core/proxy's matching so "/cart" and "/cart/" are one rule.
func normalizeGatewayPrefix(p string) string {
	if len(p) > 1 {
		if t := strings.TrimRight(p, "/"); t != "" {
			return t
		}
		return "/"
	}
	return p
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
