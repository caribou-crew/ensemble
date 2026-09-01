# The dashboard (dashboard/ensemble-ui) is embedded into the ensemble binary
# at compile time via go:embed (see ensemble/server/ui/ui.go) — the JS build
# has to run *before* the Go build, every time, or the binary serves the
# committed placeholder ("UI not built. Run pnpm -r build ..."). These
# targets exist so that ordering is never something you have to remember.

.PHONY: deps ui build install clean

deps:
	pnpm install

ui:
	pnpm -r build
	# Both dist/index.html placeholders (ensemble's and retrace's) are
	# tracked, and `pnpm -r build` overwrites both in place (see
	# .gitignore's note on them), which would otherwise make every build
	# show up as a dirty commit to both `git status` and go build's VCS
	# stamping (core/buildinfo) — even on an unmodified checkout.
	# skip-worktree tells git to stop diffing these specific files against
	# the index, since the built content is expected to differ locally and
	# is never meant to be committed.
	git update-index --skip-worktree ensemble/server/ui/dist/index.html
	git update-index --skip-worktree retrace/serve/ui/dist/index.html

# Note the bin/ prefix: `-o ensemble` would name an EXISTING directory (the
# ensemble/ module), and Go treats a directory -o as "write the binary into
# it" — the result lands at ensemble/ensemble, not repo root, and a root
# file can't share the module dir's name anyway.
build: ui
	mkdir -p bin
	go build -o bin/ensemble ./ensemble/cmd/ensemble
	go build -o bin/retrace ./retrace/cmd/retrace

install: ui
	go install ./ensemble/cmd/ensemble
	go install ./retrace/cmd/retrace

clean:
	rm -rf bin
	git update-index --no-skip-worktree ensemble/server/ui/dist/index.html
	git update-index --no-skip-worktree retrace/serve/ui/dist/index.html
	git checkout -- ensemble/server/ui/dist retrace/serve/ui/dist
