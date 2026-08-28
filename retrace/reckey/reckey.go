// Package reckey owns the team key: how it is loaded (env or a gitignored
// keyfile), how a per-recording data key is minted and wrapped under it,
// and the fingerprint that lets a rewrap notice it was handed the wrong
// key. Everything downstream (capture, diff, replay, serve, `retrace
// rekey`) goes through this package rather than re-deriving any of it.
package reckey

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/caribou-crew/ensemble/core/trace"
)

// EnvTeamKey is the environment variable LoadTeamKey checks first. This is
// also the exact name a CI job wires a GitHub Actions secret into — no
// retrace-side config beyond this variable already being the one the tool
// looks for.
const EnvTeamKey = "RETRACE_RECORDING_KEY"

// KeyFile is the gitignored keyfile LoadTeamKey falls back to, relative to
// the directory holding retrace.yaml. `**/.retrace/*` already ignores it —
// no .gitignore change is needed for a project that creates one.
const KeyFile = ".retrace/recording.key"

// KeySize is the team key's and every data key's length: AES-256.
const KeySize = 32

// ErrNoTeamKey is returned by LoadTeamKey when neither the env var nor the
// keyfile resolves. Callers that only need the key for an OPTIONAL decrypt
// (diff, serve, review) treat this as "show markers, not values"; callers
// where the key is REQUIRED (capture with an encrypt-mode field configured,
// `retrace rekey`) surface it as a hard failure.
var ErrNoTeamKey = fmt.Errorf("reckey: no team key — set %s or create %s (see `retrace rekey --init`)", EnvTeamKey, KeyFile)

// LoadTeamKey resolves the team key: RETRACE_RECORDING_KEY (hex or base64)
// first, then <dir>/.retrace/recording.key (raw bytes), else ErrNoTeamKey.
// dir is the directory holding retrace.yaml, matching every other
// project-relative lookup in this tree.
func LoadTeamKey(dir string) (key []byte, source string, err error) {
	if v := os.Getenv(EnvTeamKey); v != "" {
		k, derr := decodeKey(v)
		if derr != nil {
			return nil, "", fmt.Errorf("reckey: %s: %w", EnvTeamKey, derr)
		}
		return k, EnvTeamKey, nil
	}
	path := filepath.Join(dir, KeyFile)
	b, rerr := os.ReadFile(path)
	if rerr == nil {
		if len(b) != KeySize {
			return nil, "", fmt.Errorf("reckey: %s must be %d raw bytes, got %d", path, KeySize, len(b))
		}
		return b, path, nil
	}
	if !errors.Is(rerr, os.ErrNotExist) {
		return nil, "", fmt.Errorf("reckey: reading %s: %w", path, rerr)
	}
	return nil, "", ErrNoTeamKey
}

// ParseKey decodes a key given directly on a command line (`retrace rekey
// --old`/`--new`) — the same hex-or-base64 sniffing LoadTeamKey applies to
// RETRACE_RECORDING_KEY, exposed here because a rotation takes both keys as
// flags rather than reading either from the environment.
func ParseKey(v string) ([]byte, error) { return decodeKey(v) }

// decodeKey sniffs hex vs base64 by decoded length, per D5: 32 raw bytes is
// 64 hex characters or 44 (or 43, unpadded) base64 characters. It never
// echoes the input back in an error — a malformed key is still a secret.
func decodeKey(v string) ([]byte, error) {
	if b, err := hex.DecodeString(v); err == nil && len(b) == KeySize {
		return b, nil
	}
	if b, err := base64.StdEncoding.DecodeString(v); err == nil && len(b) == KeySize {
		return b, nil
	}
	if b, err := base64.RawStdEncoding.DecodeString(v); err == nil && len(b) == KeySize {
		return b, nil
	}
	return nil, fmt.Errorf("must decode to %d raw bytes as hex or base64", KeySize)
}

// GenerateDataKey mints a fresh 32-byte AES-256 key for one recording.
func GenerateDataKey() ([]byte, error) {
	k := make([]byte, KeySize)
	if _, err := rand.Read(k); err != nil {
		return nil, fmt.Errorf("reckey: generate data key: %w", err)
	}
	return k, nil
}

// WrapDataKey seals a data key under the team key, base64-encoded. It
// reuses trace.EncryptField directly — a data key is just 32 bytes of
// "plaintext" to the same AEAD, and this package must never grow a second
// implementation of the marker format or the nonce handling.
func WrapDataKey(dataKey, teamKey []byte) (string, error) {
	wrapped, err := trace.EncryptField(teamKey, string(dataKey))
	if err != nil {
		return "", fmt.Errorf("reckey: wrap data key: %w", err)
	}
	return wrapped, nil
}

// UnwrapDataKey reverses WrapDataKey.
func UnwrapDataKey(wrapped string, teamKey []byte) ([]byte, error) {
	plain, err := trace.DecryptField(teamKey, wrapped)
	if err != nil {
		return nil, fmt.Errorf("reckey: unwrap data key: %w", err)
	}
	return []byte(plain), nil
}

// KeyID fingerprints a team key: enough to notice "this run was wrapped by
// a different team key than the one I have" and fail with a clear message,
// not enough to leak anything about the key itself.
func KeyID(teamKey []byte) string {
	sum := sha256.Sum256(teamKey)
	return hex.EncodeToString(sum[:])[:8]
}
