package proxy

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/core/trace"
)

// serveOne stands up one intercepted upstream and returns the proxy addr.
func serveOne(t *testing.T, p *Proxy, name string, h http.HandlerFunc) string {
	t.Helper()
	up := httptest.NewServer(h)
	t.Cleanup(up.Close)
	addr, err := p.Serve(Target{Name: name, Listen: "127.0.0.1:0", Upstream: up.URL})
	if err != nil {
		t.Fatal(err)
	}
	return addr
}

// TestSSEEventFlushesThroughBeforeStreamEnds is the flush-timing test: the
// first SSE event must reach the client while the upstream is still holding
// the stream open. A buffered relay (the pre-streaming io.Copy) only
// releases bytes at stream end, which here would deadlock: the upstream
// won't end until the client proves it received event one.
func TestSSEEventFlushesThroughBeforeStreamEnds(t *testing.T) {
	rec := NewRecorder(RecorderOpts{Ring: 8})
	p := New(rec)
	defer p.Close()

	gotFirst := make(chan struct{})
	addr := serveOne(t, p, "sse", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: one\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-gotFirst:
		case <-time.After(3 * time.Second):
			// Client never saw event one — fail via the read side's timeout.
		}
		fmt.Fprint(w, "data: two\n\n")
	})

	resp, err := http.Get("http://" + addr + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	type line struct {
		s   string
		err error
	}
	lines := make(chan line)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- line{s: sc.Text()}
		}
		lines <- line{err: io.EOF}
	}()
	readLine := func(what string) string {
		select {
		case l := <-lines:
			if l.err != nil {
				t.Fatalf("stream ended while waiting for %s", what)
			}
			return l.s
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for %s — the proxy buffered the stream instead of flushing through", what)
			return ""
		}
	}
	if got := readLine("event one"); got != "data: one" {
		t.Fatalf("first line = %q, want %q", got, "data: one")
	}
	close(gotFirst) // upstream may now finish
	for {
		got := readLine("event two")
		if got == "data: two" {
			break
		}
		if got != "" {
			t.Fatalf("unexpected line %q while waiting for event two", got)
		}
	}
}

// TestStreamingHopRecordedThenFinalizedInPlace pins the two-phase contract:
// a subscriber sees the hop at response-headers time (Streaming, no DoneMs,
// no body yet), then an Updated event with the SAME Seq carrying the final
// body and duration; the ring holds one finalized hop; and the NDJSON file
// carries exactly one line for it — the finalized one.
func TestStreamingHopRecordedThenFinalizedInPlace(t *testing.T) {
	var ndjson bytes.Buffer
	rec := NewRecorder(RecorderOpts{Ring: 8, Writer: trace.NewWriter(&ndjson)})
	p := New(rec)
	defer p.Close()

	release := make(chan struct{})
	addr := serveOne(t, p, "sse", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: one\n\n")
		w.(http.Flusher).Flush()
		<-release
		fmt.Fprint(w, "data: two\n\n")
	})

	ch, _, cancel := rec.Subscribe(0)
	defer cancel()
	next := func(what string) HopEvent {
		select {
		case ev := <-ch:
			return ev
		case <-time.After(3 * time.Second):
			t.Fatalf("timed out waiting for %s", what)
			return HopEvent{}
		}
	}

	go func() {
		resp, err := http.Get("http://" + addr + "/events")
		if err != nil {
			return
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()

	open := next("headers-time hop")
	if open.Updated || !open.Hop.Streaming {
		t.Fatalf("first event = %+v, want a fresh Streaming hop", open)
	}
	if open.Hop.T.DoneMs != 0 || open.Hop.Resp.Body != "" {
		t.Fatalf("headers-time hop already finalized: DoneMs=%v body=%q", open.Hop.T.DoneMs, open.Hop.Resp.Body)
	}

	close(release)
	fin := next("finalization")
	if !fin.Updated || fin.Hop.Seq != open.Hop.Seq {
		t.Fatalf("finalization = %+v, want Updated with Seq %d", fin, open.Hop.Seq)
	}
	if fin.Hop.T.DoneMs <= 0 || !strings.Contains(fin.Hop.Resp.Body, "data: two") {
		t.Fatalf("finalized hop = %+v, want a duration and the full body", fin.Hop)
	}

	snap := rec.Snapshot()
	if len(snap) != 1 || snap[0].Seq != open.Hop.Seq || snap[0].T.DoneMs <= 0 {
		t.Fatalf("ring = %+v, want the one finalized hop in place", snap)
	}

	// One NDJSON line per hop, written at finalize time — never two.
	rec.Close()
	got := strings.TrimSpace(ndjson.String())
	if n := len(strings.Split(got, "\n")); got == "" || n != 1 {
		t.Fatalf("hops file holds %d lines, want exactly 1 (the finalized hop):\n%s", n, got)
	}
	if !strings.Contains(got, "data: two") {
		t.Fatalf("persisted line lacks the final body: %s", got)
	}
}

// TestUnsupportedProtocolsRefusedWith501 covers both refusal families: a
// WebSocket upgrade and a gRPC call are answered 501 with an explanatory
// JSON body, recorded as flagged hops, and never forwarded.
func TestUnsupportedProtocolsRefusedWith501(t *testing.T) {
	rec := NewRecorder(RecorderOpts{Ring: 8})
	p := New(rec)
	defer p.Close()

	addr := serveOne(t, p, "svc", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("upstream reached for %s — an unsupported request must never be forwarded", r.URL.Path)
	})

	cases := []struct {
		name, proto string
		set         func(h http.Header)
	}{
		{"websocket upgrade header", "websocket", func(h http.Header) {
			h.Set("Upgrade", "websocket")
			h.Set("Connection", "Upgrade")
		}},
		{"connection upgrade token only", "websocket", func(h http.Header) {
			h.Set("Connection", "keep-alive, Upgrade")
		}},
		{"grpc content type", "grpc", func(h http.Header) {
			h.Set("Content-Type", "application/grpc+proto")
		}},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req, _ := http.NewRequest("GET", "http://"+addr+"/call", nil)
			c.set(req.Header)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusNotImplemented {
				t.Fatalf("status = %d, want 501", resp.StatusCode)
			}
			if !strings.Contains(string(body), c.proto) || !strings.Contains(string(body), "refused") {
				t.Fatalf("body = %s, want an explanation naming %q", body, c.proto)
			}
			hops := rec.Snapshot()
			if len(hops) != i+1 {
				t.Fatalf("hops = %d, want %d — the refusal itself must be recorded", len(hops), i+1)
			}
			h := hops[i]
			if h.Unsupported != c.proto || h.Status != http.StatusNotImplemented {
				t.Fatalf("hop = %+v, want Unsupported=%q Status=501", h, c.proto)
			}
		})
	}
}

// TestUnsupportedHopDegradesSessionVerdict: a refused WebSocket request
// entering through a session's edge must degrade that session's verdict
// with a note naming the protocol — the recording provably misses traffic.
func TestUnsupportedHopDegradesSessionVerdict(t *testing.T) {
	rec := NewRecorder(RecorderOpts{Ring: 64})
	p := New(rec)
	defer p.Close()
	frontProxy := buildChain(t, p, []string{"traceparent", "baggage"})

	mgr := NewSessionManager(p, rec, []string{"svc-front"})
	defer mgr.Close()
	ses, err := mgr.Start("run-ws", "svc-front", "http://"+frontProxy, "", 0)
	if err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest("GET", "http://"+ses.EdgeAddr+"/socket", nil)
	req.Header.Set("Upgrade", "websocket")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", resp.StatusCode)
	}

	waitFor(t, "degraded verdict", func() bool {
		v, _ := ses.Verdict()
		return v == trace.VerdictDegraded
	})
	_, reasons := ses.Verdict()
	found := false
	for _, r := range reasons {
		if strings.Contains(r, "websocket") && strings.Contains(r, "not captured") {
			found = true
		}
	}
	if !found {
		t.Fatalf("reasons = %q, want one naming websocket as uncaptured", reasons)
	}
}

// TestBinaryBodyCapturedAsBase64AndRelayedByteIdentical: a PNG-shaped
// response (invalid UTF-8, image/*) reaches the client byte-for-byte while
// the hop stores it as BodyB64 — never a lossy string round-trip.
func TestBinaryBodyCapturedAsBase64AndRelayedByteIdentical(t *testing.T) {
	png := append([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0xFF, 0xFE, 0x00}, []byte("payload")...)
	rec := NewRecorder(RecorderOpts{Ring: 8})
	p := New(rec)
	defer p.Close()

	addr := serveOne(t, p, "img", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(png)
	})

	resp, err := http.Get("http://" + addr + "/logo.png")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Equal(got, png) {
		t.Fatalf("client received %x, want the exact upstream bytes %x", got, png)
	}

	hops := rec.Snapshot()
	if len(hops) != 1 {
		t.Fatalf("want 1 hop, got %d", len(hops))
	}
	h := hops[0]
	if h.Resp.Body != "" {
		t.Fatalf("binary body landed in the string field: %q", h.Resp.Body)
	}
	decoded, err := base64.StdEncoding.DecodeString(h.Resp.BodyB64)
	if err != nil || !bytes.Equal(decoded, png) {
		t.Fatalf("BodyB64 does not decode to the original bytes (err=%v)", err)
	}
}

// TestProxyCapturesEverySetCookieInOrder: three Set-Cookie response headers
// must land in Payload.SetCookies as three ordered values — the flattened
// Headers map's comma-join is lossy for cookies specifically.
func TestProxyCapturesEverySetCookieInOrder(t *testing.T) {
	rec := NewRecorder(RecorderOpts{Ring: 8})
	p := New(rec)
	defer p.Close()

	cookies := []string{
		"sid=abc; Path=/; HttpOnly",
		"pref=dark; Expires=Wed, 21 Oct 2026 07:28:00 GMT",
		"csrf=xyz; SameSite=Strict",
	}
	addr := serveOne(t, p, "svc", func(w http.ResponseWriter, r *http.Request) {
		for _, c := range cookies {
			w.Header().Add("Set-Cookie", c)
		}
		fmt.Fprint(w, "ok")
	})

	resp, err := http.Get("http://" + addr + "/login")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	hops := rec.Snapshot()
	if len(hops) != 1 {
		t.Fatalf("want 1 hop, got %d", len(hops))
	}
	got := hops[0].Resp.SetCookies
	if len(got) != len(cookies) {
		t.Fatalf("SetCookies = %q, want all %d cookies", got, len(cookies))
	}
	for i := range cookies {
		if got[i] != cookies[i] {
			t.Fatalf("SetCookies[%d] = %q, want %q (order preserved)", i, got[i], cookies[i])
		}
	}
}

// TestServerClosesConnectionThatNeverSendsHeaders proves ReadHeaderTimeout
// is actually wired on the proxy's servers: a connection that dials and
// then says nothing is reclaimed instead of holding a goroutine forever.
func TestServerClosesConnectionThatNeverSendsHeaders(t *testing.T) {
	saved := ServerReadHeaderTimeout
	ServerReadHeaderTimeout = 150 * time.Millisecond
	defer func() { ServerReadHeaderTimeout = saved }()

	rec := NewRecorder(RecorderOpts{Ring: 8})
	p := New(rec)
	defer p.Close()
	addr := serveOne(t, p, "svc", func(w http.ResponseWriter, r *http.Request) {})

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("server sent bytes to a silent connection; want it closed by the header timeout")
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("connection still open after the read-header timeout — ReadHeaderTimeout not wired")
	}
}
