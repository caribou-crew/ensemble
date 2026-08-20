package trace

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func sampleHop() Hop {
	return Hop{
		Seq:           1,
		TraceID:       "0af7651916cd43dd8448eb211c80319c",
		SpanID:        "b7ad6b7169203331",
		ParentSpanID:  "00f067aa0ba902b7",
		CorrelationID: "corr-123",
		Session:       "run-abc",
		From:          "bff",
		To:            "svc-a",
		Method:        "GET",
		Path:          "/v1/cards",
		Status:        200,
		T: Timings{
			Start:       time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC),
			FirstByteMs: 8.5,
			DoneMs:      12.0,
		},
		Req:  Payload{Headers: map[string]string{"accept": "application/json"}, Body: `{"token":"abc"}`},
		Resp: Payload{Headers: map[string]string{"content-type": "application/json"}, Body: `{"ok":true}`},
	}
}

func TestHopNDJSONRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	in := sampleHop()
	if err := w.Write(in); err != nil {
		t.Fatalf("write: %v", err)
	}
	line := buf.String()
	if strings.Count(line, "\n") != 1 || !strings.HasSuffix(line, "\n") {
		t.Fatalf("want exactly one newline-terminated line, got %q", line)
	}
	if !strings.Contains(line, `"schema":"ensemble/1"`) {
		t.Fatalf("schema version not stamped: %q", line)
	}

	r := NewReader(strings.NewReader(line))
	out, err := r.Next()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	in.Schema = SchemaVersion // Write stamps it
	if out.TraceID != in.TraceID || out.To != in.To || out.Status != in.Status ||
		out.Req.Body != in.Req.Body || out.T.FirstByteMs != in.T.FirstByteMs ||
		!out.T.Start.Equal(in.T.Start) || out.Session != in.Session {
		t.Fatalf("round-trip mismatch:\n in=%+v\nout=%+v", in, out)
	}
}

func TestReaderSkipsBlankAndTolERatesUnknownFields(t *testing.T) {
	input := "\n" +
		`{"schema":"ensemble/1","seq":7,"to":"x","status":204,"futureField":{"a":1}}` + "\n" +
		"\n"
	r := NewReader(strings.NewReader(input))
	h, err := r.Next()
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if h.Seq != 7 || h.To != "x" || h.Status != 204 {
		t.Fatalf("got %+v", h)
	}
	if _, err := r.Next(); err != ErrEOF {
		t.Fatalf("want ErrEOF, got %v", err)
	}
}
