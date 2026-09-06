package proxy

import (
	"testing"

	"github.com/caribou-crew/ensemble/core/trace"
)

func TestRecorderRefusesSequenceWrap(t *testing.T) {
	rec := NewRecorder(RecorderOpts{InitialSeq: ^uint64(0) - 1})
	defer rec.Close()
	if h := rec.Record(trace.Hop{Path: "/last"}); h.Seq != ^uint64(0) {
		t.Fatalf("last available sequence = %d", h.Seq)
	}
	defer func() {
		if recover() == nil {
			t.Error("exhausted sequence wrapped instead of failing")
		}
		if got := rec.Snapshot(); len(got) != 1 || got[0].Path != "/last" {
			t.Errorf("exhausted recorder published a duplicate sequence: %+v", got)
		}
	}()
	rec.Record(trace.Hop{Path: "/must-not-wrap"})
}
