package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// ComparisonFingerprint returns a deterministic SHA-256 digest of the
// effective, public Config value. Discover has already applied defaults and
// merged machine-owned overlays before this is called, so the digest describes
// the policy a comparison actually used rather than only retrace.yaml's source
// bytes.
//
// Dir and Loaded are discovery metadata, not policy. Holding them at their
// zero values makes moving an otherwise identical checkout leave the digest
// unchanged. The encoded config is used only as hash input and is never
// returned or stored in comparison provenance, so URLs, commands, redact
// fields, and other config values do not leak into the Summary.
func (c *Config) ComparisonFingerprint() (string, error) {
	type publicConfig Config
	copy := publicConfig(*c)
	copy.Dir = ""
	copy.Loaded = false
	b, err := json.Marshal(copy)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}
