package config

import (
	"crypto/tls"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// validatePassthrough checks one service's Upstream/Passthrough/mTLS fields
// and, when they're clean, resolves the client certificate (if any) and
// caches it on c — mirrors validateReadiness caching readinessChecks: a bad
// cert/key fails ensemble up at load time, not at first proxy dial.
func (c *Config) validatePassthrough(name string, svc Service) []error {
	var errs []error

	if svc.Passthrough != "" && svc.Upstream == "" {
		errs = append(errs, fmt.Errorf("service %q: passthrough is set but upstream is empty", name))
	}
	if svc.Upstream == "" {
		if svc.ClientCertFile != "" || svc.ClientKeyEnv != "" {
			errs = append(errs, fmt.Errorf("service %q: client_cert_file/client_key_env require upstream to be set", name))
		}
		return errs
	}

	u, err := url.Parse(svc.Upstream)
	if err != nil || u.Scheme == "" || u.Host == "" {
		errs = append(errs, fmt.Errorf("service %q: upstream %q is not a valid http(s) URL", name, svc.Upstream))
	}
	if svc.Proxy <= 0 {
		errs = append(errs, fmt.Errorf("service %q: upstream is set but proxy is 0 (a passthrough target needs a listen port for clients to call)", name))
	}

	switch {
	case svc.ClientKeyEnv != "" && svc.ClientCertFile == "":
		errs = append(errs, fmt.Errorf("service %q: client_key_env is set but client_cert_file is empty", name))
	case svc.ClientCertFile != "" && svc.ClientKeyEnv == "":
		errs = append(errs, fmt.Errorf("service %q: client_cert_file is set but client_key_env is empty", name))
	case svc.ClientCertFile != "" && svc.ClientKeyEnv != "":
		certPath := svc.ClientCertFile
		if !filepath.IsAbs(certPath) {
			certPath = filepath.Join(c.Dir, certPath)
		}
		certPEM, rerr := os.ReadFile(certPath)
		if rerr != nil {
			errs = append(errs, fmt.Errorf("service %q: client_cert_file: read %s: %w", name, svc.ClientCertFile, rerr))
			break
		}
		keyPEM, ok := c.LookupEnv(svc.ClientKeyEnv)
		if !ok || strings.TrimSpace(keyPEM) == "" {
			errs = append(errs, fmt.Errorf("service %q: client_key_env %q is not set", name, svc.ClientKeyEnv))
			break
		}
		cert, cerr := tls.X509KeyPair(certPEM, []byte(keyPEM))
		if cerr != nil {
			errs = append(errs, fmt.Errorf("service %q: client cert/key: %w", name, cerr))
			break
		}
		if c.clientCerts == nil {
			c.clientCerts = map[string]tls.Certificate{}
		}
		c.clientCerts[name] = cert
	}

	return errs
}

// ClientCert returns the resolved mTLS client certificate for a passthrough
// service, or (zero, false) when it has none configured. Populated by
// Validate; see validatePassthrough.
func (c *Config) ClientCert(service string) (tls.Certificate, bool) {
	cert, ok := c.clientCerts[service]
	return cert, ok
}
