package config

import (
	"crypto/tls"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// validateGatewayUpstreams checks gatewayName's declared Upstreams and,
// when they're clean, resolves and caches each one's mTLS client
// certificate (if any) — mirrors validatePassthrough's shape (see
// passthrough.go), minus the Proxy>0 check a GatewayUpstream has no
// equivalent of (a gateway's single Port is validated separately, in
// validate.go's gateway loop).
func (c *Config) validateGatewayUpstreams(gatewayName string, gw Gateway) []error {
	var errs []error
	seen := make(map[string]bool, len(gw.Upstreams))
	for i, gu := range gw.Upstreams {
		if gu.Name == "" {
			errs = append(errs, fmt.Errorf("gateway %q: upstream %d: name is required", gatewayName, i))
		} else if seen[gu.Name] {
			errs = append(errs, fmt.Errorf("gateway %q: upstream %d: duplicate name %q", gatewayName, i, gu.Name))
		}
		seen[gu.Name] = true

		u, err := url.Parse(gu.URL)
		if err != nil || u.Scheme == "" || u.Host == "" {
			errs = append(errs, fmt.Errorf("gateway %q: upstream %q: url %q is not a valid http(s) URL", gatewayName, gu.Name, gu.URL))
		}

		switch {
		case gu.ClientKeyEnv != "" && gu.ClientCertFile == "":
			errs = append(errs, fmt.Errorf("gateway %q: upstream %q: client_key_env is set but client_cert_file is empty", gatewayName, gu.Name))
		case gu.ClientCertFile != "" && gu.ClientKeyEnv == "":
			errs = append(errs, fmt.Errorf("gateway %q: upstream %q: client_cert_file is set but client_key_env is empty", gatewayName, gu.Name))
		case gu.ClientCertFile != "" && gu.ClientKeyEnv != "":
			certPath := gu.ClientCertFile
			if !filepath.IsAbs(certPath) {
				certPath = filepath.Join(c.Dir, certPath)
			}
			certPEM, rerr := os.ReadFile(certPath)
			if rerr != nil {
				errs = append(errs, fmt.Errorf("gateway %q: upstream %q: client_cert_file: read %s: %w", gatewayName, gu.Name, gu.ClientCertFile, rerr))
				break
			}
			keyPEM, ok := c.LookupEnv(gu.ClientKeyEnv)
			if !ok || strings.TrimSpace(keyPEM) == "" {
				errs = append(errs, fmt.Errorf("gateway %q: upstream %q: client_key_env %q is not set", gatewayName, gu.Name, gu.ClientKeyEnv))
				break
			}
			cert, cerr := tls.X509KeyPair(certPEM, []byte(keyPEM))
			if cerr != nil {
				errs = append(errs, fmt.Errorf("gateway %q: upstream %q: client cert/key: %w", gatewayName, gu.Name, cerr))
				break
			}
			if c.gatewayClientCerts == nil {
				c.gatewayClientCerts = map[string]map[string]tls.Certificate{}
			}
			if c.gatewayClientCerts[gatewayName] == nil {
				c.gatewayClientCerts[gatewayName] = map[string]tls.Certificate{}
			}
			c.gatewayClientCerts[gatewayName][gu.Name] = cert
		}
	}
	return errs
}

// GatewayUpstreamClientCert returns the resolved mTLS client certificate
// for gateway's upstream named upstream, or (zero, false) when it has none
// configured. Populated by Validate; see validateGatewayUpstreams.
func (c *Config) GatewayUpstreamClientCert(gateway, upstream string) (tls.Certificate, bool) {
	cert, ok := c.gatewayClientCerts[gateway][upstream]
	return cert, ok
}
