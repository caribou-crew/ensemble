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
	# dist/index.html is a tracked placeholder that this build overwrites in
	# place (see .gitignore's note on it), which would otherwise make every
	# build show up as a dirty commit to both `git status` and go build's
	# VCS stamping (core/buildinfo) — even on an unmodified checkout.
	# skip-worktree tells git to stop diffing this specific file against
	# the index, since the built content is expected to differ locally and
	# is never meant to be committed.
	git update-index --skip-worktree ensemble/server/ui/dist/index.html

build: ui
	go build -o ensemble ./ensemble/cmd/ensemble
	go build -o retrace ./retrace/cmd/retrace

install: ui
	go install ./ensemble/cmd/ensemble
	go install ./retrace/cmd/retrace

clean:
	rm -f ensemble retrace
	git update-index --no-skip-worktree ensemble/server/ui/dist/index.html
	git checkout -- ensemble/server/ui/dist
