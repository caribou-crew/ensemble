package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/core/trace"
)

// End to end through a real stack: a request carrying a client-identity
// header, then `ensemble traffic --client` narrowing to it. The point of
// the feature in one test — no jq, no post-processing.
func TestCLI_TrafficClientFilter(t *testing.T) {
	env := startEnsemble(t)

	for _, client := range []string{"app-legacy", "app-next"} {
		req, err := http.NewRequest("GET", fmt.Sprintf("http://127.0.0.1:%d/hello", env.proxyPort), nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("x-source-client", client)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("proxy request for %s: %v", client, err)
		}
		resp.Body.Close()
	}

	var out TrafficResponse
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"traffic", "--api-url", env.apiURL, "--json", "--client", "app-legacy"}, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("traffic --client exit = %d, stderr = %s", code, stderr.String())
		}
		out = TrafficResponse{}
		if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
			t.Fatalf("decode stdout: %v; stdout = %s", err, stdout.String())
		}
		if len(out.Hops) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(out.Hops) == 0 {
		t.Fatal("traffic --client app-legacy returned no hops")
	}
	for _, h := range out.Hops {
		if h.Client != "app-legacy" {
			t.Fatalf("hop recorded client %q on an app-legacy filter", h.Client)
		}
	}
}

// stubTrafficAPI records the query string of the last /api/traffic request
// and answers with an empty hop list.
func stubTrafficAPI(t *testing.T, gotQuery *string) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/traffic") {
			*gotQuery = r.URL.RawQuery
		}
		w.Header().Set("content-type", "application/json")
		fmt.Fprint(w, `{"hops":[]}`)
	}))
	t.Cleanup(ts.Close)
	return ts
}

// The filter must travel to the server, not be applied locally: a CLI that
// post-filtered would disagree with the API the dashboard uses the moment
// the rule changed on one side.
func TestCLI_TrafficClientIsSentToTheServer(t *testing.T) {
	var query string
	ts := stubTrafficAPI(t, &query)

	var stdout, stderr bytes.Buffer
	code := run([]string{"traffic", "--api-url", ts.URL, "--json", "--client", "app-next"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(query, "client=app-next") {
		t.Fatalf("request query = %q, want it to carry client=app-next", query)
	}
}

func TestCLI_TrafficClientComposesWithSessionAndErrorsOnly(t *testing.T) {
	var query string
	ts := stubTrafficAPI(t, &query)

	var stdout, stderr bytes.Buffer
	code := run([]string{"traffic", "--api-url", ts.URL, "--json",
		"--session", "run-7", "--client", "app-next", "--errors-only"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	for _, want := range []string{"client=app-next", "session=run-7", "errorsOnly=true"} {
		if !strings.Contains(query, want) {
			t.Errorf("request query = %q, want it to carry %s", query, want)
		}
	}
}

func TestCLI_TrafficUnattributedSentinelIsSentVerbatim(t *testing.T) {
	var query string
	ts := stubTrafficAPI(t, &query)

	var stdout, stderr bytes.Buffer
	code := run([]string{"traffic", "--api-url", ts.URL, "--json", "--client", trace.UnattributedClient}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(query, "client=%28none%29") {
		t.Fatalf("request query = %q, want the unattributed sentinel url-encoded", query)
	}
}

// --export renders a whole session. Combining it with --client must be
// refused, not silently ignored: returning every client's hops to a command
// line that asked for one client's is a wrong answer dressed as a filtered
// one.
func TestCLI_TrafficExportRefusesAClientFilter(t *testing.T) {
	var query string
	ts := stubTrafficAPI(t, &query)

	var stdout, stderr bytes.Buffer
	code := run([]string{"traffic", "--api-url", ts.URL, "--session", "run-7", "--client", "app-next", "--export", "har"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit = %d, want 2; stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--client") {
		t.Fatalf("stderr = %q, want it to name the refused flag", stderr.String())
	}
	if query != "" {
		t.Fatalf("a refused command still called the API (query = %q)", query)
	}
}
