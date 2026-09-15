package capture

import (
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/retrace/runs"
)

func startAttachedWithAPI(t *testing.T, c EnsembleClient, api string) *Session {
	t.Helper()
	s, err := StartAttached(Options{
		Cwd: t.TempDir(), App: "web", Flow: "checkout", EnsembleAPI: api,
		Now: func() time.Time { return time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC) },
	}, c, "bff")
	if err != nil {
		t.Fatalf("StartAttached: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// The link must record the session id that was actually REGISTERED, read
// back from the control plane that received it — not a value re-derived
// from the run id. The two are equal by StartAttached's own choice today;
// a reader who assumed that and was wrong would follow a link to another
// run's traffic.
func TestEnsembleLinkRecordsTheRegisteredSession(t *testing.T) {
	f := &fakeEnsemble{}
	s := startAttachedWithAPI(t, f, "http://127.0.0.1:4700")

	link := s.EnsembleLink()
	if link == nil {
		t.Fatal("an attached run recorded no ensemble link")
	}
	if link.API != "http://127.0.0.1:4700" {
		t.Errorf("link.API = %q, want the control plane the run attached to", link.API)
	}
	if got, want := link.Session, f.RegisteredID(); got != want {
		t.Errorf("link.Session = %q, but the control plane was asked to register %q", got, want)
	}
}

// A caller that never named the control plane's address gets NO link — not
// one pointing at "". Every test fake is such a caller, and a manifest
// claiming an attachment it cannot name is worse than one admitting it does
// not know.
func TestEnsembleLinkAbsentWhenTheAPIIsUnnamed(t *testing.T) {
	s := attachedSessionFor(t, &fakeEnsemble{})
	if link := s.EnsembleLink(); link != nil {
		t.Fatalf("EnsembleLink = %+v, want nil when no control-plane address was given", link)
	}
}

// Standalone mode has no control plane at all, and must not inherit one
// from an option the caller happened to set.
func TestEnsembleLinkAbsentInStandaloneMode(t *testing.T) {
	s, err := StartStandalone(Options{
		Cwd: t.TempDir(), App: "web", Flow: "checkout",
		Upstream: "http://127.0.0.1:1", EnsembleAPI: "http://127.0.0.1:4700",
		Now: func() time.Time { return time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("StartStandalone: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if link := s.EnsembleLink(); link != nil {
		t.Fatalf("standalone EnsembleLink = %+v, want nil", link)
	}
}

// A half-filled link is not a weaker link, it is an unusable one: a URL
// with no session scopes to the whole stack, a session with no URL names a
// control plane nobody can find. Both would render as a link that goes
// somewhere wrong.
func TestManifestRejectsAHalfFilledEnsembleLink(t *testing.T) {
	for _, tc := range []struct {
		name string
		link runs.EnsembleLink
	}{
		{"no api", runs.EnsembleLink{Session: "run-7"}},
		{"no session", runs.EnsembleLink{API: "http://127.0.0.1:4700"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := runs.Create(runs.RunsRoot(t.TempDir()), "web", "checkout", "run-7")
			if err != nil {
				t.Fatal(err)
			}
			m := runs.Manifest{
				Capture:  runs.CaptureTrust{Status: "ok"},
				Wire:     runs.Counts{Recorded: true},
				Ensemble: &tc.link,
			}
			if err := runs.WriteManifest(p, &m); err == nil {
				t.Fatal("WriteManifest accepted a half-filled ensemble link")
			}
		})
	}
}
