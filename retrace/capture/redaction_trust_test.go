package capture

import (
	"errors"
	"testing"

	"github.com/caribou-crew/ensemble/core/trace"
)

func TestAssessDegradesRedactionFailures(t *testing.T) {
	for _, prior := range []string{"", "unexpected EOF"} {
		h := trace.DegradeHop(trace.Hop{Seq: 1, Err: prior}, errors.New("JSON capture is incomplete"))
		got := Assess(AssessInput{Hops: []trace.Hop{h}, RequestsSeen: 1})
		if got.Status != trace.VerdictDegraded || !Fatal(got) {
			t.Errorf("prior=%q: redaction failure assessed as %s", prior, got.Status)
		}
	}
	got := Assess(AssessInput{Hops: []trace.Hop{{Seq: 1, Status: 502, Err: "dial: connection refused"}}, RequestsSeen: 1})
	if got.Status != trace.VerdictOK {
		t.Errorf("ordinary recorded HTTP failure degraded capture: %s", got.Status)
	}
}
