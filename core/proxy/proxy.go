package proxy

import (
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

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
	// Passthrough marks Upstream as a real remote environment rather than a
	// local process — it arms the read-only-by-default safety rail (see
	// AllowWrites) below, and rewrites the outbound Host header to match
	// Upstream (see handler) so a passthrough target behaves exactly like a
	// client pointed at that host directly, without the caller having to
	// know or send it. TLS (below) is what handles a passthrough target
	// that happens to need mTLS.
	Passthrough bool
	// AllowWrites opts a Passthrough target out of the read-only default.
	// Ignored when Passthrough is false.
	AllowWrites bool
	// TLS, when set, is the client certificate presented while dialing
	// Upstream — for a remote edge that requires mTLS. nil (the default)
	// uses Proxy's shared transport, which presents no client certificate
	// and otherwise behaves exactly like ordinary Go stdlib TLS (default
	// verification, default SNI from the upstream URL's host).
	TLS *tls.Certificate
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

// transportFor returns the transport a target's requests should dial
// through: the shared no-TLS transport for every ordinary target, or a
// dedicated transport presenting t.TLS's client certificate when set. Built
// once per Serve call (see handler), never per request.
func (p *Proxy) transportFor(t Target) *http.Transport {
	if t.TLS == nil {
		return p.transport
	}
	clone := p.transport.Clone()
	tlsCfg := &tls.Config{}
	if clone.TLSClientConfig != nil {
		tlsCfg = clone.TLSClientConfig.Clone()
	}
	tlsCfg.Certificates = []tls.Certificate{*t.TLS}
	clone.TLSClientConfig = tlsCfg
	return clone
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

// loopbackCompanion maps one well-known loopback literal to the other —
// "127.0.0.1" <-> "::1" — so BindLoopbackCompanion can best-effort bind
// the family a caller did not ask for, without any DNS resolution. ""
// means host is not one of these two literals: a hostname already gets
// both families through hostAddrs, and any other loopback-adjacent
// literal (127.0.0.2, say) has no defined companion.
func loopbackCompanion(host string) string {
	switch host {
	case "127.0.0.1":
		return "::1"
	case "::1":
		return "127.0.0.1"
	default:
		return ""
	}
}

// BindLoopbackCompanion best-effort binds the OTHER loopback address
// family on primary's own port — 127.0.0.1 gets a companion ::1, and
// vice versa — so a client that resolves "localhost" (or is otherwise
// pointed at this host) via either family still reaches a working
// listener. This is what fixes the common default case: a proxy or
// replay listener configured with a bare literal IP (no explicit
// hostname) has, until now, only ever answered the one family it happened
// to bind.
//
// Returns (nil, nil) — not an error — when primary's host is not one of
// the two well-known loopback literals: a hostname target already binds
// both families via ServeStoppable's own hostAddrs path, and this must
// not double-bind on top of that. A failed companion bind (no IPv6 stack,
// a same-port race) is also silent and non-fatal — exactly as
// hostAddrs' own best-effort ln6 already behaves — since the primary
// listener working is what every existing caller and advertised address
// already depends on.
//
// Exported for retrace/cmd/retrace's replay listener, which binds its own
// net.Listener directly rather than going through ServeStoppable/Target.
func BindLoopbackCompanion(primary net.Listener) (net.Listener, error) {
	host, port, err := net.SplitHostPort(primary.Addr().String())
	if err != nil {
		return nil, err
	}
	companionHost := loopbackCompanion(host)
	if companionHost == "" {
		return nil, nil
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(companionHost, port))
	if err != nil {
		return nil, nil
	}
	return ln, nil
}

// Listen binds the listener(s) for one capture-plane listen address, with
// this package's loopback enforcement: a hostname (anything net.ParseIP
// does not recognize) is resolved, MUST be loopback-only (see hostAddrs),
// and binds both loopback families where available; a literal IP binds
// exactly as given, with a best-effort loopback companion (see
// BindLoopbackCompanion). advertise is the address a client should be told
// — built from the CONFIGURED hostname string for a hostname, never the
// resolved address, so it reads back exactly as configured.
//
// Exported so core/stub binds through the same enforcement instead of a
// bare net.Listen — the stub records hops into the same Recorder, so a
// stub reachable from a LAN interface would be the same forged-capture
// hazard hostAddrs documents for the proxy.
func Listen(listen string) (lns []net.Listener, advertise string, err error) {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return nil, "", err
	}
	if host != "" && net.ParseIP(host) == nil {
		ln4, ln6, herr := hostAddrs(host, port)
		if herr != nil {
			return nil, "", herr
		}
		lns = append(lns, ln4)
		if ln6 != nil {
			lns = append(lns, ln6)
		}
		_, boundPort, _ := net.SplitHostPort(ln4.Addr().String())
		return lns, net.JoinHostPort(host, boundPort), nil
	}
	ln, lerr := net.Listen("tcp", listen)
	if lerr != nil {
		return nil, "", lerr
	}
	lns = append(lns, ln)
	if companion, cerr := BindLoopbackCompanion(ln); cerr == nil && companion != nil {
		lns = append(lns, companion)
	}
	return lns, ln.Addr().String(), nil
}

// ServerReadHeaderTimeout bounds how long a connected client may dawdle
// before sending its request headers — without it an idle socket holds a
// server goroutine forever (slowloris). Generous, because everything here
// is loopback: the point is reclaiming abandoned sockets, not policing
// latency. Exported so core/stub's server sets the same bound. A var, not
// a const, only so tests can shorten it — production never writes it.
var ServerReadHeaderTimeout = 10 * time.Second

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
	lns, advertise, err := Listen(t.Listen)
	if err != nil {
		return "", nil, fmt.Errorf("proxy %s: %w", t.Name, err)
	}

	srv := &http.Server{Handler: p.handler(t), ReadHeaderTimeout: ServerReadHeaderTimeout}
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
	// Resolved once per Serve call, not per request — matches Target's own
	// "wired once" lifecycle. A target with no TLS material keeps sharing
	// Proxy's one transport, unchanged from before passthrough existed.
	transport := p.transportFor(t)
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

		// Unsupported protocols are refused HERE — after the hop carries its
		// full trace context (so the refusal lands in the right session and
		// the verdict can degrade), before anything is forwarded. A silent
		// dead 101 or a garbled gRPC stream told the user nothing; a flagged
		// 501 at the first request tells them at the first request.
		if proto := unsupportedProtocol(r); proto != "" {
			hop.Status = http.StatusNotImplemented
			hop.Unsupported = proto
			hop.Req.Headers = flatHeaders(r.Header)
			hop.T.DoneMs = msSince(start)
			p.rec.Record(hop)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotImplemented)
			fmt.Fprintf(w, `{"error":"ensemble does not proxy %s — this request was refused, not forwarded","unsupported":%q}`+"\n", proto, proto)
			return
		}

		// Read-only-by-default safety rail: a passthrough target refuses
		// any write unless explicitly opted in. Refused HERE, same as an
		// unsupported protocol above — recorded as a hop, not silently
		// dropped, so a refusal is as visible as a forwarded call.
		if t.Passthrough && !t.AllowWrites && r.Method != http.MethodGet && r.Method != http.MethodHead {
			hop.Status = http.StatusBadGateway
			hop.Err = fmt.Sprintf("passthrough target %q is read-only by default; refused %s (set allow_writes: true to permit)", t.Name, r.Method)
			hop.Req.Headers = flatHeaders(r.Header)
			hop.T.DoneMs = msSince(start)
			p.rec.Record(hop)
			http.Error(w, hop.Err, http.StatusBadGateway)
			return
		}

		// Artificial latency runs before the upstream clock starts.
		forwardStart := start
		if p.Latency != nil {
			delayFn := p.Latency.DelayFor
			if t.Passthrough {
				delayFn = p.Latency.DelayForExact
			}
			if delay := delayFn(t.Name, r.URL.Path); delay > 0 {
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
		if t.Passthrough {
			// Standard reverse-proxy behavior: the Host header sent
			// upstream matches the configured upstream, not whatever the
			// caller happened to send — a passthrough target has a real
			// remote host of its own that the caller shouldn't need to
			// know. upReq.URL.Host (not a re-parse of upstream) since
			// NewRequestWithContext above already parsed it.
			upReq.Host = upReq.URL.Host
		}

		resp, err := transport.RoundTrip(upReq)
		if err != nil {
			hop.Status, hop.Err = http.StatusBadGateway, err.Error()
			hop.Req.Headers = flatHeaders(r.Header)
			setCapturedBody(&hop.Req, reqCap, r.Header.Get("Content-Type"))
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

		hop.Status = resp.StatusCode
		hop.Req.Headers = flatHeaders(r.Header)
		setCapturedBody(&hop.Req, reqCap, r.Header.Get("Content-Type"))
		hop.Resp.Headers = flatHeaders(resp.Header)
		// Every Set-Cookie value, in order — the flattened Headers join is
		// lossy for cookies specifically (see trace.Payload.SetCookies).
		if sc := resp.Header.Values("Set-Cookie"); len(sc) > 0 {
			hop.Resp.SetCookies = append([]string(nil), sc...)
		}

		if isStreamingResponse(resp) {
			// A streaming response is recorded NOW, at response-headers time
			// (Streaming true, no DoneMs, body so far empty), so an
			// hour-long SSE stream is visible in the traffic plane while
			// open, and finalized in place — same Seq — when it closes.
			// Writes are flushed through per-write so events reach the
			// client the moment the upstream sends them, not at stream end.
			hop.Streaming = true
			hop.Seq = p.rec.Record(hop).Seq
			var dst io.Writer = w
			if f, ok := w.(http.Flusher); ok {
				dst = flushWriter{w: w, f: f}
			}
			_, copyErr := io.Copy(dst, io.TeeReader(resp.Body, respCap))
			setCapturedBody(&hop.Resp, respCap, resp.Header.Get("Content-Type"))
			hop.T.DoneMs = msSince(forwardStart)
			if copyErr != nil {
				hop.Err = copyErr.Error()
			}
			p.rec.Update(hop)
			return
		}

		_, copyErr := io.Copy(w, io.TeeReader(resp.Body, respCap))
		setCapturedBody(&hop.Resp, respCap, resp.Header.Get("Content-Type"))
		hop.T.DoneMs = msSince(forwardStart)
		if copyErr != nil {
			hop.Err = copyErr.Error()
		}
		p.rec.Record(hop)
	})
}

// unsupportedProtocol classifies a request the proxy cannot forward
// faithfully: a WebSocket upgrade (an Upgrade header, or an Upgrade token
// in Connection — either one means the client expects a protocol switch
// this proxy cannot relay) or a gRPC call (Content-Type application/grpc
// and its +proto/+json subtypes, which needs trailers and HTTP/2 framing
// the tee-capture path would destroy). "" means an ordinary HTTP request.
func unsupportedProtocol(r *http.Request) string {
	if r.Header.Get("Upgrade") != "" {
		return "websocket"
	}
	for _, tok := range strings.Split(r.Header.Get("Connection"), ",") {
		if strings.EqualFold(strings.TrimSpace(tok), "upgrade") {
			return "websocket"
		}
	}
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/grpc") {
		return "grpc"
	}
	return ""
}

// isStreamingResponse identifies a response whose bytes should reach the
// client as they arrive: SSE by content type, or a chunked response with
// no declared length (an upstream that doesn't know its own end is
// streaming by construction). A plain response with Content-Length keeps
// the buffered relay — one flush at the end is cheaper and identical to
// the client.
func isStreamingResponse(resp *http.Response) bool {
	if strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		return true
	}
	for _, te := range resp.TransferEncoding {
		if strings.EqualFold(te, "chunked") {
			return resp.ContentLength < 0
		}
	}
	return false
}

// flushWriter flushes after every write, so a streaming upstream's events
// cross the proxy the moment they are written instead of pooling in the
// response buffer until the stream ends.
type flushWriter struct {
	w io.Writer
	f http.Flusher
}

func (fw flushWriter) Write(b []byte) (int, error) {
	n, err := fw.w.Write(b)
	fw.f.Flush()
	return n, err
}

// knownBinaryType reports content types whose bodies are bytes rather than
// text — the families the protocol-guardrails spec names. A body of one of
// these is stored base64 (Payload.BodyB64) even when it happens to be
// valid UTF-8, so a PNG that accidentally decodes never gets a lossy
// string round-trip.
func knownBinaryType(contentType string) bool {
	ct := strings.ToLower(strings.TrimSpace(contentType))
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	switch {
	case strings.HasPrefix(ct, "image/"), strings.HasPrefix(ct, "font/"),
		strings.HasPrefix(ct, "application/grpc"):
		return true
	case ct == "application/octet-stream", ct == "application/pdf",
		ct == "application/protobuf":
		return true
	}
	return false
}

// setCapturedBody stores one captured payload body, choosing Body or
// BodyB64: a known-binary content type or bytes that are not valid UTF-8
// go base64 (lossless — a Go string written through encoding/json replaces
// every invalid byte with U+FFFD), everything else stays the raw text it
// always was. The cap already happened in the cappedBuffer, so Truncated
// describes the raw bytes regardless of which field holds them.
func setCapturedBody(p *trace.Payload, buf *cappedBuffer, contentType string) {
	p.Truncated = buf.truncated
	data := buf.buf.Bytes()
	if len(data) == 0 {
		return
	}
	if knownBinaryType(contentType) || !utf8.Valid(data) {
		p.BodyB64 = base64.StdEncoding.EncodeToString(data)
		return
	}
	p.Body = string(data)
}

func msSince(t time.Time) float64 {
	return float64(time.Since(t)) / float64(time.Millisecond)
}
