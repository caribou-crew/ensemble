package reckey

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// InitKeyFile generates a fresh team key and writes it to dir's gitignored
// KeyFile, for a project's first setup. It refuses when a key already
// resolves — from RETRACE_RECORDING_KEY or an existing keyfile — because
// overwriting either would orphan every data key already wrapped under the
// key that goes away; that is what `retrace rekey --old --new` exists for,
// not this flag.
func InitKeyFile(dir string) (path string, err error) {
	if _, _, err := LoadTeamKey(dir); err == nil {
		return "", fmt.Errorf("reckey: a team key already resolves for %s — --init never overwrites one; use `retrace rekey --old <existing> --new <fresh>` to rotate instead", dir)
	} else if !errors.Is(err, ErrNoTeamKey) {
		return "", err
	}

	key, err := GenerateDataKey()
	if err != nil {
		return "", err
	}
	path = filepath.Join(dir, KeyFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".recording.key.*")
	if err != nil {
		return "", err
	}
	name := tmp.Name()
	if _, err := tmp.Write(key); err != nil {
		tmp.Close()
		os.Remove(name)
		return "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return "", err
	}
	// 0600, not writeJSONFile's 0644: this file IS the secret, not a record
	// naming one.
	if err := os.Chmod(name, 0o600); err != nil {
		os.Remove(name)
		return "", err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return "", err
	}
	return path, nil
}
