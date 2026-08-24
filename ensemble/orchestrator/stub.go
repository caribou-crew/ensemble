package orchestrator

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/caribou-crew/ensemble/core/stub"
	"github.com/caribou-crew/ensemble/ensemble/config"
)

// startStubs starts every config-defined stub not already running — called
// once from Up, and by Reconcile for an individually added/changed stub.
// Errors are joined rather than aborting: a stub is independent of every
// other node in the stack, so one failing to bind shouldn't stop the rest
// from starting (same per-node philosophy as Up's service/database loop).
func (o *Orchestrator) startStubs() error {
	names := make([]string, 0, len(o.cfg.Stubs))
	for name := range o.cfg.Stubs {
		names = append(names, name)
	}
	sort.Strings(names)

	var errs []error
	for _, name := range names {
		if err := o.startStub(name, o.cfg.Stubs[name]); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// startStub starts one config-defined stub, mapping config.Stub/
// config.StubRoute onto core/stub's types (kept as separate types
// deliberately — config is the on-disk YAML shape, stub is the runtime
// shape; ported as-is from cmd_up.go's original startStubs).
func (o *Orchestrator) startStub(name string, st config.Stub) error {
	routes := make([]stub.Route, len(st.Routes))
	for i, r := range st.Routes {
		// BodyFile is declared relative to Config.Dir, same as
		// SeedSQL.File — not the process CWD, which is what core/stub's
		// os.ReadFile would otherwise use.
		bodyFile := r.Respond.BodyFile
		if bodyFile != "" && !filepath.IsAbs(bodyFile) {
			bodyFile = filepath.Join(o.cfg.Dir, bodyFile)
		}
		routes[i] = stub.Route{
			Match: stub.Match{Method: r.Match.Method, Path: r.Match.Path},
			Respond: stub.Respond{
				Status: r.Respond.Status, Headers: r.Respond.Headers,
				Body: r.Respond.Body, BodyFile: bodyFile, Template: r.Respond.Template,
			},
		}
	}
	s := stub.New(name, routes, o.Rec)
	s.TraceHeader = o.cfg.TraceHeader
	listen := "127.0.0.1:0"
	if st.Port != 0 {
		listen = fmt.Sprintf("127.0.0.1:%d", st.Port)
	}
	if _, err := s.Serve(listen); err != nil {
		return fmt.Errorf("stub %s: %w", name, err)
	}
	o.mu.Lock()
	o.stubs[name] = s
	o.mu.Unlock()
	return nil
}

// stopStub closes name's running stub, if any, and forgets it. A no-op for
// a name that isn't running.
func (o *Orchestrator) stopStub(name string) {
	o.mu.Lock()
	s, ok := o.stubs[name]
	delete(o.stubs, name)
	o.mu.Unlock()
	if ok {
		s.Close()
	}
}

