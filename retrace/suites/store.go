package suites

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const maxJSONBytes = 16 << 20

// readJSONFile rejects links and special files before decoding. Root confines
// every open even if a directory is replaced between validation and the read.
func readJSONFile(root *os.Root, path string) ([]byte, error) {
	info, err := root.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("suites: %s must be a regular file, not a symlink or special file", path)
	}
	f, err := root.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("suites: %s must be a regular file", path)
	}
	b, err := io.ReadAll(io.LimitReader(f, maxJSONBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxJSONBytes {
		return nil, fmt.Errorf("suites: %s exceeds %d bytes", path, maxJSONBytes)
	}
	return b, nil
}
func inventoryAt(root *os.Root) (Inventory, error) {
	b, err := readJSONFile(root, InventoryFile)
	if errors.Is(err, fs.ErrNotExist) {
		return Inventory{Schema: InventorySchema, Suites: []Suite{}}, nil
	}
	if err != nil {
		return Inventory{}, err
	}
	inv, err := DecodeInventory(b)
	if err != nil {
		return inv, fmt.Errorf("suites: %s: %w", InventoryFile, err)
	}
	return inv, nil
}

// directory checks each named storage component rather than following symlinks.
// OpenRoot also enforces confinement during races; links inside the project are
// rejected to keep the authoritative storage location unambiguous.
func directory(root *os.Root, path string, create bool) error {
	current := ""
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		current = filepath.Join(current, part)
		if create {
			if err := root.Mkdir(current, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
				return err
			}
		}
		info, err := root.Lstat(current)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("suites: %s must be a directory, not a symlink", current)
		}
	}
	return nil
}
func readAttempts(root *os.Root, inv Inventory) ([]Attempt, error) {
	path := filepath.Join(".retrace", "suites")
	if err := directory(root, path, false); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return []Attempt{}, nil
		}
		return nil, err
	}
	entries, err := fs.ReadDir(root.FS(), path)
	if err != nil {
		return nil, err
	}
	attempts := []Attempt{}
	for _, entry := range entries {
		suitePath := filepath.Join(path, entry.Name())
		if err := directory(root, suitePath, false); err != nil {
			return nil, err
		}
		files, err := fs.ReadDir(root.FS(), suitePath)
		if err != nil {
			return nil, err
		}
		for _, file := range files {
			name := file.Name()
			// In-flight atomic imports are unpublished, hence not attempts yet.
			if strings.HasPrefix(name, ".import-") && strings.HasSuffix(name, ".tmp") {
				continue
			}
			reportPath := filepath.Join(suitePath, name)
			if !strings.HasSuffix(name, ".json") {
				return nil, fmt.Errorf("suites: unexpected report file %s", reportPath)
			}
			b, err := readJSONFile(root, reportPath)
			if err != nil {
				return nil, err
			}
			a, err := DecodeAttempt(b, inv)
			if err != nil {
				return nil, fmt.Errorf("suites: %s: %w", reportPath, err)
			}
			for _, r := range a.Results {
				for _, s := range r.Screens {
					if s.File != "" {
						return nil, fmt.Errorf("suites: %s: screen %q is still in unpublished source form", reportPath, s.Label)
					}
				}
			}
			if a.SuiteID != entry.Name() || a.AttemptID+".json" != name {
				return nil, fmt.Errorf("suites: report identity does not match storage path %s", reportPath)
			}
			attempts = append(attempts, a)
		}
	}
	return attempts, nil
}

// Load returns the current project inventory and every immutable attempt. No
// inventory means an empty onboarding list only when no report contradicts it.
func Load(cwd string) ([]SuiteOverview, error) {
	root, err := os.OpenRoot(cwd)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	inv, err := inventoryAt(root)
	if err != nil {
		return nil, err
	}
	attempts, err := readAttempts(root, inv)
	if err != nil {
		return nil, err
	}
	return Aggregate(inv, attempts)
}

// Import validates an external runner file and publishes it atomically without
// ever replacing an existing attempt id. The temporary file is flushed before
// a hard link publishes the complete JSON document; Link is atomic and fails
// if the destination already exists, unlike a check followed by Rename.
func Import(cwd, file string) (Attempt, error) {
	var a Attempt
	root, err := os.OpenRoot(cwd)
	if err != nil {
		return a, err
	}
	defer root.Close()
	inv, err := inventoryAt(root)
	if err != nil {
		return a, err
	}
	f, err := os.Open(file)
	if err != nil {
		return a, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return a, err
	}
	if !info.Mode().IsRegular() {
		return a, fmt.Errorf("suites: import source must be a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxJSONBytes+1))
	if err != nil {
		return a, err
	}
	if len(data) > maxJSONBytes {
		return a, fmt.Errorf("suites: import exceeds %d bytes", maxJSONBytes)
	}
	a, err = DecodeAttempt(data, inv)
	if err != nil {
		return a, err
	}
	path := filepath.Join(".retrace", "suites", a.SuiteID)
	if _, err := root.Lstat(filepath.Join(path, a.AttemptID+".json")); err == nil {
		return a, fmt.Errorf("suites: publish immutable attempt %q: already exists", a.AttemptID)
	}
	assets, err := materializeScreens(filepath.Dir(file), &a)
	if err != nil {
		return a, err
	}
	if err := directory(root, path, true); err != nil {
		return a, err
	}
	created, err := writeAssets(root, a.SuiteID, a.AttemptID, assets)
	if err != nil {
		removeAll(root, created)
		return a, err
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return a, err
	}
	temp := filepath.Join(path, ".import-"+hex.EncodeToString(nonce)+".tmp")
	out, err := root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return a, err
	}
	defer root.Remove(temp)
	canonical, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		out.Close()
		return a, err
	}
	if _, err := out.Write(append(canonical, '\n')); err != nil {
		out.Close()
		return a, err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return a, err
	}
	if err := out.Close(); err != nil {
		return a, err
	}
	target := filepath.Join(path, a.AttemptID+".json")
	if err := root.Link(temp, target); err != nil {
		removeAll(root, created)
		return a, fmt.Errorf("suites: publish immutable attempt %q: %w", a.AttemptID, err)
	}
	return a, nil
}
