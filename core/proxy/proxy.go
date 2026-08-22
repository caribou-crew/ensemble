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
}

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

	mu      sync.Mutex
	servers []*http.Server
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

// ServeStoppable is Serve with a per-listener stop, used for ephemeral
// listeners like session client-edge ports.
func (p *Proxy) ServeStoppable(t Target) (string, func(), error) {
	ln, err := net.Listen("tcp", t.Listen)
	if err != nil {
		return "", nil, fmt.Errorf("proxy %s: %w", t.Name, err)
	}
	srv := &http.Server{Handler: p.handler(t)}
	p.mu.Lock()
	p.servers = append(p.servers, srv)
	p.mu.Unlock()
	go srv.Serve(ln)
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
	return ln.Addr().String(), stop, nil
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
		if t.CORS != nil {
			if h, ok := t.CORS.headers(r.Header.Get("Origin")); ok {
				copyHeaders(w.Header(), h)
				if isPreflight(r) {
					w.WriteHeader(http.StatusNoContent)
					return
				}
			}
		}

		start := time.Now()

		// Trace context: parse (or mint), then advance one span for this hop.
		ctx := trace.ParseCtx(r.Header.Get("traceparent"), r.Header.Get("baggage"))
		incomingSpan := ""
		if r.Header.Get("traceparent") != "" {
			incomingSpan = ctx.SpanID
		}
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

		hop := trace.Hop{
			TraceID:       hopCtx.TraceID,
			SpanID:        hopCtx.SpanID,
			ParentSpanID:  hopCtx.ParentSpanID,
			CorrelationID: hopCtx.CorrelationID(),
			Session:       hopCtx.Session(),
			From:          p.rec.SpanOwner(incomingSpan),
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
