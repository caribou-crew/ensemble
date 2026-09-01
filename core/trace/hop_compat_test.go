package trace

import (
	"bytes"
	"os"
	"reflect"
	"testing"
)

// The audit-hardening change added fields to Hop (Streaming, Unsupported)
// and Payload (BodyB64, SetCookies). All are omitempty additions under the
// unchanged "ensemble/1" schema: recordings written before the change must
// read back exactly, and hops using the new fields must round-trip.

func TestPreChangeRecordingReadsUnchanged(t *testing.T) {
	f, err := os.Open("testdata/prechange_wire.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	r := NewReader(f)
	var hops []Hop
	for {
		h, err := r.Next()
		if err == ErrEOF {
			break
		}
		if err != nil {
			t.Fatalf("reading pre-change fixture: %v", err)
		}
		hops = append(hops, h)
	}
	if len(hops) != 3 {
		t.Fatalf("got %d hops, want 3", len(hops))
	}
	if hops[0].To != "catalog" || hops[0].Resp.Body != `{"items":[]}` {
		t.Errorf("hop 1 misread: %+v", hops[0])
	}
	if !hops[1].Req.Truncated || hops[1].InjectedDelayMs != 400 || hops[1].Client != "web" {
		t.Errorf("hop 2 misread: %+v", hops[1])
	}
	if !hops[2].Preflight {
		t.Errorf("hop 3 misread: %+v", hops[2])
	}
	for i, h := range hops {
		if h.Streaming || h.Unsupported != "" ||
			h.Req.BodyB64 != "" || h.Resp.BodyB64 != "" ||
			h.Req.SetCookies != nil || h.Resp.SetCookies != nil {
			t.Errorf("hop %d: new fields must be zero on a pre-change record: %+v", i+1, h)
		}
	}

	// A pre-change hop written back must not sprout new keys.
	var buf bytes.Buffer
	if err := NewWriter(&buf).Write(hops[0]); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"streaming", "unsupported", "bodyB64", "setCookies"} {
		if bytes.Contains(buf.Bytes(), []byte(`"`+key+`"`)) {
			t.Errorf("re-serialized pre-change hop emits %q:\n%s", key, buf.String())
		}
	}
}

func TestNewFieldsRoundTrip(t *testing.T) {
	in := sampleHop()
	in.Streaming = true
	in.Unsupported = "websocket"
	in.Resp = Payload{
		Headers:    map[string]string{"set-cookie": "a=1, b=2"},
		BodyB64:    "iVBORw0KGgo=",
		SetCookies: []string{"a=1; Path=/", "b=2; HttpOnly"},
		Truncated:  true,
	}

	var buf bytes.Buffer
	if err := NewWriter(&buf).Write(in); err != nil {
		t.Fatal(err)
	}
	out, err := NewReader(&buf).Next()
	if err != nil {
		t.Fatal(err)
	}
	in.Schema = SchemaVersion // Writer stamps it
	if !reflect.DeepEqual(in, out) {
		t.Errorf("round trip mismatch:\n in: %+v\nout: %+v", in, out)
	}
}
