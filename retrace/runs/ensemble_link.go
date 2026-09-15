package runs

import "fmt"

// EnsembleLink is the control plane a run was captured against, recorded at
// session start.
//
// It exists so a run and its hops are one navigable object rather than two
// artifacts that happen to share an id. `retrace run` already registers its
// run id AS ensemble's session id, so every hop of the run already carries
// it in trace.BaggageSession — but nothing recorded WHICH control plane, so
// neither dashboard could offer a link to the other. This is that missing
// half, and it is the whole of it: no new endpoint, no new id.
//
// Session is recorded even though it is, today, always equal to
// Manifest.RunID. The link should say what was actually registered rather
// than make every future reader re-derive an equality that holds only by
// the current implementation's choice — and a reader who assumed it and was
// wrong would follow a link to another run's traffic.
type EnsembleLink struct {
	// API is the control-plane base URL (a local development address).
	API string `json:"api"`
	// Session is the session id registered with that control plane, which
	// is what a hop's `session` field carries.
	Session string `json:"session"`
}

// validateEnsembleLink rejects a half-filled link. Called from both
// WriteManifest and ReadManifest, like validateStack: a link with one field
// empty is not a weaker link, it is an unusable one — a URL with no session
// scopes to the whole stack, and a session with no URL names a control
// plane nobody can find. Either would render as a link that goes somewhere
// wrong, which is worse than the honest absence a nil pointer encodes.
func validateEnsembleLink(l *EnsembleLink) error {
	if l == nil {
		return nil
	}
	if l.API == "" {
		return fmt.Errorf("runs: manifest ensemble link has no api url — omit the link instead, so \"not attached\" stays distinguishable from \"attached to somewhere unnamed\"")
	}
	if l.Session == "" {
		return fmt.Errorf("runs: manifest ensemble link has no session id — omit the link instead; a link with no session scopes to the whole stack rather than to this run")
	}
	return nil
}
