package tui

import (
	"context"
	"sync"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/ensemble/orchestrator"
)

// fakeAPIClient is an in-memory apiClient used by panel/model tests —
// every call records how it was invoked and returns whatever's queued, so
// tests can assert both the resulting UI state and which endpoint a key
// binding actually called.
type fakeAPIClient struct {
	mu sync.Mutex

	status   StatusResponse
	statusErr error
	calls    []string

	restartResult orchestrator.ServiceState
	restartErr    error
	flipResult    orchestrator.ServiceState
	flipErr       error
	seedResult    SeedResponse
	seedErr       error

	latency    LatencyListResponse
	latencyErr error

	profiles    orchestrator.ProfilesState
	profilesErr error

	trafficErr error
}

func (f *fakeAPIClient) record(call string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
}

func (f *fakeAPIClient) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeAPIClient) Status(ctx context.Context) (StatusResponse, error) {
	f.record("status")
	return f.status, f.statusErr
}

func (f *fakeAPIClient) Restart(ctx context.Context, name string) (orchestrator.ServiceState, error) {
	f.record("restart:" + name)
	return f.restartResult, f.restartErr
}

func (f *fakeAPIClient) Flip(ctx context.Context, name string) (orchestrator.ServiceState, error) {
	f.record("flip:" + name)
	return f.flipResult, f.flipErr
}

func (f *fakeAPIClient) Seed(ctx context.Context, name string) (SeedResponse, error) {
	f.record("seed:" + name)
	return f.seedResult, f.seedErr
}

func (f *fakeAPIClient) LatencyList(ctx context.Context) (LatencyListResponse, error) {
	f.record("latency-list")
	return f.latency, f.latencyErr
}

func (f *fakeAPIClient) LatencyArmAll(ctx context.Context, enabled bool) (LatencyListResponse, error) {
	if enabled {
		f.record("latency-arm-all")
	} else {
		f.record("latency-disarm-all")
	}
	return f.latency, f.latencyErr
}

func (f *fakeAPIClient) LatencyReset(ctx context.Context) (LatencyListResponse, error) {
	f.record("latency-reset")
	return f.latency, f.latencyErr
}

func (f *fakeAPIClient) Profiles(ctx context.Context) (orchestrator.ProfilesState, error) {
	f.record("profiles")
	return f.profiles, f.profilesErr
}

func (f *fakeAPIClient) ProfileUp(ctx context.Context, name string) (orchestrator.ProfilesState, error) {
	f.record("profile-up:" + name)
	return f.profiles, f.profilesErr
}

func (f *fakeAPIClient) ProfileDown(ctx context.Context, name string) (orchestrator.ProfilesState, error) {
	f.record("profile-down:" + name)
	return f.profiles, f.profilesErr
}

func (f *fakeAPIClient) TrafficStream(ctx context.Context, since uint64) (<-chan trace.Hop, error) {
	f.record("traffic-stream")
	if f.trafficErr != nil {
		return nil, f.trafficErr
	}
	ch := make(chan trace.Hop)
	close(ch)
	return ch, nil
}
