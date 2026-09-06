package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/caribou-crew/ensemble/core/proxy"
	"github.com/caribou-crew/ensemble/core/trace"
)

const (
	doctorDefaultTimeout = 5 * time.Second
	doctorMaximumTimeout = 30 * time.Second
	doctorDrainWindow    = 100 * time.Millisecond
	doctorEdgeTarget     = "ensemble-doctor"
	doctorHopLimit       = 200
	doctorReasonLimit    = 20
	doctorTargetLimit    = 128
	doctorPathLimit      = 2048
	doctorExpectLimit    = 64
)

// DoctorVerdict is the outcome of one explicit stack probe.
type DoctorVerdict string

const (
	DoctorPass         DoctorVerdict = "pass"
	DoctorFail         DoctorVerdict = "fail"
	DoctorInconclusive DoctorVerdict = "inconclusive"
)

// DoctorRequest is the JSON body accepted by POST /api/doctor.
type DoctorRequest struct {
	Target    string   `json:"target"`
	Path      string   `json:"path"`
	Expect    []string `json:"expect,omitempty"`
	TimeoutMs int      `json:"timeoutMs,omitempty"`
}

// DoctorPropagation records the context properties the captured hops prove.
type DoctorPropagation struct {
	TraceContext   bool     `json:"traceContext"`
	SessionContext bool     `json:"sessionContext"`
	Linked         bool     `json:"linked"`
	Gaps           []string `json:"gaps,omitempty"`
}

// DoctorCapture records loss and quality evidence scoped to the probe.
type DoctorCapture struct {
	Verdict       trace.Verdict `json:"verdict"`
	Reasons       []string      `json:"reasons,omitempty"`
	DroppedHops   uint64        `json:"droppedHops"`
	DroppedWrites uint64        `json:"droppedWrites"`
	WriteErrors   uint64        `json:"writeErrors"`
}

// DoctorResponse is the typed result shared by the REST API and CLI.
type DoctorResponse struct {
	Verdict         DoctorVerdict     `json:"verdict"`
	Target          string            `json:"target"`
	Path            string            `json:"path"`
	HTTPStatus      int               `json:"httpStatus,omitempty"`
	DurationMs      float64           `json:"durationMs"`
	TraceID         string            `json:"traceId"`
	SessionID       string            `json:"sessionId"`
	Hops            []ObservedHop     `json:"hops"`
	HopsTruncated   bool              `json:"hopsTruncated"`
	ObservedTargets []string          `json:"observedTargets"`
	MissingExpected []string          `json:"missingExpected"`
	Propagation     DoctorPropagation `json:"propagation"`
	Capture         DoctorCapture     `json:"capture"`
	Reasons         []string          `json:"reasons,omitempty"`
	Limitations     []string          `json:"limitations,omitempty"`
	ProbeError      string            `json:"probeError,omitempty"`
}

func (s *server) handleDoctor(w http.ResponseWriter, r *http.Request) {
	var in DoctorRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if err := requireJSONEOF(dec); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}

	timeout, err := validateDoctorRequest(s, in)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.Rec == nil || s.Sessions == nil {
		writeErr(w, http.StatusServiceUnavailable, "doctor requires the recorder and session manager")
		return
	}
	port, ok := doctorTargetPort(s, in.Target)
	if !ok {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("target %q is not a configured proxy", in.Target))
		return
	}

	root := trace.NewCtx()
	sessionID := "doctor-" + root.TraceID
	root.Baggage[trace.BaggageSession] = sessionID
	baselineDropped, baselineErrors := s.Rec.DroppedWrites(), s.Rec.WriteErrors()
	ses, err := s.Sessions.Start(sessionID, doctorEdgeTarget, fmt.Sprintf("http://127.0.0.1:%d", port), "", 0)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "start doctor session: "+err.Error())
		return
	}
	ended := false
	finish := func() *proxy.Session {
		if ended {
			return ses
		}
		ended = true
		if final := s.Sessions.End(sessionID); final != nil {
			ses = final
		}
		return ses
	}
	defer finish()

	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	probeReq, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+ses.EdgeAddr+in.Path, nil)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid path: "+err.Error())
		return
	}
	probeReq.Header.Set("traceparent", root.Traceparent())
	probeReq.Header.Set("baggage", root.BaggageHeader())
	client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}}

	started := time.Now()
	resp, probeErr := client.Do(probeReq)
	httpStatus := 0
	if resp != nil {
		httpStatus = resp.StatusCode
		_, drainErr := io.Copy(io.Discard, resp.Body)
		if drainErr != nil && !errors.Is(drainErr, io.EOF) {
			probeErr = fmt.Errorf("read probe response: %w", drainErr)
		}
		_ = resp.Body.Close()
	}
	if ctx.Err() == nil {
		drainDoctorSession(r.Context(), doctorDrainWindow)
	}
	finish()

	result := classifyDoctor(in, root, sessionID, httpStatus, time.Since(started), probeErr, ses,
		counterDelta(s.Rec.DroppedWrites(), baselineDropped), counterDelta(s.Rec.WriteErrors(), baselineErrors))
	writeJSON(w, http.StatusOK, result)
}

func requireJSONEOF(dec *json.Decoder) error {
	var extra any
	err := dec.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("multiple JSON values")
	}
	return err
}

func validateDoctorRequest(s *server, in DoctorRequest) (time.Duration, error) {
	if strings.TrimSpace(in.Target) == "" {
		return 0, errors.New("target is required")
	}
	if len(in.Target) > doctorTargetLimit {
		return 0, fmt.Errorf("target must not exceed %d bytes", doctorTargetLimit)
	}
	if len(in.Path) > doctorPathLimit {
		return 0, fmt.Errorf("path must not exceed %d bytes", doctorPathLimit)
	}
	if err := validateOriginFormPath(in.Path); err != nil {
		return 0, err
	}
	if in.TimeoutMs < 0 {
		return 0, errors.New("timeoutMs must be positive")
	}
	if in.TimeoutMs > int(doctorMaximumTimeout/time.Millisecond) {
		return 0, fmt.Errorf("timeoutMs must not exceed %d", doctorMaximumTimeout.Milliseconds())
	}
	timeout := doctorDefaultTimeout
	if in.TimeoutMs > 0 {
		timeout = time.Duration(in.TimeoutMs) * time.Millisecond
	}
	if s.Cfg == nil {
		return 0, errors.New("stack configuration is unavailable")
	}
	seen := map[string]bool{}
	if len(in.Expect) > doctorExpectLimit {
		return 0, fmt.Errorf("expect must not contain more than %d targets", doctorExpectLimit)
	}
	for _, name := range in.Expect {
		if name == "" {
			return 0, errors.New("expect contains an empty target")
		}
		if len(name) > doctorTargetLimit {
			return 0, fmt.Errorf("expected target names must not exceed %d bytes", doctorTargetLimit)
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		if !doctorConfiguredName(s, name) {
			return 0, fmt.Errorf("expected target %q is not configured", name)
		}
	}
	return timeout, nil
}

func validateOriginFormPath(path string) error {
	if path == "" {
		return errors.New("path is required")
	}
	if strings.Contains(path, "#") {
		return errors.New("path must be a valid origin-form request target")
	}
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return errors.New("path must be an origin-form request target beginning with /")
	}
	u, err := url.ParseRequestURI(path)
	if err != nil || u.IsAbs() || u.Host != "" || u.Fragment != "" {
		return errors.New("path must be a valid origin-form request target")
	}
	return nil
}

func doctorConfiguredName(s *server, name string) bool {
	if _, ok := s.Cfg.Services[name]; ok {
		return true
	}
	if _, ok := s.Cfg.Gateways[name]; ok {
		return true
	}
	return false
}

func doctorTargetPort(s *server, target string) (int, bool) {
	if svc, ok := s.Cfg.Services[target]; ok {
		return svc.Proxy, svc.Proxy > 0
	}
	if gw, ok := s.Cfg.Gateways[target]; ok {
		return gw.Port, gw.Port > 0
	}
	return 0, false
}

func drainDoctorSession(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func counterDelta(after, before uint64) uint64 {
	if after < before {
		return after
	}
	return after - before
}

func classifyDoctor(in DoctorRequest, root trace.Ctx, sessionID string, httpStatus int, elapsed time.Duration, probeErr error, ses *proxy.Session, droppedWrites, writeErrors uint64) DoctorResponse {
	hops := ses.Hops()
	captureVerdict, captureReasons := ses.Verdict()
	out := DoctorResponse{
		Verdict:    DoctorPass,
		Target:     in.Target,
		Path:       doctorDisplayPath(in.Path),
		HTTPStatus: httpStatus,
		DurationMs: float64(elapsed) / float64(time.Millisecond),
		TraceID:    root.TraceID,
		SessionID:  sessionID,
		Capture: DoctorCapture{
			Verdict: captureVerdict, Reasons: boundedDoctorReasons(captureReasons), DroppedHops: ses.DroppedHops(),
			DroppedWrites: droppedWrites, WriteErrors: writeErrors,
		},
	}
	projected := projectObservedHops(hops)
	for i := range projected {
		projected[i].Path = doctorDisplayPath(projected[i].Path)
	}
	if len(projected) > doctorHopLimit {
		out.HopsTruncated = true
		projected = projected[:doctorHopLimit]
		out.Limitations = append(out.Limitations, fmt.Sprintf("hop evidence was limited to %d of %d attributed hops", doctorHopLimit, len(hops)))
	}
	out.Hops = projected
	if probeErr != nil {
		out.ProbeError = doctorProbeError(probeErr)
	}

	spanIDs := make(map[string]bool, len(hops))
	observed := map[string]bool{}
	for _, h := range hops {
		if validDoctorSpanID(h.SpanID) {
			spanIDs[h.SpanID] = true
		}
		if h.To != doctorEdgeTarget {
			observed[h.To] = true
		}
	}
	for name := range observed {
		out.ObservedTargets = append(out.ObservedTargets, name)
	}
	sort.Strings(out.ObservedTargets)
	if len(out.ObservedTargets) > doctorHopLimit {
		out.Limitations = append(out.Limitations, fmt.Sprintf("observed target names were limited to %d", doctorHopLimit))
		out.ObservedTargets = out.ObservedTargets[:doctorHopLimit]
	}
	for i := range out.ObservedTargets {
		out.ObservedTargets[i] = doctorBoundedText(out.ObservedTargets[i], doctorTargetLimit)
	}
	for _, name := range in.Expect {
		if !observed[name] && !containsString(out.MissingExpected, name) {
			out.MissingExpected = append(out.MissingExpected, name)
		}
	}

	out.Propagation.TraceContext = len(hops) > 0
	out.Propagation.SessionContext = len(hops) > 0
	out.Propagation.Linked = len(hops) > 0
	completed := len(hops) > 0
	captureLimited := false
	hopFailed := false
	for _, h := range hops {
		if h.TraceID == "" || h.TraceID != root.TraceID || !validDoctorSpanID(h.SpanID) {
			out.Propagation.TraceContext = false
		}
		if h.Session == "" || h.Session != sessionID {
			out.Propagation.SessionContext = false
		}
		if h.ParentSpanID == "" || (h.ParentSpanID != root.SpanID && !spanIDs[h.ParentSpanID]) {
			out.Propagation.Linked = false
		}
		if h.Status == 0 || !validObservationNumber(h.T.DoneMs) || h.T.DoneMs <= 0 {
			completed = false
		}
		if trace.HasRedactionFailure(h) || h.Req.Truncated || h.Resp.Truncated || h.Unsupported != "" {
			captureLimited = true
		}
		if h.Status >= 400 || h.Err != "" {
			hopFailed = true
		}
	}
	for _, reason := range captureReasons {
		if strings.Contains(reason, "propagation gap") || strings.Contains(reason, "dropping trace headers") {
			out.Propagation.Gaps = append(out.Propagation.Gaps, reason)
		}
	}
	out.Propagation.Gaps = boundedDoctorReasons(out.Propagation.Gaps)

	if len(in.Expect) == 0 {
		out.Limitations = append(out.Limitations, "only the observed path was checked; configured dependencies were not assumed to execute")
	}

	switch {
	case len(hops) == 0:
		out.Verdict = DoctorInconclusive
		out.Reasons = append(out.Reasons, "the probe produced no attributable hops")
	case probeErr != nil:
		out.Verdict = DoctorFail
		out.Reasons = append(out.Reasons, "the probe request did not complete: "+doctorProbeError(probeErr))
	case httpStatus < 200 || httpStatus >= 300:
		out.Verdict = DoctorFail
		out.Reasons = append(out.Reasons, fmt.Sprintf("probe returned HTTP %d", httpStatus))
	}
	if len(hops) > 0 && !observed[in.Target] {
		out.Verdict = DoctorFail
		out.Reasons = append(out.Reasons, fmt.Sprintf("configured target %q was not observed", in.Target))
	}
	if len(out.MissingExpected) > 0 {
		out.Verdict = DoctorFail
		out.Reasons = append(out.Reasons, "expected targets were not observed: "+strings.Join(out.MissingExpected, ", "))
	}
	if hopFailed {
		out.Verdict = DoctorFail
		out.Reasons = append(out.Reasons, "one or more observed hops failed")
	}
	if len(hops) > 0 && (!out.Propagation.TraceContext || !out.Propagation.SessionContext || !out.Propagation.Linked) {
		out.Verdict = DoctorFail
		out.Reasons = append(out.Reasons, "trace or session context was missing or unlinked")
	}
	if out.Verdict == DoctorPass && (!completed || captureLimited || captureVerdict != trace.VerdictOK || ses.DroppedHops() > 0 || droppedWrites > 0 || writeErrors > 0) {
		out.Verdict = DoctorInconclusive
		switch {
		case !completed:
			out.Reasons = append(out.Reasons, "one or more observed hops did not complete")
		case captureLimited:
			out.Reasons = append(out.Reasons, "one or more observed hops has limited capture quality")
		case captureVerdict != trace.VerdictOK:
			out.Reasons = append(out.Reasons, "capture quality was "+string(captureVerdict))
		default:
			out.Reasons = append(out.Reasons, "capture loss was recorded during the probe")
		}
	}
	out.Reasons = clipDoctorStrings(out.Reasons, doctorReasonLimit, 512)
	out.Limitations = clipDoctorStrings(out.Limitations, doctorReasonLimit, 512)
	return out
}

func validDoctorSpanID(id string) bool {
	if len(id) != 16 || id == "0000000000000000" {
		return false
	}
	for _, c := range []byte(id) {
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func doctorDisplayPath(requestTarget string) string {
	if u, err := url.ParseRequestURI(requestTarget); err == nil && u.Path != "" {
		return u.EscapedPath()
	}
	if path, _, ok := strings.Cut(requestTarget, "?"); ok {
		return path
	}
	return requestTarget
}

func boundedDoctorReasons(reasons []string) []string {
	if len(reasons) > doctorReasonLimit {
		reasons = reasons[:doctorReasonLimit]
	}
	out := make([]string, len(reasons))
	for i, reason := range reasons {
		switch {
		case strings.Contains(reason, "propagation gap"), strings.Contains(reason, "unattributed traffic"):
		case strings.Contains(reason, "recorder dropped hops"):
			reason = "recorder dropped hops for the session subscriber"
		case strings.Contains(reason, "unsupported protocol"):
			reason = "an unsupported protocol limited capture quality"
		case strings.HasPrefix(reason, "hop "):
			reason = "redaction failed for an observed hop"
		default:
			reason = "capture quality was degraded"
		}
		out[i] = doctorBoundedText(reason, 512)
	}
	return out
}

func clipDoctorStrings(values []string, count, bytes int) []string {
	if len(values) > count {
		values = values[:count]
	}
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = doctorBoundedText(value, bytes)
	}
	return out
}

func doctorBoundedText(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	for limit > 0 && !utf8.RuneStart(value[limit]) {
		limit--
	}
	return value[:limit] + "…"
}

func doctorProbeError(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "probe timed out"
	case errors.Is(err, context.Canceled):
		return "probe canceled"
	default:
		return "probe transport failed"
	}
}

func containsString(values []string, value string) bool {
	for _, got := range values {
		if got == value {
			return true
		}
	}
	return false
}
