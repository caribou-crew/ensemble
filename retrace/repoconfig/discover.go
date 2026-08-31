package repoconfig

import (
	"os"
	"path/filepath"
)

// Discover searches startDir, then each parent directory in turn, for
// retrace.repo.yaml — the same "walk up like .git" search a developer
// already expects from other repo-root-finding tools, so `retrace serve`
// run from any app's own subdirectory still finds the repo-wide config.
//
// The search stops at the first retrace.repo.yaml found, or at a
// directory that itself contains .git (inclusive — a repo.yaml sitting
// beside .git is still found), or at the filesystem root, whichever
// comes first. A directory with no retrace.repo.yaml anywhere above it is
// NOT an error: it returns (nil, "", nil), the same "absent means no
// repo-scoped config, behave as before this file existed" contract
// retrace/config.Discover already uses for a missing retrace.yaml.
func Discover(startDir string) (*Config, string, error) {
	dir, err := filepath.Abs(startDir)
	if err != nil {
		return nil, "", err
	}
	for {
		candidate := filepath.Join(dir, FileName)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			cfg, err := Load(candidate)
			if err != nil {
				return nil, "", err
			}
			return cfg, dir, nil
		}

		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return nil, "", nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, "", nil
		}
		dir = parent
	}
}
