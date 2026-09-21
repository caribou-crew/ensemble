package suites

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/caribou-crew/ensemble/retrace/runs"
)

const (
	maxScreensPerResult = 12
	maxScreenBytes      = 8 << 20
	maxScreenLabel      = 80
	maxWireNote         = 400
)

// Only raster formats are accepted. SVG and HTML can carry script and are
// served from the same origin as the dashboard, so they are never stored.
var screenExt = map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/webp": ".webp"}
var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// sniffMedia trusts the bytes, never a file name.
func sniffMedia(b []byte) (string, bool) {
	switch {
	case len(b) >= 8 && string(b[:8]) == "\x89PNG\r\n\x1a\n":
		return "image/png", true
	case len(b) >= 3 && b[0] == 0xff && b[1] == 0xd8 && b[2] == 0xff:
		return "image/jpeg", true
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return "image/webp", true
	}
	return "", false
}

// validateScreens accepts each screen in exactly one of two forms: the source
// form (File only, as written by a runner) or the stored form (hash, media and
// size, as published by import). Anything else is malformed, so a screen can
// neither smuggle a path into a stored report nor claim bytes it never had.
func validateScreens(flowID string, screens []Screen) error {
	if len(screens) > maxScreensPerResult {
		return fmt.Errorf("suites: result %q has %d screens; at most %d", flowID, len(screens), maxScreensPerResult)
	}
	labels := map[string]bool{}
	for _, s := range screens {
		label := strings.TrimSpace(s.Label)
		if label == "" || len(label) > maxScreenLabel || label != s.Label {
			return fmt.Errorf("suites: result %q screen label must be 1-%d characters without surrounding space", flowID, maxScreenLabel)
		}
		if labels[label] {
			return fmt.Errorf("suites: result %q has duplicate screen label %q", flowID, label)
		}
		labels[label] = true
		source, stored := s.File != "", s.SHA256 != "" || s.Media != "" || s.Bytes != 0
		if source == stored {
			return fmt.Errorf("suites: result %q screen %q must give either file (report) or sha256/media/bytes (stored), not both or neither", flowID, label)
		}
		if source {
			if !filepath.IsLocal(s.File) || filepath.IsAbs(s.File) {
				return fmt.Errorf("suites: result %q screen %q file must be a relative path inside the report directory", flowID, label)
			}
			continue
		}
		if !sha256Hex.MatchString(s.SHA256) || screenExt[s.Media] == "" || s.Bytes <= 0 || s.Bytes > maxScreenBytes {
			return fmt.Errorf("suites: result %q screen %q has an invalid stored hash, media type or size", flowID, label)
		}
	}
	return nil
}

func assetDir(suiteID, attemptID string) string {
	return filepath.Join(".retrace", "suite-assets", suiteID, attemptID)
}

type pendingAsset struct {
	sha, ext string
	data     []byte
}

// materializeScreens reads every source-form screen, verifies the bytes really
// are an accepted image, and rewrites the attempt in stored form. Nothing is
// written here: a report that fails any check publishes no assets.
func materializeScreens(reportDir string, a *Attempt) ([]pendingAsset, error) {
	var out []pendingAsset
	var src *os.Root
	defer func() {
		if src != nil {
			src.Close()
		}
	}()
	for i := range a.Results {
		for j := range a.Results[i].Screens {
			s := &a.Results[i].Screens[j]
			if s.File == "" {
				return nil, fmt.Errorf("suites: import requires screens to name a file; got a stored-form screen for %q", a.Results[i].FlowID)
			}
			if src == nil {
				r, err := os.OpenRoot(reportDir)
				if err != nil {
					return nil, err
				}
				src = r
			}
			data, err := readScreenFile(src, s.File)
			if err != nil {
				return nil, fmt.Errorf("suites: result %q screen %q: %w", a.Results[i].FlowID, s.Label, err)
			}
			media, ok := sniffMedia(data)
			if !ok {
				return nil, fmt.Errorf("suites: result %q screen %q is not a PNG, JPEG or WebP image", a.Results[i].FlowID, s.Label)
			}
			h := sha256.Sum256(data)
			hexSum := hex.EncodeToString(h[:])
			*s = Screen{Label: s.Label, SHA256: hexSum, Media: media, Bytes: len(data)}
			out = append(out, pendingAsset{sha: hexSum, ext: screenExt[media], data: data})
		}
	}
	return out, nil
}

func readScreenFile(root *os.Root, name string) ([]byte, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s must be a regular file, not a symlink or special file", name)
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxScreenBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxScreenBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes", name, maxScreenBytes)
	}
	return data, nil
}

// writeAssets stores content-addressed files under the attempt. It returns the
// paths it created so a failed publish can remove exactly those.
func writeAssets(root *os.Root, suiteID, attemptID string, assets []pendingAsset) ([]string, error) {
	created := []string{}
	if len(assets) == 0 {
		return created, nil
	}
	dir := assetDir(suiteID, attemptID)
	if err := directory(root, dir, true); err != nil {
		return created, err
	}
	for _, a := range assets {
		path := filepath.Join(dir, a.sha+a.ext)
		if existing, err := readScreenFile(root, path); err == nil {
			if string(existing) != string(a.data) {
				return created, fmt.Errorf("suites: asset %s exists with different content", path)
			}
			continue
		} else if !errors.Is(err, fs.ErrNotExist) {
			return created, err
		}
		f, err := root.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return created, err
		}
		created = append(created, path)
		if _, err := f.Write(a.data); err != nil {
			f.Close()
			return created, err
		}
		if err := f.Sync(); err != nil {
			f.Close()
			return created, err
		}
		if err := f.Close(); err != nil {
			return created, err
		}
	}
	return created, nil
}

func removeAll(root *os.Root, paths []string) {
	for _, p := range paths {
		root.Remove(p)
	}
}

// OpenScreen returns one stored image. It serves only a hash that a valid stored
// report references, re-verifies the bytes against that hash, and fails closed on
// any mismatch, so the asset store cannot be used to read arbitrary files.
func OpenScreen(cwd, suiteID, attemptID, sha string) ([]byte, string, error) {
	if err := runs.ValidateComponents(suiteID, attemptID); err != nil {
		return nil, "", fmt.Errorf("suites: %w", err)
	}
	if !sha256Hex.MatchString(sha) {
		return nil, "", fmt.Errorf("suites: screen id must be a sha256 hex digest")
	}
	root, err := os.OpenRoot(cwd)
	if err != nil {
		return nil, "", err
	}
	defer root.Close()
	inv, err := inventoryAt(root)
	if err != nil {
		return nil, "", err
	}
	reportPath := filepath.Join(".retrace", "suites", suiteID, attemptID+".json")
	b, err := readJSONFile(root, reportPath)
	if err != nil {
		return nil, "", err
	}
	a, err := DecodeAttempt(b, inv)
	if err != nil {
		return nil, "", err
	}
	media := ""
	for _, r := range a.Results {
		for _, s := range r.Screens {
			if s.SHA256 == sha && s.File == "" {
				media = s.Media
			}
		}
	}
	if media == "" {
		return nil, "", fmt.Errorf("suites: attempt %s/%s references no screen %s", suiteID, attemptID, sha)
	}
	data, err := readScreenFile(root, filepath.Join(assetDir(suiteID, attemptID), sha+screenExt[media]))
	if err != nil {
		return nil, "", err
	}
	if h := sha256.Sum256(data); hex.EncodeToString(h[:]) != sha {
		return nil, "", fmt.Errorf("suites: stored screen %s no longer matches its hash", sha)
	}
	return data, media, nil
}
