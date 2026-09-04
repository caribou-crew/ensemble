// Package repoconfig parses retrace.repo.yaml — the repo-owned map of
// every app a repo declares, and the root directory each app's own
// retrace.yaml and .retrace/ tree already live in. It exists so a repo
// with apps recorded from more than one subdirectory (web at the repo
// root, several mobile apps under one shared subdirectory, say) can be
// viewed as one standalone `retrace serve` dashboard without depending on
// ensemble's machine-global retrace: block — see
// openspec/changes/retrace-repo-config/design.md.
//
// A retrace.repo.yaml maps app keys straight to root directories, the
// same shape ensemble/config.RetraceConfig.Apps already uses for config
// resolution: two or more app keys naming the same root is the expected,
// common case (several apps already colocated under one .retrace/runs/
// tree), not a special case this package needs to detect.
package repoconfig

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

// FileName is the name Discover and Load look for.
const FileName = "retrace.repo.yaml"

// AppEntry is one entry of Config.Apps.
type AppEntry struct {
	// Root is the directory holding this app's own retrace.yaml and
	// .retrace/ tree, relative to the retrace.repo.yaml file's own
	// location unless absolute. Load resolves this into an absolute path
	// before returning — see Config.Apps.
	Root string `yaml:"root"`
}

// SyncDefaults supplies the repo-wide defaults `retrace serve --watch`
// (and, in principle, a future repo-aware `retrace sync`) use when a CLI
// flag doesn't override them — the same fields
// ensemble/config.RetraceConfig already exposes for its own sync routes,
// kept as a nested block here rather than flattened onto Config so a
// repo.yaml reader can see at a glance which keys are sync-specific.
type SyncDefaults struct {
	Workflows []string `yaml:"workflows"`
	Branch    string   `yaml:"branch"`
	// Branches is a glob allowlist (path.Match, same mechanism Workflows
	// already uses) naming which branches the dashboard's "Choose source"
	// picker offers. Distinct from Branch: Branch is the single exact-match
	// default `--watch` and the plain "pull latest" button use; Branches
	// only governs what the picker shows. Empty means no filter — every
	// branch discovered is offered. See docs/retrace-repo-config.md.
	Branches []string `yaml:"branches"`
	Actor    string   `yaml:"actor"`
	Event    string   `yaml:"event"`
	Status   string   `yaml:"status"`
	// Since bounds how far back a sync looks (e.g. "7d", "24h"), the same
	// string shape retrace/sync.ParseSince accepts. Empty means that
	// package's own default.
	Since string `yaml:"since"`
}

// Config is the parsed contents of retrace.repo.yaml, with every Apps
// entry's Root resolved to an absolute path by Load.
type Config struct {
	// Repo is "org/repo" — the GitHub repository `retrace serve --watch`
	// syncs from. Optional: a repo config that only aggregates local runs
	// (no sync) has no use for it.
	Repo string `yaml:"repo"`
	// Apps maps an app key — the same name recorded under
	// .retrace/runs/<app>/ and .retrace-ref/<app>/ — to the root
	// directory holding that app's own retrace.yaml. Required: Load
	// rejects a file with no entries.
	Apps map[string]AppEntry `yaml:"apps"`
	Sync SyncDefaults        `yaml:"sync"`
	// Dir is the directory containing this retrace.repo.yaml, set by Load
	// from the file's own location. Not a YAML key — see
	// retrace/config.Config.Dir's own doc comment for why this must be
	// tagged yaml:"-".
	Dir string `yaml:"-"`
}

// Load reads and validates one retrace.repo.yaml. Every Apps[*].Root is
// resolved relative to filepath.Dir(path) (an already-absolute Root
// passes through unchanged) and is checked to exist and be a directory —
// failing fast, at load, rather than the first time some other command
// tries to read a root that was never there (design.md's "fail at load,
// not at first use" posture, matching retrace/config.Load).
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true) // a typo'd key is an error, not a silently ignored setting
	if err := dec.Decode(&c); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); err == nil {
		return nil, fmt.Errorf("%s: multiple YAML documents are not supported", path)
	} else if !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	dir := filepath.Dir(path)
	c.Dir = dir

	if len(c.Apps) == 0 {
		return nil, fmt.Errorf("%s: apps: is empty — retrace.repo.yaml must declare at least one app", path)
	}

	for key, entry := range c.Apps {
		root := entry.Root
		if root == "" {
			return nil, fmt.Errorf("%s: apps.%s.root is required", path, key)
		}
		if !filepath.IsAbs(root) {
			root = filepath.Join(dir, root)
		}
		info, err := os.Stat(root)
		if err != nil || !info.IsDir() {
			return nil, fmt.Errorf("%s: apps.%s.root %q does not exist as a directory", path, key, entry.Root)
		}
		c.Apps[key] = AppEntry{Root: root}
	}

	return &c, nil
}

// Roots returns every distinct root directory named in Apps, sorted.
func (c *Config) Roots() []string {
	seen := make(map[string]bool, len(c.Apps))
	out := make([]string, 0, len(c.Apps))
	for _, entry := range c.Apps {
		if seen[entry.Root] {
			continue
		}
		seen[entry.Root] = true
		out = append(out, entry.Root)
	}
	sort.Strings(out)
	return out
}

// RootFor returns the root directory app resolves to, and whether app is
// a key in Apps at all.
func (c *Config) RootFor(app string) (string, bool) {
	entry, ok := c.Apps[app]
	if !ok {
		return "", false
	}
	return entry.Root, true
}

// AppsIn returns every app key mapped to root, sorted — the allowlist a
// per-root retrace/sync.Options.Apps call passes so a sync scoped to one
// root never merges another root's apps (design.md D4).
func (c *Config) AppsIn(root string) []string {
	out := make([]string, 0, len(c.Apps))
	for key, entry := range c.Apps {
		if entry.Root == root {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}
