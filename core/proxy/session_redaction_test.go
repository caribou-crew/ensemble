package proxy

import (
	"strings"
	"testing"

	"github.com/caribou-crew/ensemble/core/trace"
)

func TestSessionVerdictDegradesForRedactionFailure(t *testing.T) {
	for _, stage := range []string{"record", "stream finalization"} {
		t.Run(stage, func(t *testing.T) {
			red, err := trace.NewRedactor(nil, 0, nil)
			if err != nil {
				t.Fatal(err)
			}
			rec := NewRecorder(RecorderOpts{Redactor: red})
			p := New(rec)
			defer p.Close()
			mgr := NewSessionManager(p, rec, []string{"api"})
			defer mgr.Close()
			ses, err := mgr.Start("unsafe-json", "api", "http://127.0.0.1:1", "127.0.0.1", 0)
			if err != nil {
				t.Fatal(err)
			}
			h := trace.Hop{To: "api", Session: ses.ID, Method: "GET", Path: "/fixture", Status: 200}
			unsafe := trace.Payload{Headers: map[string]string{"Content-Type": "application/json"}, Body: `{"password":"synthetic-session-canary","tail":"`, Truncated: true}
			if stage == "record" {
				h.Req = unsafe
				rec.Record(h)
			} else {
				h.Streaming = true
				h = rec.Record(h)
				waitFor(t, "open stream routed", func() bool { return len(ses.Hops()) == 1 })
				h.Resp = unsafe
				rec.Update(h)
			}
			waitFor(t, "redaction failure routed", func() bool {
				hs := ses.Hops()
				return len(hs) == 1 && hs[0].Err != ""
			})
			verdict, reasons := ses.Verdict()
			if verdict != trace.VerdictDegraded {
				t.Fatalf("session verdict=%s, want degraded: %v", verdict, reasons)
			}
			if !strings.Contains(strings.Join(reasons, " "), "redaction") {
				t.Errorf("session omits redaction failure reason: %v", reasons)
			}
		})
	}
}
