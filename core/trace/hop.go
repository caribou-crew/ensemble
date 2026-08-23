// Package trace defines the single hop/trace data model shared by ensemble
// (live telemetry) and retrace (recordings), plus W3C trace-context helpers.
// One schema, two consumers — recordings ARE the telemetry format.
package trace

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
)

// SchemaVersion identifies the hop record shape. Bump only with a migration
// path; readers must tolerate unknown fields for forward compatibility.
const SchemaVersion = "ensemble/1"

// Hop is one proxied request/response observed between two services.
type Hop struct {
	Schema        string `json:"schema"`
	Seq           uint64 `json:"seq"`
	TraceID       string `json:"traceId,omitempty"`
	SpanID        string `json:"spanId,omitempty"`
	ParentSpanID  string `json:"parentSpanId,omitempty"`
	CorrelationID string `json:"correlationId,omitempty"`
	Session       string `json:"session,omitempty"` // retrace-run id; "" = ambient traffic
	From          string `json:"from,omitempty"`
	// Attribution is "inferred" when From came from a config-declared
	// CalledBy hint (see core/proxy.Target) rather than real W3C
	// trace-context propagation — SpanOwner had nothing to look up, so the
	// proxy fell back to a guess. Empty means From (if set) is a real,
	// trace-derived fact.
	Attribution string  `json:"attribution,omitempty"`
	To          string  `json:"to"`
	Method      string  `json:"method,omitempty"`
	Path        string  `json:"path,omitempty"`
	Status      int     `json:"status,omitempty"`
	T           Timings `json:"t"`
	Req         Payload `json:"req,omitzero"`
	Resp        Payload `json:"resp,omitzero"`
	// InjectedDelayMs is artificial latency added by a rule, kept distinct
	// from upstream time so timings stay honest.
	InjectedDelayMs float64 `json:"injectedDelayMs,omitempty"`
	Err             string  `json:"err,omitempty"`
}

// Timings breaks a hop into the three observable moments at the proxy.
type Timings struct {
	Start       time.Time `json:"start"`                 // request entered the proxy
	FirstByteMs float64   `json:"firstByteMs,omitempty"` // upstream first response byte
	DoneMs      float64   `json:"doneMs,omitempty"`      // upstream response complete
}

// Payload is one side of a hop. Body is raw text (JSON stays JSON);
// Truncated marks a size-capped body.
type Payload struct {
	Headers   map[string]string `json:"headers,omitempty"`
	Body      string            `json:"body,omitempty"`
	Truncated bool              `json:"truncated,omitempty"`
}

// ErrEOF reports a clean end of an NDJSON stream.
var ErrEOF = errors.New("trace: end of stream")

// Writer appends hops as NDJSON lines, stamping SchemaVersion.
type Writer struct{ w io.Writer }

func NewWriter(w io.Writer) *Writer { return &Writer{w: w} }

func (w *Writer) Write(h Hop) error {
	h.Schema = SchemaVersion
	b, err := json.Marshal(h)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = w.w.Write(b)
	return err
}

// Reader iterates hops from an NDJSON stream, skipping blank lines and
// tolerating unknown fields.
type Reader struct{ s *bufio.Scanner }

func NewReader(r io.Reader) *Reader {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	return &Reader{s: s}
}

func (r *Reader) Next() (Hop, error) {
	for r.s.Scan() {
		line := strings.TrimSpace(r.s.Text())
		if line == "" {
			continue
		}
		var h Hop
		if err := json.Unmarshal([]byte(line), &h); err != nil {
			return Hop{}, err
		}
		return h, nil
	}
	if err := r.s.Err(); err != nil {
		return Hop{}, err
	}
	return Hop{}, ErrEOF
}
