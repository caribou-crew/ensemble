package server

import (
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/caribou-crew/ensemble/core/trace"
)

// ObservationScope describes this read, not a certification of capture. The
// ring can evict old hops or contain a still-running trace even with zero loss
// counters. No read from this surface claims complete historical coverage.
type ObservationScope struct {
	Source        string   `json:"source"`
	Completeness  string   `json:"completeness"`
	RetainedHops  int      `json:"retainedHops"`
	OldestSeq     uint64   `json:"oldestSeq"`
	NewestSeq     uint64   `json:"newestSeq"`
	Matched       int      `json:"matched"`
	Returned      int      `json:"returned"`
	Limit         int      `json:"limit"`
	Truncated     bool     `json:"truncated"`
	DroppedWrites uint64   `json:"droppedWrites"`
	WriteErrors   uint64   `json:"writeErrors"`
	Notes         []string `json:"notes"`
}

// ObservedHop is an API projection of core/trace.Hop, never a second capture
// schema. Excluding payloads and raw Err text keeps agent context compact.
type ObservedHop struct {
	Seq             uint64    `json:"seq"`
	TraceID         string    `json:"traceId"`
	SpanID          string    `json:"spanId"`
	ParentSpanID    string    `json:"parentSpanId,omitempty"`
	From            string    `json:"from,omitempty"`
	To              string    `json:"to"`
	Attribution     string    `json:"attribution,omitempty"`
	Method          string    `json:"method"`
	Path            string    `json:"path"`
	Status          int       `json:"status"`
	StartedAt       time.Time `json:"startedAt"`
	DurationMs      float64   `json:"durationMs"`
	InjectedDelayMs float64   `json:"injectedDelayMs"`
	HasError        bool      `json:"hasError"`
	Quality         []string  `json:"quality"`
	TextTruncated   bool      `json:"textTruncated"`
}

type ObservationFinding struct {
	Code      string   `json:"code"`
	Message   string   `json:"message"`
	Count     int      `json:"count"`
	HopSeqs   []uint64 `json:"hopSeqs"`
	Truncated bool     `json:"truncated"`
}

var observationMessages = map[string]string{
	"trace-unobserved":     "No hop for this trace is retained in the live ring; this does not prove the request never ran.",
	"parent-unobserved":    "The referenced parent span is absent from this trace's retained hops; it may be external or outside the ring.",
	"http-error":           "The proxy observed an HTTP error status. This identifies an outcome, not its cause.",
	"hop-error":            "The recorded hop carries an error; raw error text is omitted from this summary.",
	"stream-incomplete":    "A streaming response has not recorded its completion.",
	"redaction-failed":     "Capture dropped bodies after a redaction failure; the evidence is degraded.",
	"body-truncated":       "At least one captured payload was truncated.",
	"unsupported-protocol": "The proxy refused a protocol it cannot capture.",
	"context-unavailable":  "A hop lacks a trace or span identifier; trace linkage cannot be established from it.",
	"timing-unavailable":   "A completed duration is unavailable or invalid; zero is not a measured zero duration.",
	"attribution-declared": "Caller identity was declared by a header rather than linked through an observed parent span.",
	"attribution-inferred": "Caller identity was inferred from configuration rather than observed trace context.",
}

func (s *server) observationSnapshot(w http.ResponseWriter, limit int) ([]trace.Hop, ObservationScope, bool) {
	if s.Rec == nil {
		writeErr(w, http.StatusServiceUnavailable, "recorder is unavailable")
		return nil, ObservationScope{}, false
	}
	hops := s.Rec.Snapshot()
	scope := ObservationScope{Source: "live-ring", Completeness: "unknown", RetainedHops: len(hops), Limit: limit,
		DroppedWrites: s.Rec.DroppedWrites(), WriteErrors: s.Rec.WriteErrors(),
		Notes: []string{"Only hops currently retained in memory are searched; historical coverage and capture completeness are unknown.", "Durations are inclusive per-hop timings and must not be summed as end-to-end latency."}}
	for _, h := range hops {
		if scope.OldestSeq == 0 || h.Seq < scope.OldestSeq {
			scope.OldestSeq = h.Seq
		}
		scope.NewestSeq = max(scope.NewestSeq, h.Seq)
	}
	if scope.DroppedWrites > 0 || scope.WriteErrors > 0 {
		scope.Completeness = "degraded"
		scope.Notes = append(scope.Notes, "Recorder persistence loss counters are nonzero; these are lifetime counters, not proof of which trace lost disk evidence.")
	}
	return hops, scope, true
}

func observationQuery(r *http.Request, defaultLimit, maxLimit int, allowed ...string) (url.Values, int, error) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, 0, fmt.Errorf("invalid query: %w", err)
	}
	keys := map[string]bool{"limit": true}
	for _, key := range allowed {
		keys[key] = true
	}
	for key, values := range q {
		if !keys[key] || len(values) != 1 {
			return nil, 0, fmt.Errorf("unknown or repeated query parameter %q", key)
		}
		if len(values[0]) > 512 {
			return nil, 0, fmt.Errorf("query parameter %q is too long", key)
		}
	}
	limit := defaultLimit
	if q.Has("limit") {
		limit, err = strconv.Atoi(q.Get("limit"))
		if err != nil || limit < 1 || limit > maxLimit {
			return nil, 0, fmt.Errorf("limit must be between 1 and %d", maxLimit)
		}
	}
	return q, limit, nil
}

func (s *server) handleObservationRequests(w http.ResponseWriter, r *http.Request) {
	q, limit, err := observationQuery(r, 20, 100, "service", "path", "errorsOnly", "minDurationMs")
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	var errorsOnly bool
	if q.Has("errorsOnly") {
		errorsOnly, err = strconv.ParseBool(q.Get("errorsOnly"))
		if err != nil {
			writeErr(w, 400, "errorsOnly must be a boolean")
			return
		}
	}
	var duration float64
	if q.Has("minDurationMs") {
		duration, err = strconv.ParseFloat(q.Get("minDurationMs"), 64)
		if err != nil || !validObservationNumber(duration) || duration < 0 {
			writeErr(w, 400, "minDurationMs must be finite and nonnegative")
			return
		}
	}
	all, scope, ok := s.observationSnapshot(w, limit)
	if !ok {
		return
	}
	matches := make([]trace.Hop, 0)
	for _, h := range all {
		if service := q.Get("service"); service != "" && h.From != service && h.To != service {
			continue
		}
		if path := q.Get("path"); path != "" && !strings.Contains(h.Path, path) {
			continue
		}
		if errorsOnly && h.Status < 400 && h.Err == "" {
			continue
		}
		if q.Has("minDurationMs") && (!validObservationNumber(h.T.DoneMs) || h.T.DoneMs < duration || h.Streaming && h.T.DoneMs <= 0) {
			continue
		}
		matches = append(matches, h)
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].Seq > matches[j].Seq })
	scope.Matched = len(matches)
	degradeObservationScope(&scope, matches)
	if len(matches) > limit {
		matches = matches[:limit]
		scope.Truncated = true
	}
	hops := projectObservedHops(matches)
	scope.Returned = len(hops)
	writeJSON(w, 200, struct {
		Hops  []ObservedHop    `json:"hops"`
		Scope ObservationScope `json:"scope"`
	}{hops, scope})
}

func (s *server) handleObservationTrace(w http.ResponseWriter, r *http.Request) {
	_, limit, err := observationQuery(r, 50, 200)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	id := r.PathValue("traceId")
	if strings.TrimSpace(id) == "" || len(id) > 256 {
		writeErr(w, 400, "traceId must contain between 1 and 256 bytes")
		return
	}
	all, scope, ok := s.observationSnapshot(w, limit)
	if !ok {
		return
	}
	matches := hopsForTrace(all, id)
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].T.Start.Equal(matches[j].T.Start) {
			return matches[i].Seq < matches[j].Seq
		}
		return matches[i].T.Start.Before(matches[j].T.Start)
	})
	scope.Matched = len(matches)
	degradeObservationScope(&scope, matches)
	spans := make(map[string]bool, len(matches))
	for _, h := range matches {
		if h.SpanID != "" {
			spans[h.SpanID] = true
		}
	}
	findings := map[string]*ObservationFinding{}
	add := func(code string, seq uint64) {
		f := findings[code]
		if f == nil {
			f = &ObservationFinding{Code: code, Message: observationMessages[code], HopSeqs: []uint64{}}
			findings[code] = f
		}
		f.Count++
		if seq > 0 {
			if len(f.HopSeqs) < min(limit, 10) {
				f.HopSeqs = append(f.HopSeqs, seq)
			} else {
				f.Truncated = true
			}
		}
	}
	var slowest uint64
	var longest float64
	for _, h := range matches {
		if validObservationNumber(h.T.DoneMs) && h.T.DoneMs > longest {
			slowest, longest = h.Seq, h.T.DoneMs
		}
		if h.ParentSpanID != "" && !spans[h.ParentSpanID] {
			add("parent-unobserved", h.Seq)
		}
		if h.Status >= 400 {
			add("http-error", h.Seq)
		}
		if h.Err != "" {
			add("hop-error", h.Seq)
		}
		for _, quality := range observedQuality(h) {
			add(quality, h.Seq)
		}
	}
	if len(matches) == 0 {
		add("trace-unobserved", 0)
	}
	ordered := make([]ObservationFinding, 0, len(findings))
	for _, finding := range findings {
		ordered = append(ordered, *finding)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Code < ordered[j].Code })
	observed := len(matches)
	if len(matches) > limit {
		matches = matches[:limit]
		scope.Truncated = true
	}
	hops := projectObservedHops(matches)
	scope.Returned = len(hops)
	writeJSON(w, 200, struct {
		TraceID       string               `json:"traceId"`
		ObservedHops  int                  `json:"observedHops"`
		Hops          []ObservedHop        `json:"hops"`
		SlowestHopSeq uint64               `json:"slowestHopSeq,omitempty"`
		Findings      []ObservationFinding `json:"findings"`
		Scope         ObservationScope     `json:"scope"`
	}{id, observed, hops, slowest, ordered, scope})
}

func validObservationNumber(n float64) bool { return !math.IsNaN(n) && !math.IsInf(n, 0) }

func observedQuality(h trace.Hop) []string {
	out := []string{}
	if h.Streaming && h.T.DoneMs <= 0 {
		out = append(out, "stream-incomplete")
	}
	if trace.HasRedactionFailure(h) {
		out = append(out, "redaction-failed")
	}
	if h.Req.Truncated || h.Resp.Truncated {
		out = append(out, "body-truncated")
	}
	if h.Unsupported != "" {
		out = append(out, "unsupported-protocol")
	}
	if h.TraceID == "" || h.SpanID == "" {
		out = append(out, "context-unavailable")
	}
	if !validObservationNumber(h.T.DoneMs) || h.T.DoneMs <= 0 {
		out = append(out, "timing-unavailable")
	}
	if h.Attribution == "declared" {
		out = append(out, "attribution-declared")
	}
	if h.Attribution == "inferred" {
		out = append(out, "attribution-inferred")
	}
	return out
}

func degradeObservationScope(scope *ObservationScope, hops []trace.Hop) {
	for _, h := range hops {
		if trace.HasRedactionFailure(h) || h.Unsupported != "" || h.Req.Truncated || h.Resp.Truncated {
			scope.Completeness = "degraded"
			return
		}
	}
}

func projectObservedHops(hops []trace.Hop) []ObservedHop {
	out := make([]ObservedHop, 0, len(hops))
	for _, h := range hops {
		truncated := false
		clip := func(s string, limit int) string {
			if len(s) <= limit {
				return s
			}
			truncated = true
			for limit > 0 && !utf8.RuneStart(s[limit]) {
				limit--
			}
			return s[:limit] + "…"
		}
		duration, delay := h.T.DoneMs, h.InjectedDelayMs
		if !validObservationNumber(duration) || duration < 0 {
			duration = 0
		}
		if !validObservationNumber(delay) || delay < 0 {
			delay = 0
		}
		row := ObservedHop{Seq: h.Seq, TraceID: clip(h.TraceID, 256), SpanID: clip(h.SpanID, 128), ParentSpanID: clip(h.ParentSpanID, 128),
			From: clip(h.From, 128), To: clip(h.To, 128), Attribution: clip(h.Attribution, 128), Method: clip(h.Method, 32), Path: clip(h.Path, 512),
			Status: h.Status, StartedAt: h.T.Start, DurationMs: duration, InjectedDelayMs: delay, HasError: h.Err != "" || h.Status >= 400, Quality: observedQuality(h)}
		row.TextTruncated = truncated
		out = append(out, row)
	}
	return out
}
