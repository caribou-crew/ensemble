package config

// DefaultDatadogSite, DefaultDatadogAPIKeyEnv, DefaultDatadogAppKeyEnv, and
// DefaultDatadogWindowMinutes apply when the corresponding DatadogConfig
// field is left unset (zero) — and, for the two *Env fields, when there is
// no datadog: block at all (see Config.DatadogAPIKeyEnv/AppKeyEnv/Site).
const (
	DefaultDatadogSite          = "datadoghq.com"
	DefaultDatadogAPIKeyEnv     = "DD_API_KEY"
	DefaultDatadogAppKeyEnv     = "DD_APP_KEY"
	DefaultDatadogWindowMinutes = 60
)

// DatadogConfig is optional stack-level Datadog configuration — the site,
// which environment variables carry the API/app keys (never the keys
// themselves, see Config.LookupEnv), a default query window, and a
// service-name mapping. Every field is optional; a nil *DatadogConfig on
// Config (no `datadog:` key at all) is the fully zero-config path — see
// Config.DatadogSite/APIKeyEnvName/AppKeyEnvName.
type DatadogConfig struct {
	Site                 string `yaml:"site"`
	APIKeyEnv            string `yaml:"api_key_env"`
	AppKeyEnv            string `yaml:"app_key_env"`
	DefaultWindowMinutes int    `yaml:"default_window_minutes"`
	// ServiceMap maps an ensemble service/stub name to the Datadog service
	// tag it's queried under, for the (common) case where they differ —
	// e.g. ensemble's "statements" is Datadog's "accounts-statements".
	ServiceMap map[string]string `yaml:"service_map"`
}

// DatadogSite returns the configured Datadog site, or DefaultDatadogSite
// when unconfigured (no datadog: block, or datadog.site left blank).
func (c *Config) DatadogSite() string {
	if c.Datadog != nil && c.Datadog.Site != "" {
		return c.Datadog.Site
	}
	return DefaultDatadogSite
}

// DatadogAPIKeyEnvName returns the name of the environment variable that
// should hold the Datadog API key — datadog.api_key_env when configured,
// else DefaultDatadogAPIKeyEnv.
func (c *Config) DatadogAPIKeyEnvName() string {
	if c.Datadog != nil && c.Datadog.APIKeyEnv != "" {
		return c.Datadog.APIKeyEnv
	}
	return DefaultDatadogAPIKeyEnv
}

// DatadogAppKeyEnvName mirrors DatadogAPIKeyEnvName for the application key.
func (c *Config) DatadogAppKeyEnvName() string {
	if c.Datadog != nil && c.Datadog.AppKeyEnv != "" {
		return c.Datadog.AppKeyEnv
	}
	return DefaultDatadogAppKeyEnv
}

// DatadogDefaultWindowMinutes returns datadog.default_window_minutes, or
// DefaultDatadogWindowMinutes when unconfigured.
func (c *Config) DatadogDefaultWindowMinutes() int {
	if c.Datadog != nil && c.Datadog.DefaultWindowMinutes > 0 {
		return c.Datadog.DefaultWindowMinutes
	}
	return DefaultDatadogWindowMinutes
}

// DatadogServiceName maps an ensemble target name onto its Datadog service
// tag via datadog.service_map, or returns target unchanged when no mapping
// is configured for it.
func (c *Config) DatadogServiceName(target string) string {
	if c.Datadog == nil {
		return target
	}
	if mapped, ok := c.Datadog.ServiceMap[target]; ok {
		return mapped
	}
	return target
}
