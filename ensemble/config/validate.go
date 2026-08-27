package config

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// retraceSincePattern matches a bare integer plus one unit — "7d", "24h",
// "30m" — the same shape retrace/sync's own parser accepts. Days aren't a
// valid time.ParseDuration unit, which is exactly why this is a distinct,
// looser check rather than a call into time.ParseDuration.
var retraceSincePattern = regexp.MustCompile(`^[0-9]+[dhms]$`)

// validDatabaseTypes are the emulators ensemble knows how to provision.
var validDatabaseTypes = map[string]bool{
	"postgres":   true,
	"mysql":      true,
	"redis":      true,
	"dynamodb":   true,
	"localstack": true,
	"http":       true,
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

	for i, check := range c.Preflight {
		if check.Run == "" {
			errs = append(errs, fmt.Errorf("preflight[%d]: run is required", i))
		}
		if check.TimeoutS < 0 {
			errs = append(errs, fmt.Errorf("preflight[%d]: timeout_s must be >= 0", i))
		}
	}

	if c.Freshness != nil && c.Freshness.PollIntervalS < 0 {
		errs = append(errs, fmt.Errorf("freshness: poll_interval_s must be >= 0"))
	}

	if c.Retrace != nil && c.Retrace.Since != "" && !retraceSincePattern.MatchString(c.Retrace.Since) {
		errs = append(errs, fmt.Errorf("retrace: since %q is not a duration like \"7d\", \"24h\", or \"30m\"", c.Retrace.Since))
	}

	for name, db := range c.Databases {
		if db.Type == "" {
			db.Type = inferDatabaseType(db.Image)
		}
		if !validDatabaseTypes[db.Type] {
			errs = append(errs, fmt.Errorf("database %q: invalid type %q (image %q)", name, db.Type, db.Image))
		}
		if db.Type == "http" && db.URL == "" {
			errs = append(errs, fmt.Errorf("database %q: url is required when type is http", name))
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
		for _, caller := range svc.CalledBy {
			if _, ok := c.Services[caller]; !ok {
				if _, ok := c.Gateways[caller]; !ok {
					errs = append(errs, fmt.Errorf("service %q: called_by references unknown service/gateway %q", name, caller))
				}
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
				if route.Rewrite != "" && route.StripPrefix {
					errs = append(errs, fmt.Errorf("gateway %q: route %d: rewrite and strip_prefix are mutually exclusive", name, i))
				}
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
		if gw.CORS != nil {
			if len(gw.CORS.AllowOrigins) == 0 {
				errs = append(errs, fmt.Errorf("gateway %q: cors: allow_origins is empty", name))
			}
			if gw.CORS.AllowCredentials && slices.Contains(gw.CORS.AllowOrigins, "*") {
				errs = append(errs, fmt.Errorf("gateway %q: cors: allow_origins must not include \"*\" when allow_credentials is true", name))
			}
			if gw.CORS.MaxAgeSeconds < 0 {
				errs = append(errs, fmt.Errorf("gateway %q: cors: max_age_seconds must be >= 0", name))
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
		seenLabels := make(map[string]bool, len(ent.Links))
		for i, link := range ent.Links {
			if link.Label == "" || link.Template == "" {
				errs = append(errs, fmt.Errorf("entity %q: link %d: label and template must both be non-empty", name, i))
			}
			if link.Label != "" {
				// EntityView.tsx keys each link's button by its label, so a
				// duplicate silently collides in React rather than erroring
				// there — catch it here instead, where the message can name
				// both the entity and the offending label.
				if seenLabels[link.Label] {
					errs = append(errs, fmt.Errorf("entity %q: link %d: duplicate label %q (link labels must be unique within an entity)", name, i, link.Label))
				}
				seenLabels[link.Label] = true
			}
			errs = append(errs, c.validateEntityLinkKind(name, i, link)...)
		}
	}

	for _, name := range c.OnReady.Seeds {
		if _, ok := c.Seeds[name]; !ok {
			errs = append(errs, fmt.Errorf("on_ready: seeds references unknown seed %q", name))
		}
	}

	errs = append(errs, c.validateReadiness()...)
	errs = append(errs, c.validateLatencyProfiles()...)

	return errors.Join(errs...)
}

// validEntityLinkKinds are the only values EntityLink.Kind may take. Empty
// is equivalent to "url" — the only behavior that existed before Kind did.
var validEntityLinkKinds = map[string]bool{"": true, "url": true, "exec": true}

// execLinkSchemeRe matches a literal URI scheme (RFC 3986 §3.1) at the
// start of a string, e.g. "myapp:" out of "myapp://widget/". Used to check
// the text before an "exec" link's first {{ placeholder — see
// validateEntityLinkKind.
var execLinkSchemeRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.\-]*:`)

// templatePlaceholderRe matches one {{column}} placeholder, for stripping
// them out to inspect a link template's config-authored literal text.
var templatePlaceholderRe = regexp.MustCompile(`\{\{\w+\}\}`)

// hasControlByte reports whether s contains an ASCII control character
// (byte < 0x20, or 0x7F/DEL).
func hasControlByte(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] == 0x7F {
			return true
		}
	}
	return false
}

// validateEntityLinkKind checks the Kind/Exec-related rules for one entity
// link. Split out from the main Validate loop because "exec" links carry
// several rules of their own — see EntityLink's doc comment and
// execcommands.go for why these exist. A method on *Config (rather than a
// free function) because Reverse names are resolved via c.RoutablePort,
// the same resolution a gateway route or readiness check uses.
func (c *Config) validateEntityLinkKind(entityName string, i int, link EntityLink) []error {
	var errs []error

	if !validEntityLinkKinds[link.Kind] {
		errs = append(errs, fmt.Errorf("entity %q: link %d: kind %q is not valid (must be \"url\" or \"exec\")", entityName, i, link.Kind))
		return errs
	}

	if link.Kind != "exec" {
		if link.Exec != "" {
			errs = append(errs, fmt.Errorf("entity %q: link %d: exec is set but kind is %q (exec: only applies to kind: exec links)", entityName, i, link.Kind))
		}
		if len(link.Reverse) > 0 {
			errs = append(errs, fmt.Errorf("entity %q: link %d: reverse is set but kind is %q (reverse: only applies to kind: exec links)", entityName, i, link.Kind))
		}
		return errs
	}

	var cmd ExecCommand
	var cmdOK bool
	if link.Exec == "" {
		errs = append(errs, fmt.Errorf("entity %q: link %d: kind: exec requires exec: naming one of %s", entityName, i, strings.Join(ExecCommandNames(), ", ")))
	} else if cmd, cmdOK = LookupExecCommand(link.Exec); !cmdOK {
		errs = append(errs, fmt.Errorf("entity %q: link %d: exec %q is not a known command (must be one of %s)", entityName, i, link.Exec, strings.Join(ExecCommandNames(), ", ")))
	}

	if len(link.Reverse) > 0 && cmdOK && !cmd.ReversePorts {
		errs = append(errs, fmt.Errorf("entity %q: link %d: reverse is set but exec %q does not support it", entityName, i, link.Exec))
	}
	for _, target := range link.Reverse {
		if _, ok := c.ReversePort(target); !ok {
			errs = append(errs, fmt.Errorf("entity %q: link %d: reverse references unknown/unroutable service/stub/gateway %q", entityName, i, target))
		}
	}

	if link.Template != "" {
		scheme := link.Template
		if idx := strings.Index(link.Template, "{{"); idx >= 0 {
			scheme = link.Template[:idx]
		}
		if !execLinkSchemeRe.MatchString(scheme) {
			errs = append(errs, fmt.Errorf("entity %q: link %d: kind: exec requires a literal scheme before the first {{ (e.g. \"myapp://{{id}}\"), got template %q", entityName, i, link.Template))
		}
		if literal := templatePlaceholderRe.ReplaceAllString(link.Template, ""); hasControlByte(literal) {
			errs = append(errs, fmt.Errorf("entity %q: link %d: template contains a control character", entityName, i))
		}
	}

	return errs
}

// validateLatencyProfiles checks that every latency.profiles entry's file
// exists and parses, and that every rule in it declares exactly one source
// (from_datadog or fixed_ms) and a target that resolves the same way a
// gateway route's service would ("*" also allowed, matching
// proxy.LatencyRule's own wildcard target). The parsed profile files are
// cached on c (see Config.LatencyProfile) so `ensemble latency apply`
// doesn't re-read/re-parse them at runtime.
func (c *Config) validateLatencyProfiles() []error {
	if len(c.Latency.Profiles) == 0 {
		return nil
	}
	var errs []error
	profiles := make(map[string]*LatencyProfileFile, len(c.Latency.Profiles))

	for name, p := range c.Latency.Profiles {
		if p.File == "" {
			errs = append(errs, fmt.Errorf("latency profile %q: file is required", name))
			continue
		}
		f, err := LoadLatencyProfile(c.Dir, p)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for i, rule := range f.Rules {
			if !rule.HasExactlyOneSource() {
				errs = append(errs, fmt.Errorf("latency profile %q: rule %d (%s %s): exactly one of from_datadog or fixed_ms is required", name, i, rule.Target, rule.Path))
			}
			if rule.FromDatadog != nil && rule.FromDatadog.Query == "" {
				errs = append(errs, fmt.Errorf("latency profile %q: rule %d (%s %s): from_datadog.query is required", name, i, rule.Target, rule.Path))
			}
			if rule.Target != "*" {
				if _, _, ok := c.RoutablePort(rule.Target); !ok {
					errs = append(errs, fmt.Errorf("latency profile %q: rule %d: references unknown service/stub %q", name, i, rule.Target))
				}
			}
		}
		profiles[name] = f
	}

	if len(errs) == 0 {
		c.latencyProfiles = profiles
	}
	return errs
}

// validateReadiness checks readiness.file exists and parses, that its
// timeout/retry fields aren't negative, and that every check's service
// resolves the same way a gateway route's service would — a bad reference
// here should fail at config-load time, not at first check execution.
// The parsed checks file is cached on c (see Config.ReadinessChecks) so
// the orchestrator doesn't re-read/re-parse it at runtime.
func (c *Config) validateReadiness() []error {
	if c.Readiness == nil {
		return nil
	}
	var errs []error
	if c.Readiness.TimeoutS < 0 {
		errs = append(errs, fmt.Errorf("readiness: timeout_s must be >= 0"))
	}
	if c.Readiness.RetryIntervalS < 0 {
		errs = append(errs, fmt.Errorf("readiness: retry_interval_s must be >= 0"))
	}
	if c.Readiness.File == "" {
		errs = append(errs, fmt.Errorf("readiness: file is required"))
		return errs
	}

	checks, err := LoadReadinessChecks(c.Dir, *c.Readiness)
	if err != nil {
		errs = append(errs, err)
		return errs
	}

	seen := make(map[string]bool, len(checks.Checks))
	for i, chk := range checks.Checks {
		switch {
		case chk.Name == "":
			errs = append(errs, fmt.Errorf("readiness: check %d: name is required", i))
		case seen[chk.Name]:
			errs = append(errs, fmt.Errorf("readiness: duplicate check name %q", chk.Name))
		}
		seen[chk.Name] = true
		if _, _, ok := c.RoutablePort(chk.Service); !ok {
			errs = append(errs, fmt.Errorf("readiness: check %q: references unknown service/stub %q", chk.Name, chk.Service))
		}
	}
	if len(errs) == 0 {
		c.readinessChecks = checks
	}
	return errs
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
