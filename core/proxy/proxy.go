package proxy

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/caribou-crew/ensemble/core/trace"
)

// Target is one intercepted service: a listen address fronting an upstream.
type Target struct {
	Name     string // logical service name, becomes Hop.To
	Listen   string // e.g. "127.0.0.1:7003"; ":0" picks an ephemeral port
	Upstream string // base URL of the real service, e.g. "http://localhost:8003"
	// InjectBaggage entries are forced into the trace context of every
	// request through this listener — how a session's client-edge port
	// stamps retrace-run without the client knowing anything about it.
	InjectBaggage map[string]string
	// Routes, when non-empty, makes this listener a gateway: the upstream
	// is chosen per request by longest path-prefix match (see Route) and
	// Upstream is unused. A request matching no route is answered 404 and
	// still recorded as a hop, so a mis-routed client shows up in the
	// traffic stream rather than vanishing.
	Routes []Route
	// CORS, when set, adds cross-origin response headers to every request
	// through this listener and answers a preflight OPTIONS request
	// directly (204, no upstream call, not recorded as a hop — it's
	// synthetic browser traffic, not application traffic). nil disables
	// CORS entirely; see CORSPolicy.
	CORS *CORSPolicy
	// CalledBy is a config-declared fallback caller hint, used only when
	// SpanOwner has no real trace-derived answer (the inbound request
	// carried no traceparent this proxy claimed, or none at all — typical
	// of an off-the-shelf backend with no trace-context propagation). One
	// entry attributes the hop directly; more than one means the hint is
	// ambiguous, so every candidate is surfaced jointly. Either way the hop
	// is marked Hop.Attribution = "inferred" so the UI never presents a
	// guess as ground truth.
	CalledBy []string
}

// CallerHeader is the default request header letting a caller ensemble
// doesn't manage (a dev-only client, another team's tool, a bare curl)
// self-declare its name when it has no real trace context and no
// config-declared CalledBy hint to fall back on. Honored only when
// SpanOwner has no real trace-derived answer; the resulting hop is marked
// Hop.Attribution = "declared" — a caller-asserted name, not ground truth,
// but more specific than a static config guess. Used only when
// Proxy.SourceHeaders is unset — an org with its own existing header
// convention (e.g. "X-Source-Client") sets SourceHeaders instead.
const CallerHeader = "X-Ensemble-Caller"

// CaptureLimit caps how much request/response body is *captured* (never how
// much is forwarded). Exported so other capture paths — notably
// core/stub, which has no upstream to stream through and so can't reuse
// cappedBuffer directly — cap at the same size instead of duplicating the
// number.
const CaptureLimit = 256 * 1024

// Proxy runs any number of intercept listeners inside one process. Each
// listener is a goroutine and a socket — per-service cost is kilobytes.
type Proxy struct {
	rec       *Recorder
	transport *http.Transport

	// Latency, when set, injects artificial delay per its rules. The sleep
	// happens before the upstream clock starts so recorded upstream timings
	// stay honest; the injected amount lands in Hop.InjectedDelayMs.
	Latency *LatencyStore

	// TraceHeader names a stack's own correlation header (e.g.
	// "x-local-trace-id"), read as a fallback trace id whenever a request
	// carries no real traceparent — see trace.ResolveInbound. Stack-wide,
	// not per-Target, since it's a convention the whole company's services
	// already share. Empty (the default) disables the fallback entirely,
	// preserving the exact prior behavior of always minting a fresh trace.
	TraceHeader string

	// SourceHeaders names request headers (checked in order, case-insensitive
	// per the HTTP header convention — see net/http.Header.Get) that let a
	// caller ensemble doesn't manage self-declare its name, replacing the
	// CallerHeader default entirely. The first header present on the request
	// wins, regardless of how many later entries are also present. Empty
	// (the default) falls back to checking only CallerHeader, preserving
	// prior behavior for a stack with no such convention of its own.
	SourceHeaders []string

	// ClientHeaders names request headers (checked in order,
	// case-insensitive) carrying the name of the CLIENT APPLICATION that
	// sent a request — "web", "ios", "admin". The first header PRESENT wins,
	// even if its value is malformed; see clientIdentity. Empty (the
	// default) checks DefaultClientHeaders.
	//
	// Not the same setting as SourceHeaders even though both name headers
	// that identify an origin, and they must not be merged: SourceHeaders
	// answers "who called this hop" in the service graph, is consulted only
	// when trace context has no answer, and accepts free text;
	// ClientHeaders answers "which of our front-ends started this", is read
	// unconditionally, and is validated to an identifier (see
	// trace.Hop.Client). A stack may reasonably set one and not the other.
	ClientHeaders []string

	// OnWarn receives non-fatal diagnostics from live traffic — today, a
	// malformed client identity. Nil (the default) discards them, which is
	// what keeps a library with no logger from having to grow one.
	//
	// Invoked serially, never concurrently, so a sink needs no lock of its
	// own. That is load-bearing: the natural sink is stderr.
	OnWarn func(string)

	mu      sync.Mutex
	servers []*http.Server

	// warnMu guards warnedClients AND serializes OnWarn — see warnBadClient.
	// Separate from mu, which guards the server list: a warning fired from a
	// request goroutine must never contend with Serve/Close.
	warnMu        sync.Mutex
	warnedClients map[string]bool
}

// declaredCaller returns the value of the first configured SourceHeaders
// entry present on r (or CallerHeader alone, if SourceHeaders is unset),
// or "" if none of them are set.
func (p *Proxy) declaredCaller(r *http.Request) string {
	headers := p.SourceHeaders
	if len(headers) == 0 {
		headers = []string{CallerHeader}
	}
	for _, h := range headers {
		if v := r.Header.Get(h); v != "" {
			return v
		}
	}
	return ""
}

func New(rec *Recorder) *Proxy {
	return &Proxy{
		rec: rec,
		transport: &http.Transport{
			MaxIdleConnsPerHost: 32,
			IdleConnTimeout:     90 * time.Second,
			// Local dev: no proxies, no TLS between local services.
		},
	}
}

// Serve opens the target's listener and starts intercepting immediately.
// It returns the bound address (useful with ":0").
func (p *Proxy) Serve(t Target) (string, error) {
	addr, _, err := p.ServeStoppable(t)
	return addr, err
}

// lookupIP resolves a hostname to its addresses. A package-level seam
// (rather than a Proxy field) so tests can substitute it without threading
// a resolver through every Target — see hostAddrs' doc comment for why the
// real resolver alone can't be exercised in a hermetic test.
var lookupIP = net.LookupIP

// hostAddrs resolves host (already known not to be an IP literal — see
// ServeStoppable) and returns the loopback listeners a request to that
// hostname must be able to land on. It refuses a host that resolves to
// anything else: this proxy injects trace baggage into every request that
// reaches it, and a capture listener reachable from a LAN interface would
// let an off-machine caller forge that baggage (the same threat model
// core/httpguard and design.md §6.1.2 already apply to the client-edge
// listener; see also ensemble/config/validate.go's "one 127.0.0.1 address
// space" invariant for intercept ports generally).
//
// Both families are bound on the SAME port explicitly (tcp4 127.0.0.1,
// then best-effort tcp6 ::1) rather than relying on net.Listen("tcp", host)
// to pick one: Go's resolver and a test's own HTTP client (Node, a browser)
// do not reliably agree on which address "localhost" means on a given
// machine, and binding only the address Go happened to pick would leave
// the listener unreachable at exactly the hostname it was configured to
// answer on. Deliberately does NOT reuse the caller's Listen string for
// either bind — both binds use their literal loopback address so there is
// no resolution step left to disagree on the way there.
func hostAddrs(host, port string) (ln4, ln6 net.Listener, err error) {
	ips, err := lookupIP(host)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve %q: %w", host, err)
	}
	for _, ip := range ips {
		if !ip.IsLoopback() {
			return nil, nil, fmt.Errorf("%q resolves to non-loopback address %s — a capture proxy must stay on loopback", host, ip)
		}
	}
	ln4, err = net.Listen("tcp4", net.JoinHostPort("127.0.0.1", port))
	if err != nil {
		return nil, nil, err
	}
	_, boundPort, _ := net.SplitHostPort(ln4.Addr().String())
	// Best-effort: a host with no IPv6 loopback (or a same-port v6 clash,
	// vanishingly unlikely for a freshly bound ephemeral port) still gets a
	// working listener via ln4 alone.
	ln6, _ = net.Listen("tcp6", net.JoinHostPort("::1", boundPort))
	return ln4, ln6, nil
}

// ServeStoppable is Serve with a per-listener stop, used for ephemeral
// listeners like session client-edge ports.
//
// t.Listen's host may be a literal IP (unchanged behavior: one listener,
// bound and advertised exactly as given — net.ParseIP recognizes it, so no
// resolution happens at all) or a hostname such as "localhost". A hostname
// is resolved and MUST be loopback-only (see hostAddrs); it binds on both
// loopback families where available, and the returned address is built
// from the CONFIGURED hostname string, never the listener's resolved
// address — so it reads back exactly as configured (e.g. "localhost:53221")
// regardless of which family a given client happens to dial. This exists
// for URL-bound auth schemes (DPoP/RFC 9449 and similar) whose validation
// compares hostnames; see design.md §6.1.2. It does not touch port
// selection — ":0" still picks an ephemeral port — so it does not
// reintroduce the shared-fixed-port design that was rejected there.
func (p *Proxy) ServeStoppable(t Target) (string, func(), error) {
	host, port, err := net.SplitHostPort(t.Listen)
	if err != nil {
		return "", nil, fmt.Errorf("proxy %s: %w", t.Name, err)
	}

	var lns []net.Listener
	advertise := t.Listen
	if host != "" && net.ParseIP(host) == nil {
		ln4, ln6, herr := hostAddrs(host, port)
		if herr != nil {
			return "", nil, fmt.Errorf("proxy %s: %w", t.Name, herr)
		}
		lns = append(lns, ln4)
		if ln6 != nil {
			lns = append(lns, ln6)
		}
		_, boundPort, _ := net.SplitHostPort(ln4.Addr().String())
		advertise = net.JoinHostPort(host, boundPort)
	} else {
		ln, lerr := net.Listen("tcp", t.Listen)
		if lerr != nil {
			return "", nil, fmt.Errorf("proxy %s: %w", t.Name, lerr)
		}
		lns = append(lns, ln)
		advertise = ln.Addr().String()
	}

	srv := &http.Server{Handler: p.handler(t)}
	p.mu.Lock()
	p.servers = append(p.servers, srv)
	p.mu.Unlock()
	for _, ln := range lns {
		go srv.Serve(ln)
	}
	stop := func() {
		srv.Close()
		p.mu.Lock()
		for i, s := range p.servers {
			if s == srv {
				p.servers = append(p.servers[:i], p.servers[i+1:]...)
				break
			}
		}
		p.mu.Unlock()
	}
	return advertise, stop, nil
}

// Close shuts every listener down.
func (p *Proxy) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, srv := range p.servers {
		srv.Close()
	}
	p.servers = nil
}

// cappedBuffer captures up to limit bytes and counts the rest as truncated.
type cappedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (c *cappedBuffer) Write(b []byte) (int, error) {
	room := c.limit - c.buf.Len()
	if room > 0 {
		if len(b) <= room {
			c.buf.Write(b)
		} else {
			c.buf.Write(b[:room])
			c.truncated = true
		}
	} else if len(b) > 0 {
		c.truncated = true
	}
	return len(b), nil
}

// hopByHopHeaders must not be forwarded (RFC 9110 §7.6.1).
var hopByHopHeaders = []string{
	"Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization",
	"Te", "Trailer", "Transfer-Encoding", "Upgrade",
}

func copyHeaders(dst, src http.Header) {
	for k, vs := range src {
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
	for _, k := range hopByHopHeaders {
		dst.Del(k)
	}
}

func flatHeaders(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for k, vs := range h {
		out[strings.ToLower(k)] = strings.Join(vs, ", ")
	}
	return out
}

func (p *Proxy) handler(t Target) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		corsPassthrough := false
		if len(t.Routes) > 0 {
			if route, ok := t.matchRoute(r.URL.Path); ok {
				corsPassthrough = route.CORSPassthrough
			}
		}
		if t.CORS != nil && !corsPassthrough {
			if h, ok := t.CORS.headers(r.Header.Get("Origin")); ok {
				copyHeaders(w.Header(), h)
				if isPreflight(r) {
					p.rec.Record(trace.Hop{
						To:        t.Name,
						Method:    r.Method,
						Path:      r.URL.RequestURI(),
						Status:    http.StatusNoContent,
						Preflight: true,
						T:         trace.Timings{Start: time.Now()},
						Req:       trace.Payload{Headers: flatHeaders(r.Header)},
						Resp:      trace.Payload{Headers: flatHeaders(h)},
					})
					w.WriteHeader(http.StatusNoContent)
					return
				}
			}
		}

		start := time.Now()

		// Trace context: parse (or mint), then advance one span for this hop.
		customTraceID := ""
		if p.TraceHeader != "" {
			customTraceID = r.Header.Get(p.TraceHeader)
		}
		ctx, incomingSpan := trace.ResolveInbound(r.Header.Get("traceparent"), r.Header.Get("baggage"), customTraceID)
		hopCtx := ctx.Child()
		for k, v := range t.InjectBaggage {
			hopCtx.Baggage[k] = v
		}
		hopCtx.EnsureCorrelationID()
		// Downstream calls made by this service will carry hopCtx's span as
		// parent — claim it so the next hop can name this service as caller.
		p.rec.ClaimSpan(hopCtx.SpanID, t.Name)
		// Claim trace->session at request START: nested hops are RECORDED
		// inner-first, but they always start outer-first, so gap detection
		// can trust this ordering.
		p.rec.ClaimTrace(hopCtx.TraceID, hopCtx.Session())

		from := p.rec.SpanOwner(incomingSpan)
		attribution := ""
		switch {
		case from != "":
			// real, trace-derived — leave attribution unset
		case p.declaredCaller(r) != "":
			from = p.declaredCaller(r)
			attribution = "declared"
		case len(t.CalledBy) > 0:
			from = strings.Join(t.CalledBy, "|")
			attribution = "inferred"
		}

		hop := trace.Hop{
			TraceID:       hopCtx.TraceID,
			SpanID:        hopCtx.SpanID,
			ParentSpanID:  hopCtx.ParentSpanID,
			CorrelationID: hopCtx.CorrelationID(),
			Session:       hopCtx.Session(),
			From:          from,
			Attribution:   attribution,
			Client:        p.clientIdentity(r.Header),
			To:            t.Name,
			Method:        r.Method,
			Path:          r.URL.RequestURI(),
			T:             trace.Timings{Start: start},
		}

		// Artificial latency runs before the upstream clock starts.
		forwardStart := start
		if p.Latency != nil {
			if delay := p.Latency.DelayFor(t.Name, r.URL.Path); delay > 0 {
				hop.InjectedDelayMs = float64(delay) / float64(time.Millisecond)
				select {
				case <-time.After(delay):
				case <-r.Context().Done():
				}
				forwardStart = time.Now()
			}
		}

		// Capture the request body without buffering the full stream.
		reqCap := &cappedBuffer{limit: CaptureLimit}
		var reqBody io.Reader = http.NoBody
		if r.Body != nil {
			reqBody = io.TeeReader(r.Body, reqCap)
		}

		upstream, forwardPath := t.Upstream, r.URL.RequestURI()
		if len(t.Routes) > 0 {
			up, fwd, ok := t.resolve(r.URL.Path)
			if !ok {
				hop.Status, hop.Err = http.StatusNotFound, "no route for "+r.URL.Path
				hop.Req.Headers = flatHeaders(r.Header)
				hop.T.DoneMs = msSince(forwardStart)
				p.rec.Record(hop)
				http.Error(w, hop.Err, http.StatusNotFound)
				return
			}
			upstream = up
			forwardPath = fwd
			if r.URL.RawQuery != "" {
				forwardPath += "?" + r.URL.RawQuery
			}
		}

		upReq, err := http.NewRequestWithContext(r.Context(), r.Method, upstream+forwardPath, reqBody)
		if err != nil {
			hop.Status, hop.Err = http.StatusBadGateway, err.Error()
			hop.T.DoneMs = msSince(forwardStart)
			p.rec.Record(hop)
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		copyHeaders(upReq.Header, r.Header)
		upReq.Header.Set("traceparent", hopCtx.Traceparent())
		if bh := hopCtx.BaggageHeader(); bh != "" {
			upReq.Header.Set("baggage", bh)
		}
		upReq.ContentLength = r.ContentLength
		upReq.Host = r.Host

		resp, err := p.transport.RoundTrip(upReq)
		if err != nil {
			hop.Status, hop.Err = http.StatusBadGateway, err.Error()
			hop.Req.Headers = flatHeaders(r.Header)
			hop.Req.Body, hop.Req.Truncated = reqCap.buf.String(), reqCap.truncated
			hop.T.DoneMs = msSince(forwardStart)
			p.rec.Record(hop)
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		hop.T.FirstByteMs = msSince(forwardStart)

		// Relay the response while capturing a capped copy.
		copyHeaders(w.Header(), resp.Header)
		w.WriteHeader(resp.StatusCode)
		respCap := &cappedBuffer{limit: CaptureLimit}
		_, copyErr := io.Copy(w, io.TeeReader(resp.Body, respCap))

		hop.Status = resp.StatusCode
		hop.Req.Headers = flatHeaders(r.Header)
		hop.Req.Body, hop.Req.Truncated = reqCap.buf.String(), reqCap.truncated
		hop.Resp.Headers = flatHeaders(resp.Header)
		hop.Resp.Body, hop.Resp.Truncated = respCap.buf.String(), respCap.truncated
		hop.T.DoneMs = msSince(forwardStart)
		if copyErr != nil {
			hop.Err = copyErr.Error()
		}
		p.rec.Record(hop)
	})
}

func msSince(t time.Time) float64 {
	return float64(time.Since(t)) / float64(time.Millisecond)
}
