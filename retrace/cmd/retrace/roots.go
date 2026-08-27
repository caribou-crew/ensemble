package main

import (
	"fmt"
	"path/filepath"
	"strings"
)

// A "root" is a repository directory — the one holding `.retrace/` — not the
// runs directory inside it. Everything else in this CLI already takes the
// repo directory (cwd) and derives the rest, and a flag that took the inner
// path would be the one place where the two differ.
//
// Repeating --root is what makes a cross-repo comparison possible: the run
// recorded by the web repo's suite and the one recorded by the mobile repo's
// live in different trees, and until now `diff` could only see the tree it
// was invoked from. That is not a convenience gap. Two clients hitting the
// same backend is exactly where "did the stack change or did my client?" is
// hardest to answer, and answering it requires both recordings at once.
type rootList []string

func (r *rootList) String() string { return strings.Join(*r, ", ") }

// Set makes each root absolute. The list ends up in error messages that name
// which root a run was found in, and two different relative paths that mean
// the same directory would print as if they were different places.
//
// Duplicates are dropped rather than rejected: `--root . --root $(pwd)` is a
// script being careful, not a mistake, and keeping both would make every
// lookup ambiguous against itself.
func (r *rootList) Set(v string) error {
	v = strings.TrimSpace(v)
	if v == "" {
		return fmt.Errorf("--root needs a directory")
	}
	abs, err := filepath.Abs(v)
	if err != nil {
		return fmt.Errorf("--root %s: %w", v, err)
	}
	for _, have := range *r {
		if have == abs {
			return nil
		}
	}
	*r = append(*r, abs)
	return nil
}

// resolve returns the roots to search, defaulting to the working directory.
// A caller never sees an empty list, so the single-root path and the
// many-roots path are one code path rather than two.
func (r rootList) resolve(cwd string) []string {
	if len(r) == 0 {
		return []string{cwd}
	}
	return r
}

// splitSelector reads the `app@selector` form, falling back to the CLI's own
// --app for a bare selector.
//
// The separator is the FIRST "@": run ids and git shas never contain one, and
// runs.validateComponents already forbids it in an app name, so there is no
// ambiguity to resolve — only a decision about where to cut, and cutting at
// the first keeps a stray "@" inside a selector a lookup failure rather than
// a silently different app.
func splitSelector(sel, defaultApp string) (app, selector string) {
	before, after, found := strings.Cut(sel, "@")
	if !found {
		return defaultApp, sel
	}
	if before == "" {
		// "@latest" — the separator with nothing before it. Read as the
		// default app rather than as an empty app name, which would fail a
		// component check with a message about validation instead of about
		// the thing the user typed.
		return defaultApp, after
	}
	return before, after
}
