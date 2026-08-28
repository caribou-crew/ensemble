package runs

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// EncryptionFile is the sidecar written beside manifest.json for a run that
// has at least one encrypt-mode redacted field. Its ABSENCE means "nothing
// in this run is encrypted" — the same absence-is-the-local-case reading
// source.json established, here read as "nothing to decrypt" rather than
// "recorded locally".
const EncryptionFile = "encryption.json"

// EncryptionSchema versions this sidecar independently of the manifest, for
// the same reason SourceSchema does: a reader of encryption.json has no
// business depending on the manifest schema having been reached.
const EncryptionSchema = "retrace/encryption/1"

// Encryption records how to recover this run's encrypted fields: the
// per-run data key, wrapped under a team key, plus a fingerprint of which
// team key did the wrapping.
type Encryption struct {
	Schema string `json:"schema"`
	// KeyID fingerprints the team key that wrapped WrappedDataKey — see
	// reckey.KeyID. Lets a reader notice "this run was wrapped by a
	// different team key than the one I have" before attempting an unwrap
	// that would only fail with a less specific error.
	KeyID string `json:"keyId"`
	// WrappedDataKey is this run's data key, sealed under the team key
	// identified by KeyID — trace.EncryptField's own marker shape
	// (base64 nonce||ciphertext), via reckey.WrapDataKey.
	WrappedDataKey string `json:"wrappedDataKey"`
}

func (p Paths) encryptionPath() string { return filepath.Join(p.RunDir, EncryptionFile) }

// EncryptionPath exposes the sidecar location for callers outside this
// package (retrace/refs' bundle copy), matching SourcePath's own
// single-construction-seam rule.
func (p Paths) EncryptionPath() string { return p.encryptionPath() }

// WriteEncryption stamps Schema and writes the sidecar, atomically (via the
// same temp-file-and-rename writeJSONFile every other sentinel in this
// package uses) so a reader never sees a half-written encryption.json.
func WriteEncryption(p Paths, e Encryption) error {
	if e.KeyID == "" || e.WrappedDataKey == "" {
		return fmt.Errorf("runs: encryption record needs a keyId and a wrappedDataKey — an empty one is indistinguishable from a bug that forgot to set it")
	}
	e.Schema = EncryptionSchema
	return writeJSONFile(p.encryptionPath(), e)
}

// ReadEncryption loads the sidecar. A missing file is (nil, nil): "nothing
// encrypted in this run" is the ordinary case for every run with no
// encrypt-mode field configured, not an error — mirroring ReadSource's own
// contract for source.json.
func ReadEncryption(p Paths) (*Encryption, error) {
	b, err := os.ReadFile(p.encryptionPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var e Encryption
	if err := json.Unmarshal(b, &e); err != nil {
		return nil, fmt.Errorf("runs: %s: %w", EncryptionFile, err)
	}
	if e.Schema != EncryptionSchema {
		return nil, fmt.Errorf("runs: %s schema %q, want %q", EncryptionFile, e.Schema, EncryptionSchema)
	}
	return &e, nil
}
