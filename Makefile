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

build: ui
	go build -o ensemble ./ensemble/cmd/ensemble
	go build -o retrace ./retrace/cmd/retrace

install: ui
	go install ./ensemble/cmd/ensemble
	go install ./retrace/cmd/retrace

clean:
	rm -f ensemble retrace
	git checkout -- ensemble/server/ui/dist
