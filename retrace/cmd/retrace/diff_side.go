package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/caribou-crew/ensemble/retrace/config"
	"github.com/caribou-crew/ensemble/retrace/diff"
)

type comparisonSideRequest struct {
	Label            string
	Selector         string
	Root             string
	RootSet          bool
	App              string
	AppSet           bool
	Commit           string
	CommitSet        bool
	SearchRoots      []string
	LegacyDefaultApp string
	GlobalApp        string
	Flow             string
}

// explicitStringFlag preserves the distinction between an absent optional
// flag and a flag the caller supplied with an empty value. A plain *string
// erases that distinction and silently turns --a-root= into "use cwd".
type explicitStringFlag struct {
	value string
	set   bool
}

func (f *explicitStringFlag) String() string { return f.value }
func (f *explicitStringFlag) Set(value string) error {
	f.value = value
	f.set = true
	return nil
}

type resolvedComparisonSide struct {
	Ref         diff.RunRef
	Root        string
	Selector    string
	App         string
	SearchRoots []string
}

// resolveComparisonSide applies one side's explicit inputs without changing
// legacy resolution for the other. An explicit root is a singleton search;
// an absent one uses the existing repeated --root search and its ambiguity
// refusal unchanged.
func resolveComparisonSide(req comparisonSideRequest) (resolvedComparisonSide, error) {
	for _, input := range []struct {
		name  string
		value string
		set   bool
	}{
		{"root", req.Root, req.RootSet},
		{"app", req.App, req.AppSet},
		{"commit", req.Commit, req.CommitSet},
	} {
		if input.set && strings.TrimSpace(input.value) == "" {
			return resolvedComparisonSide{}, fmt.Errorf("--%s-%s was explicitly empty", req.Label, input.name)
		}
	}
	if req.CommitSet && !isFullGitSHA(req.Commit) {
		return resolvedComparisonSide{}, fmt.Errorf("--%s-commit must be a full 40 or 64 hexadecimal character git SHA, got %q", req.Label, req.Commit)
	}

	roots := req.SearchRoots
	defaultApp := req.LegacyDefaultApp
	if req.Root != "" {
		root, err := existingAbsoluteDir(req.Root)
		if err != nil {
			return resolvedComparisonSide{}, fmt.Errorf("side %s root: %w", req.Label, err)
		}
		roots = []string{root}
		defaultApp = ""
	}

	qualifiedApp, _, qualified := qualifiedSelector(req.Selector)
	if req.App != "" && qualified && qualifiedApp != req.App {
		return resolvedComparisonSide{}, fmt.Errorf("side %s app conflict: --%s-app names %q but selector %q names app %q",
			req.Label, req.Label, req.App, req.Selector, qualifiedApp)
	}

	switch {
	case req.App != "":
		defaultApp = req.App
	case qualified:
		defaultApp = qualifiedApp
	case req.GlobalApp != "":
		defaultApp = req.GlobalApp
	case req.Root != "":
		cfg, err := config.Discover(roots[0])
		if err != nil {
			return resolvedComparisonSide{}, fmt.Errorf("side %s root config: %w", req.Label, err)
		}
		defaultApp = cfg.App
		if defaultApp == "" {
			defaultApp = filepath.Base(roots[0])
		}
	}

	ref, root, err := resolveSideWithRoot(roots, defaultApp, req.Flow, req.Selector)
	if err != nil {
		return resolvedComparisonSide{}, err
	}
	resolved := resolvedComparisonSide{
		Ref: ref, Root: root, Selector: req.Selector,
		App: ref.Manifest.App, SearchRoots: roots,
	}
	if resolved.App == "" {
		resolved.App, _ = splitSelector(req.Selector, defaultApp)
	}
	if req.CommitSet && ref.Kind != "none" {
		if ref.Manifest.Git.SHA == "" {
			return resolvedComparisonSide{}, fmt.Errorf("side %s selected %s/%s/%s, but its manifest does not record a git commit; cannot assert --%s-commit %q",
				req.Label, resolved.App, req.Flow, ref.RunID, req.Label, req.Commit)
		}
		if ref.Manifest.Git.SHA != req.Commit {
			return resolvedComparisonSide{}, fmt.Errorf("side %s commit assertion %q does not exactly match the selected manifest's recorded commit %q",
				req.Label, req.Commit, ref.Manifest.Git.SHA)
		}
	}
	return resolved, nil
}

func isFullGitSHA(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, c := range value {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func (s resolvedComparisonSide) provenance() diff.SideProvenance {
	return diff.SideProvenance{
		Root: s.Root, Selector: s.Selector, Kind: s.Ref.Kind,
		App: s.Ref.Manifest.App, Flow: s.Ref.Manifest.Flow,
		RunID: s.Ref.RunID, RecordedGit: s.Ref.Manifest.Git,
	}
}

func qualifiedSelector(selector string) (app, sel string, ok bool) {
	before, after, found := strings.Cut(selector, "@")
	if !found || before == "" {
		return "", selector, false
	}
	return before, after, true
}

func existingAbsoluteDir(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("an empty value is not an existing directory")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("%q: %w", path, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("%s is not an existing directory: %w", abs, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", abs)
	}
	return abs, nil
}
