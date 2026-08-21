package tui

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/core/trace"
)

// setStreamReconnectDelayForTest overrides the package's reconnect delay
// for the duration of the test, restoring it on cleanup — avoids the
// default 1s delay making reconnect tests slow. Since streamReconnectDelay
// is process-global, every test that uses it must also fully drain its
// StreamTraffic channel (see drainUntilClosed) before returning: otherwise
// a still-running goroutine from this test can read the var concurrently
// with the next test's cleanup resetting it.
func setStreamReconnectDelayForTest(t *testing.T, d time.Duration) {
	t.Helper()
	orig := streamReconnectDelay
	streamReconnectDelay = d
	t.Cleanup(func() { streamReconnectDelay = orig })
}

// drainUntilClosed cancels the stream and blocks until out closes,
// confirming StreamTraffic's background goroutine has actually exited —
// see setStreamReconnectDelayForTest.
func drainUntilClosed(t *testing.T, out <-chan trace.Hop, cancel context.CancelFunc) {
	t.Helper()
	cancel()
	for {
		select {
		case _, ok := <-out:
			if !ok {
				return
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for StreamTraffic to stop after cancel")
		}
	}
}

// fakeStreamClient is a minimal apiClient stub for stream tests: only
// TrafficStream is exercised, everything else panics if called.
type fakeStreamClient struct {
	apiClient
	attempts atomic.Int32
	connect  func(attempt int) (<-chan trace.Hop, error)
}

func (f *fakeStreamClient) TrafficStream(ctx context.Context, since uint64) (<-chan trace.Hop, error) {
	n := int(f.attempts.Add(1))
	return f.connect(n)
}

func TestStreamTrafficReconnectsAfterConnectError(t *testing.T) {
	setStreamReconnectDelayForTest(t, 5*time.Millisecond)

	fc := &fakeStreamClient{connect: func(attempt int) (<-chan trace.Hop, error) {
		if attempt == 1 {
			return nil, errors.New("connection refused")
		}
		ch := make(chan trace.Hop, 1)
		ch <- trace.Hop{Seq: 1, To: "catalog"}
		close(ch)
		return ch, nil
	}}

	ctx, cancel := context.WithCancel(context.Background())
	out := StreamTraffic(ctx, fc, 0)
	defer drainUntilClosed(t, out, cancel)

	select {
	case h := <-out:
		if h.To != "catalog" {
			t.Fatalf("unexpected hop: %+v", h)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for hop after reconnect")
	}
	if fc.attempts.Load() < 2 {
		t.Fatalf("expected at least 2 connect attempts, got %d", fc.attempts.Load())
	}
}

// recordingStreamClient records the `since` cursor passed to each
// TrafficStream call, so tests can assert a reconnect picks up where the
// last delivered hop left off instead of replaying from 0.
type recordingStreamClient struct {
	apiClient
	steps []func() (<-chan trace.Hop, error)
	n     int

	mu        sync.Mutex
	sinceSeen []uint64
}

func (r *recordingStreamClient) TrafficStream(ctx context.Context, since uint64) (<-chan trace.Hop, error) {
	r.mu.Lock()
	r.sinceSeen = append(r.sinceSeen, since)
	r.mu.Unlock()

	i := r.n
	r.n++
	if i >= len(r.steps) {
		ch := make(chan trace.Hop)
		close(ch)
		return ch, nil
	}
	return r.steps[i]()
}

func (r *recordingStreamClient) SinceSeen() []uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]uint64(nil), r.sinceSeen...)
}

func TestStreamTrafficAdvancesCursorAcrossReconnect(t *testing.T) {
	setStreamReconnectDelayForTest(t, 5*time.Millisecond)

	rc := &recordingStreamClient{steps: []func() (<-chan trace.Hop, error){
		func() (<-chan trace.Hop, error) {
			ch := make(chan trace.Hop, 1)
			ch <- trace.Hop{Seq: 7, To: "a"}
			close(ch)
			return ch, nil
		},
	}}

	ctx, cancel := context.WithCancel(context.Background())
	out := StreamTraffic(ctx, rc, 0)
	defer drainUntilClosed(t, out, cancel)

	select {
	case h := <-out:
		if h.Seq != 7 {
			t.Fatalf("unexpected hop: %+v", h)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first hop")
	}

	deadline := time.Now().Add(time.Second)
	for len(rc.SinceSeen()) < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	seen := rc.SinceSeen()
	if len(seen) < 2 {
		t.Fatalf("expected a second connect attempt, got %v", seen)
	}
	if seen[1] != 7 {
		t.Fatalf("expected reconnect to use since=7 (last delivered Seq), got %v", seen)
	}
}

func TestStreamTrafficStopsOnContextCancel(t *testing.T) {
	setStreamReconnectDelayForTest(t, 5*time.Millisecond)

	fc := &fakeStreamClient{connect: func(attempt int) (<-chan trace.Hop, error) {
		return nil, errors.New("never connects")
	}}
	ctx, cancel := context.WithCancel(context.Background())
	out := StreamTraffic(ctx, fc, 0)
	cancel()

	select {
	case _, ok := <-out:
		if ok {
			t.Fatal("expected channel to close, got a hop instead")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for channel to close after cancel")
	}
}
