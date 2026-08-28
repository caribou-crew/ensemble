package capture

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/caribou-crew/ensemble/retrace/config"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

func twoUpstreams(t *testing.T) (edge, auth *httptest.Server) {
	t.Helper()
	edge = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"from":"edge"}`))
	}))
	auth = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"from":"auth"}`))
	}))
	t.Cleanup(edge.Close)
	t.Cleanup(auth.Close)
	return edge, auth
}

func TestMultiListenerCapturesBothUpstreamsWithDistinctHopTags(t *testing.T) {
	edge, auth := twoUpstreams(t)
	s, err := StartStandalone(Options{
		Cwd: t.TempDir(), App: "web", Flow: "checkout",
		Listeners: []config.ListenerEntry{
			{Name: "edge", Upstream: edge.URL},
			{Name: "auth", Upstream: auth.URL},
		},
	})
	if err != nil {
		t.Fatalf("StartStandalone: %v", err)
	}
	defer s.Close()

	if len(s.listeners) != 2 {
		t.Fatalf("got %d listeners, want 2", len(s.listeners))
	}
	if _, err := http.Get(s.listeners[0].ProxyURL + "/x"); err != nil {
		t.Fatalf("GET via edge listener: %v", err)
	}
	if _, err := http.Get(s.listeners[1].ProxyURL + "/y"); err != nil {
		t.Fatalf("GET via auth listener: %v", err)
	}

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	hops, skipped, err := runs.ReadHops(s.Paths.WirePath)
	if err != nil || skipped != 0 {
		t.Fatalf("ReadHops: %v (skipped=%d)", err, skipped)
	}
	if len(hops) != 2 {
		t.Fatalf("got %d hops, want 2", len(hops))
	}
	tags := map[string]bool{}
	for _, h := range hops {
		tags[h.To] = true
	}
	if !tags["edge"] || !tags["auth"] {
		t.Fatalf("hop tags = %v, want both edge and auth", tags)
	}
}

func TestMultiListenerEnvExportsPerListenerAndDefaultVars(t *testing.T) {
	edge, auth := twoUpstreams(t)
	s, err := StartStandalone(Options{
		Cwd: t.TempDir(), App: "web", Flow: "checkout",
		Listeners: []config.ListenerEntry{
			{Name: "edge", Upstream: edge.URL},
			{Name: "auth", Upstream: auth.URL},
		},
	})
	if err != nil {
		t.Fatalf("StartStandalone: %v", err)
	}
	defer s.Close()

	env := map[string]string{}
	for _, kv := range s.Env() {
		for i := 0; i < len(kv); i++ {
			if kv[i] == '=' {
				env[kv[:i]] = kv[i+1:]
				break
			}
		}
	}
	if env["RETRACE_PROXY_URL"] == "" || env["RETRACE_PROXY_URL"] != env["RETRACE_PROXY_URL_EDGE"] {
		t.Errorf("RETRACE_PROXY_URL = %q, want it to equal RETRACE_PROXY_URL_EDGE (%q)", env["RETRACE_PROXY_URL"], env["RETRACE_PROXY_URL_EDGE"])
	}
	if env["RETRACE_PROXY_URL_AUTH"] == "" {
		t.Error("RETRACE_PROXY_URL_AUTH missing")
	}
	if env["RETRACE_PROXY_URL_AUTH"] == env["RETRACE_PROXY_URL_EDGE"] {
		t.Error("edge and auth listeners exported the same address")
	}
}

func TestMultiListenerCloseStopsEveryListener(t *testing.T) {
	edge, auth := twoUpstreams(t)
	s, err := StartStandalone(Options{
		Cwd: t.TempDir(), App: "web", Flow: "checkout",
		Listeners: []config.ListenerEntry{
			{Name: "edge", Upstream: edge.URL},
			{Name: "auth", Upstream: auth.URL},
		},
	})
	if err != nil {
		t.Fatalf("StartStandalone: %v", err)
	}
	edgeURL, authURL := s.listeners[0].ProxyURL, s.listeners[1].ProxyURL
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := http.Get(edgeURL); err == nil {
		t.Error("edge listener still answering after Close")
	}
	if _, err := http.Get(authURL); err == nil {
		t.Error("auth listener still answering after Close")
	}
}

func TestSingleEntryListenersSliceBehavesLikeLegacyUpstream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	s, err := StartStandalone(Options{
		Cwd: t.TempDir(), App: "web", Flow: "checkout",
		Listeners: []config.ListenerEntry{{Name: "client-edge", Upstream: upstream.URL}},
	})
	if err != nil {
		t.Fatalf("StartStandalone: %v", err)
	}
	defer s.Close()
	if _, err := http.Get(s.ProxyURL); err != nil {
		t.Fatalf("GET: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	hops, _, err := runs.ReadHops(s.Paths.WirePath)
	if err != nil {
		t.Fatalf("ReadHops: %v", err)
	}
	if len(hops) != 1 || hops[0].To != "client-edge" {
		t.Fatalf("hops = %+v, want one client-edge hop", hops)
	}
}
