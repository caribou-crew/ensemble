package trace

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
)

// EncryptedPrefix marks a field-level AES-256-GCM ciphertext produced by
// EncryptField. Versioned ("v1") so a future AEAD change can land without
// invalidating a marker already committed inside a reference bundle.
const EncryptedPrefix = "$enc:v1:"

// EncryptField seals plaintext with AES-256-GCM under dataKey (must be 32
// bytes) and returns "$enc:v1:<base64(nonce||ciphertext)>". A fresh random
// nonce is drawn per call: GCM's confidentiality guarantee depends on never
// reusing a nonce under the same key, and one run's data key seals every
// encrypt-mode field captured during it.
func EncryptField(dataKey []byte, plaintext string) (string, error) {
	gcm, err := newGCM(dataKey)
	if err != nil {
		return "", fmt.Errorf("trace: encrypt field: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("trace: encrypt field: %w", err)
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return EncryptedPrefix + base64.StdEncoding.EncodeToString(sealed), nil
}

// DecryptField reverses EncryptField. It returns an error rather than
// panicking on a wrong data key or a malformed marker — both are reachable
// at runtime (a rotated team key, a hand-edited bundle), not programmer
// bugs.
func DecryptField(dataKey []byte, marker string) (string, error) {
	if !IsEncrypted(marker) {
		return "", errors.New("trace: decrypt field: not an encrypted marker")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(marker, EncryptedPrefix))
	if err != nil {
		return "", fmt.Errorf("trace: decrypt field: %w", err)
	}
	gcm, err := newGCM(dataKey)
	if err != nil {
		return "", fmt.Errorf("trace: decrypt field: %w", err)
	}
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("trace: decrypt field: ciphertext too short")
	}
	nonce, ciphertext := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("trace: decrypt field: %w", err)
	}
	return string(plaintext), nil
}

// IsEncrypted reports whether s is an EncryptField marker — a prefix check
// only, so it also answers false for a plain value and for a [redacted]
// destroy marker.
func IsEncrypted(s string) bool {
	return strings.HasPrefix(s, EncryptedPrefix)
}

func newGCM(dataKey []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(dataKey)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
