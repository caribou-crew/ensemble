package orchestrator

import (
	"context"
	"database/sql"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver

	"github.com/caribou-crew/ensemble/ensemble/config"
)

// Task 3.6, step 5: a real, docker-gated regression test for the three
// coupled defects (D1: publishing on 0.0.0.0 can shadow a developer's own
// database; D2: the config port used as the container port makes any
// non-default port dead on arrival; D3: a bare TCP dial reports "healthy"
// through both failures). Skips cleanly when docker isn't usable.
//
// SAFETY: this machine may have a real postgres already on 127.0.0.1:5432
// (and other unrelated containers). Every container this test starts uses
// a freePort()-picked, non-default host port and a container name unique
// to the test run, and is force-removed in t.Cleanup — even on failure —
// so it never touches, and is never confused with, anything else running
// on the host.
func requireDockerIntegration(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping docker-gated integration test in -short mode")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not found in PATH; skipping docker-gated test")
	}
	// A skip-guard has to be able to answer. `docker info` against a wedged
	// daemon does not fail — it blocks indefinitely, and an unbounded call here
	// takes the whole package down with Go's 10-minute panic, reported as a test
	// failure rather than as the unavailable daemon it actually is. That is not
	// hypothetical: it is what this guard did on a developer machine whose
	// daemon had hung, and CI had no job timeout to catch it either.
	//
	// "Cannot answer within a few seconds" is the same outcome as "not usable",
	// so both take the skip: ctx.Err() covers the timeout, err the ordinary
	// failure.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, "docker", "info").Run(); err != nil || ctx.Err() != nil {
		t.Skip("docker daemon not reachable (docker info failed or timed out); skipping docker-gated test")
	}
}

// pgReady wires the same kind of real, protocol-level readiness check
// cmd_up.go's dbReadyProbe builds from ensemble/inspector: a real SQL
// query, not a TCP dial (so it exercises the D3 fix in this package's
// tests without importing ensemble/inspector, which would pull in this
// package's own caller — see the Orchestrator.DBReady field comment for
// why the seam is caller-supplied).
func pgReady(dsn func(port int) string) DBReadyFunc {
	return func(ctx context.Context, name string, db config.Database) error {
		conn, err := sql.Open("pgx", dsn(db.Port))
		if err != nil {
			return err
		}
		defer conn.Close()
		return conn.PingContext(ctx)
	}
}

func testPostgresDSN(port int) string {
	return fmt.Sprintf("postgres://postgres:postgres@127.0.0.1:%d/postgres?sslmode=disable", port)
}

// cleanupContainer force-removes containerName, ignoring "already gone"
// errors, and runs even when the test failed.
func cleanupContainer(t *testing.T, containerName string) {
	t.Helper()
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "-f", containerName).Run()
	})
}

// (a) + (b): a database configured on a non-default host port is actually
// reachable and answers a real query once healthy, and its published
// binding is loopback-only (inspected via `docker port`, not by trying to
// bind 0.0.0.0 from this test — the brief's explicit guidance, since a
// test binding 0.0.0.0 for itself proves nothing about what docker did).
func TestIntegrationDatabaseReachableOnNonDefaultPortLoopbackOnly(t *testing.T) {
	requireDockerIntegration(t)

	hostPort := freePort(t)
	name := fmt.Sprintf("t36-ok-%d", time.Now().UnixNano())
	containerName := dockerContainerName(name)
	cleanupContainer(t, containerName)

	cfg := &config.Config{
		Dir: t.TempDir(),
		Databases: map[string]config.Database{
			name: {
				Image: "postgres:16-alpine",
				Type:  "postgres",
				Port:  hostPort,
				Env:   map[string]string{"POSTGRES_PASSWORD": "postgres"},
			},
		},
	}
	o := newTestOrchestrator(t, cfg, Opts{HealthTimeout: 45 * time.Second})
	o.DBReady = pgReady(testPostgresDSN)

	if err := o.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	defer o.Down()

	st, ok := o.Service(name)
	if !ok || st.Status != StatusHealthy {
		t.Fatalf("service state = %+v, ok=%v, want healthy", st, ok)
	}

	// (a): answers a real query on the published host port.
	conn, err := sql.Open("pgx", testPostgresDSN(hostPort))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer conn.Close()
	var one int
	if err := conn.QueryRowContext(context.Background(), "SELECT 1").Scan(&one); err != nil {
		t.Fatalf("query against %d: %v", hostPort, err)
	}
	if one != 1 {
		t.Fatalf("SELECT 1 = %d, want 1", one)
	}

	// (b): docker port reports a loopback bind, not 0.0.0.0 (defect D1).
	out, err := exec.Command("docker", "port", containerName).CombinedOutput()
	if err != nil {
		t.Fatalf("docker port %s: %v: %s", containerName, err, out)
	}
	portOutput := strings.TrimSpace(string(out))
	if !strings.Contains(portOutput, "127.0.0.1:"+strconv.Itoa(hostPort)) {
		t.Fatalf("docker port output = %q, want a 127.0.0.1:%d binding", portOutput, hostPort)
	}
	if strings.Contains(portOutput, "0.0.0.0:"+strconv.Itoa(hostPort)) {
		t.Fatalf("docker port output = %q, published on 0.0.0.0 (defect D1 regression)", portOutput)
	}
}

// (c): a database whose container server never becomes reachable (here:
// ContainerPort deliberately points at a port nothing inside the
// postgres:16-alpine image listens on, reproducing D2's "wrong container
// port" shape directly) is reported failed, not healthy — the D3
// regression check.
func TestIntegrationDatabaseNeverReachableReportsFailed(t *testing.T) {
	requireDockerIntegration(t)

	hostPort := freePort(t)
	name := fmt.Sprintf("t36-fail-%d", time.Now().UnixNano())
	containerName := dockerContainerName(name)
	cleanupContainer(t, containerName)

	cfg := &config.Config{
		Dir: t.TempDir(),
		Databases: map[string]config.Database{
			name: {
				Image: "postgres:16-alpine",
				Type:  "postgres",
				Port:  hostPort,
				// Nothing inside a stock postgres:16-alpine container
				// listens on 15432 — this is the "wrong container port"
				// failure mode (D2) reproduced deliberately, so the gate
				// (D3's fix) has something real to catch.
				ContainerPort: 15432,
				Env:           map[string]string{"POSTGRES_PASSWORD": "postgres"},
			},
		},
	}
	// Short timeout: this case is expected to time out, and it should stay
	// a fast test even so.
	o := newTestOrchestrator(t, cfg, Opts{HealthTimeout: 5 * time.Second})
	o.DBReady = pgReady(testPostgresDSN)

	err := o.Up(context.Background())
	defer o.Down()

	if err == nil {
		t.Fatalf("Up: got nil error, want a health-gate failure")
	}

	st, ok := o.Service(name)
	if !ok || st.Status != StatusFailed {
		t.Fatalf("service state = %+v, ok=%v, want failed", st, ok)
	}
}
