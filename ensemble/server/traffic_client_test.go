package server_test

import (
	"bufio"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/core/trace"
)

// chainHops is the shape this whole feature exists to separate: two client
// apps on one stack, each with an entry hop and a downstream call. Only
// app-next's chain dropped baggage, so its inner hop carries no client of
// its own and must be recovered by trace.
func chainHops() []trace.Hop {
	return []trace.Hop{
		{TraceID: "t1", Client: "app-legacy", To: "gateway", Method: "GET", Path: "/home", Status: 200},
		{TraceID: "t1", Client: "app-legacy", To: "wallet", Method: "GET", Path: "/v1/wallet", Status: 200},
		{TraceID: "t2", Client: "app-next", To: "edge", Method: "GET", Path: "/home", Status: 200},
		{TraceID: "t2", To: "toolkit-api", Method: "GET", Path: "/v1/profile", Status: 404},
		{TraceID: "t3", To: "health", Method: "GET", Path: "/healthz", Status: 200},
	}
}

func recordChains(e *testEnv) {
	for _, h := range chainHops() {
		e.rec.Record(h)
	}
}

func trafficHops(t *testing.T, e *testEnv, path string) []trace.Hop {
	t.Helper()
	_, body := e.get(t, path)
	var got struct {
		Hops []trace.Hop `json:"hops"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	return got.Hops
}

func paths(hops []trace.Hop) []string {
	out := make([]string, 0, len(hops))
	for _, h := range hops {
		out = append(out, h.Path)
	}
	return out
}

func TestTrafficClientFilterIncludesTheWholeChain(t *testing.T) {
	e := newTestEnv(t)
	recordChains(e)

	got := trafficHops(t, e, "/api/traffic?client=app-next")
	want := []string{"/home", "/v1/profile"}
	if strings.Join(paths(got), ",") != strings.Join(want, ",") {
		t.Fatalf("client=app-next returned %v, want %v — the downstream hop carries no client of its own and must be recovered through its trace", paths(got), want)
	}
}

func TestTrafficClientFilterExcludesOtherClients(t *testing.T) {
	e := newTestEnv(t)
	recordChains(e)

	for _, h := range trafficHops(t, e, "/api/traffic?client=app-legacy") {
		if h.To == "edge" || h.To == "toolkit-api" {
			t.Fatalf("client=app-legacy leaked a hop to %q", h.To)
		}
	}
}

func TestTrafficUnattributedSentinelSelectsOnlyOrphans(t *testing.T) {
	e := newTestEnv(t)
	recordChains(e)

	got := trafficHops(t, e, "/api/traffic?client="+trace.UnattributedClient)
	if len(got) != 1 || got[0].To != "health" {
		t.Fatalf("unattributed filter returned %v, want just the health hop — a hop whose trace names no client belongs to no client", paths(got))
	}
}

func TestTrafficClientFilterComposesWithErrorsOnly(t *testing.T) {
	e := newTestEnv(t)
	recordChains(e)

	got := trafficHops(t, e, "/api/traffic?client=app-next&errorsOnly=true")
	if len(got) != 1 || got[0].Status != 404 {
		t.Fatalf("client+errorsOnly returned %v, want only the 404", paths(got))
	}
}

// The index is built over the whole ring, not over the window `since`
// selects. A chain whose entry hop has fallen behind the cursor must keep
// reporting the client that started it, or paging would re-attribute the
// same trace differently on every poll.
func TestTrafficClientFilterResolvesAcrossTheSinceCursor(t *testing.T) {
	e := newTestEnv(t)
	recordChains(e)

	all := trafficHops(t, e, "/api/traffic")
	var entrySeq uint64
	for _, h := range all {
		if h.To == "edge" {
			entrySeq = h.Seq
		}
	}
	if entrySeq == 0 {
		t.Fatal("no entry hop recorded")
	}

	got := trafficHops(t, e, "/api/traffic?client=app-next&since="+itoa(entrySeq))
	if len(got) != 1 || got[0].Path != "/v1/profile" {
		t.Fatalf("since past the entry hop returned %v, want the downstream hop still attributed to app-next", paths(got))
	}
}

func TestTrafficNoClientParamIsUnfiltered(t *testing.T) {
	e := newTestEnv(t)
	recordChains(e)

	if got := trafficHops(t, e, "/api/traffic"); len(got) != len(chainHops()) {
		t.Fatalf("unfiltered traffic returned %d hops, want %d", len(got), len(chainHops()))
	}
}

func TestTrafficStreamFiltersByClient(t *testing.T) {
	e := newTestEnv(t)
	recordChains(e)

	req, err := http.NewRequest(http.MethodGet, e.ts.URL+"/api/traffic/stream?client=app-next", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := contextWithTimeout(3 * time.Second)
	defer cancel()
	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// The replay alone carries app-next's two hops; reading exactly that
	// many proves both the entry hop and the trace-resolved downstream hop
	// reached a filtered subscriber, and nothing else did.
	var seen []string
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() && len(seen) < 2 {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var h trace.Hop
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &h); err != nil {
			t.Fatalf("stream payload: %v", err)
		}
		if h.Client != "" && h.Client != "app-next" {
			t.Fatalf("stream delivered a hop for client %q on an app-next stream", h.Client)
		}
		seen = append(seen, h.Path)
	}
	want := []string{"/home", "/v1/profile"}
	if strings.Join(seen, ",") != strings.Join(want, ",") {
		t.Fatalf("stream delivered %v, want %v", seen, want)
	}
}
