package orchestrator

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/ensemble/config"
)

// A `type: http` databases entry owns no container — db.Image is always
// empty for it, so falling through to the docker create/adopt/restart path
// in startDatabase would `docker run` with a blank image and fail with
// "invalid reference format". startDatabase must recognize db.Type == "http"
// before ever touching docker.
func TestStartDatabaseHTTPTypeNeverTouchesDocker(t *testing.T) {
	logPath := dockerScript(t, "absent")

	cfg := &config.Config{
		Dir: t.TempDir(),
		Databases: map[string]config.Database{
			"cardco-go-inspect": {Type: "http", URL: "http://127.0.0.1:4281/ensemble-inspect"},
		},
	}
	o := newTestOrchestrator(t, cfg, Opts{HealthTimeout: 5 * time.Second})
	o.DBReady = func(context.Context, string, config.Database) error { return nil }

	if err := o.startDatabase(context.Background(), "cardco-go-inspect", cfg.Databases["cardco-go-inspect"]); err != nil {
		t.Fatalf("startDatabase: %v", err)
	}

	if calls := dockerCalls(t, logPath); len(calls) != 0 {
		t.Errorf("docker was invoked for a type: http database: %v", calls)
	}

	for _, s := range o.States() {
		if s.Name == "cardco-go-inspect" {
			if s.Status != StatusHealthy {
				t.Errorf("status = %q, want healthy", s.Status)
			}
			if s.Placement == "docker" {
				t.Error("placement = docker, want anything but docker — no container was ever created")
			}
			return
		}
	}
	t.Fatal("cardco-go-inspect: no state recorded")
}

// DBReady failing (the backing service's inspector contract never comes
// up) must fail startDatabase the same way an unreachable postgres would —
// not silently report healthy.
func TestStartDatabaseHTTPTypePropagatesDBReadyFailure(t *testing.T) {
	cfg := &config.Config{
		Dir: t.TempDir(),
		Databases: map[string]config.Database{
			"cardco-go-inspect": {Type: "http", URL: "http://127.0.0.1:4281/ensemble-inspect"},
		},
	}
	o := newTestOrchestrator(t, cfg, Opts{HealthTimeout: 200 * time.Millisecond})
	wantErr := errors.New("boom")
	o.DBReady = func(context.Context, string, config.Database) error { return wantErr }

	err := o.startDatabase(context.Background(), "cardco-go-inspect", cfg.Databases["cardco-go-inspect"])
	if err == nil {
		t.Fatal("startDatabase: expected error, got nil")
	}
}

// With no DBReady wired at all, a type: http database has nothing further
// to check and is trivially healthy — mirroring how startDatabase treats
// db.Port <= 0 for the other driver types.
func TestStartDatabaseHTTPTypeWithNoDBReadyIsHealthy(t *testing.T) {
	cfg := &config.Config{
		Dir: t.TempDir(),
		Databases: map[string]config.Database{
			"cardco-go-inspect": {Type: "http", URL: "http://127.0.0.1:4281/ensemble-inspect"},
		},
	}
	o := newTestOrchestrator(t, cfg, Opts{HealthTimeout: 5 * time.Second})

	if err := o.startDatabase(context.Background(), "cardco-go-inspect", cfg.Databases["cardco-go-inspect"]); err != nil {
		t.Fatalf("startDatabase: %v", err)
	}
}
