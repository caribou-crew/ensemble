// Package stub serves config-defined canned responses for dependencies that
// can't run locally — AWS-only capabilities, crypto backends, third-party
// analytics. Every stub call is recorded as a hop, so stubbed traffic shows
// up in the same traces as real traffic.
package stub

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/template"
	"time"

	"github.com/caribou-crew/ensemble/core/proxy"
	"github.com/caribou-crew/ensemble/core/trace"
)

// Match selects requests for a route. An empty Method matches any method.
// A Path ending in "/*" matches that prefix (respecting the slash
// boundary); otherwise the match is exact.
type Match struct {
	Method string `json:"method,omitempty"`
	Path   string `json:"path"`
}

// Respond is the canned answer. Body and BodyFile are mutually exclusive;
// Template enables Go text/template rendering with the request in scope.
type Respond struct {
	Status   int               `json:"status"`
	Headers  map[string]string `json:"headers,omitempty"`
	Body     string            `json:"body,omitempty"`
	BodyFile string            `json:"body_file,omitempty"`
	Template bool              `json:"template,omitempty"`
}

// Route pairs a matcher with its response. First matching route wins.
type Route struct {
	Match   Match   `json:"match"`
	Respond Respond `json:"respond"`
}

// TemplateData is what a templated body can reference.
type TemplateData struct {
	Method string
	Path   string
	Query  url.Values
	Header http.Header
	Body   string
}

// Stub is one fake dependency listening on its own port.
type Stub struct {
	name   string
	routes []Route
	rec    *proxy.Recorder
	srv    *http.Server
	ln     net.Listener

	// TraceHeader mirrors core/proxy.Proxy.TraceHeader — a stack's own
	// correlation header, read as a fallback trace id when a request
	// carries no real traceparent. Empty (the default) disables it.
	TraceHeader string
}

func New(name string, routes []Route, rec *proxy.Recorder) *Stub {
	return &Stub{name: name, routes: routes, rec: rec}
}

// Serve starts listening. Returns the bound address (useful with ":0").
func (s *Stub) Serve(listen string) (string, error) {
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return "", fmt.Errorf("stub %s: %w", s.name, err)
	}
	s.srv = &http.Server{Handler: s}
	s.ln = ln
	go s.srv.Serve(ln)
	return ln.Addr().String(), nil
}

// Close stops the stub and releases its port before returning. Closing the
// raw listener directly (rather than relying solely on http.Server.Close)
// matters because Serve runs in its own goroutine — http.Server only
// registers a listener for tracking once that goroutine actually starts
// running, so Close alone can return before the OS socket is released,
// which breaks an immediate re-listen on the same port (as config-reconcile
// does when a stub's config changes without its port changing).
func (s *Stub) Close() {
	if s.ln != nil {
		s.ln.Close()
	}
	if s.srv != nil {
		s.srv.Close()
	}
}

func (m Match) matches(r *http.Request) bool {
	if m.Method != "" && !strings.EqualFold(m.Method, r.Method) {
		return false
	}
	if prefix, ok := strings.CutSuffix(m.Path, "/*"); ok {
		return r.URL.Path == prefix || strings.HasPrefix(r.URL.Path, prefix+"/")
	}
	return r.URL.Path == m.Path
}

func (s *Stub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	body, _ := io.ReadAll(r.Body)

	status, respHeaders, respBody := s.respond(r, string(body))

	for k, v := range respHeaders {
		w.Header().Set(k, v)
	}
	w.WriteHeader(status)
	io.WriteString(w, respBody)

	if s.rec != nil {
		customTraceID := ""
		if s.TraceHeader != "" {
			customTraceID = r.Header.Get(s.TraceHeader)
		}
		ctx, incomingSpan := trace.ResolveInbound(r.Header.Get("traceparent"), r.Header.Get("baggage"), customTraceID)
		hopCtx := ctx.Child()
		hopCtx.EnsureCorrelationID()
		// Cap what's *captured* into the hop at the same limit core/proxy
		// caps at, independent of the Redactor's own (often-disabled) cap —
		// runUp turns that off on the assumption capping already happened
		// upstream, which was only true for the proxy path, never this one.
		// What's actually sent to the client above is never touched.
		reqBody, reqTruncated := capBody(string(body), proxy.CaptureLimit)
		capturedResp, respTruncated := capBody(respBody, proxy.CaptureLimit)
		hop := trace.Hop{
			TraceID:       hopCtx.TraceID,
			SpanID:        hopCtx.SpanID,
			ParentSpanID:  hopCtx.ParentSpanID,
			CorrelationID: hopCtx.CorrelationID(),
			Session:       hopCtx.Session(),
			From:          s.rec.SpanOwner(incomingSpan),
			To:            s.name,
			Method:        r.Method,
			Path:          r.URL.RequestURI(),
			Status:        status,
			T:             trace.Timings{Start: start, DoneMs: float64(time.Since(start)) / float64(time.Millisecond)},
			Req:           trace.Payload{Headers: flatHeaders(r.Header), Body: reqBody, Truncated: reqTruncated},
			Resp:          trace.Payload{Headers: respHeaders, Body: capturedResp, Truncated: respTruncated},
		}
		s.rec.Record(hop)
	}
}

// capBody truncates s to limit bytes, reporting whether it did. limit <= 0
// disables the cap (kept for symmetry with trace.Redactor's maxBody, though
// stub always passes proxy.CaptureLimit).
func capBody(s string, limit int) (string, bool) {
	if limit <= 0 || len(s) <= limit {
		return s, false
	}
	return s[:limit], true
}

func (s *Stub) respond(r *http.Request, reqBody string) (int, map[string]string, string) {
	for _, route := range s.routes {
		if !route.Match.matches(r) {
			continue
		}
		status := route.Respond.Status
		if status == 0 {
			status = 200
		}
		body := route.Respond.Body
		if route.Respond.BodyFile != "" {
			b, err := os.ReadFile(route.Respond.BodyFile)
			if err != nil {
				return 500, nil, fmt.Sprintf(`{"error":"stub body file: %s"}`, err)
			}
			body = string(b)
		}
		if route.Respond.Template {
			tpl, err := template.New("body").Parse(body)
			if err != nil {
				return 500, nil, fmt.Sprintf(`{"error":"stub template: %s"}`, err)
			}
			var buf bytes.Buffer
			data := TemplateData{Method: r.Method, Path: r.URL.Path, Query: r.URL.Query(), Header: r.Header, Body: reqBody}
			if err := tpl.Execute(&buf, data); err != nil {
				return 500, nil, fmt.Sprintf(`{"error":"stub template: %s"}`, err)
			}
			body = buf.String()
		}
		return status, route.Respond.Headers, body
	}
	return 404, map[string]string{"content-type": "application/json"},
		fmt.Sprintf(`{"error":"no stub route matches %s %s"}`, r.Method, r.URL.Path)
}

func flatHeaders(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for k, vs := range h {
		out[strings.ToLower(k)] = strings.Join(vs, ", ")
	}
	return out
}
