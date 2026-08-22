package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/ensemble/config"
)

// Preflight runs before the orchestrator, so it is the gate that decides
// whether container adoption is reachable at all. A running ensemble-<db>
// container is holding its published host port; if preflight treats that as a
// conflict, `ensemble up` fails on every run after the first and the whole
// reuse path in startDatabase is dead code no test would notice.
//
// These pin both directions, because only excusing conflicts is dangerous in
// one direction and useless in the other: ensemble's own running container
// must be excused, and everything else must still be reported.

// occupyPort binds a port for the duration of the test and returns it,
// standing in for whatever is already listening.
func occupyPort(t *testing.T) int {
	t.Helper()
	port := freePort(t)
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("occupy port: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	return port
}

// stubDBContainerRunning replaces the docker lookup for one test and records
// which database names preflight asked about.
func stubDBContainerRunning(t *testing.T, running bool, err error) *[]string {
	t.Helper()
	var asked []string
	prev := dbContainerRunning
	dbContainerRunning = func(_ context.Context, name string) (bool, error) {
		asked = append(asked, name)
		return running, err
	}
	t.Cleanup(func() { dbContainerRunning = prev })
	return &asked
}

func dbOnPort(port int) *config.Config {
	return &config.Config{Databases: map[string]config.Database{
		"orders": {Type: "postgres", Image: "postgres:16", Port: port},
	}}
}

func TestCheckPortsFreeAdoptsOwnRunningDatabaseContainer(t *testing.T) {
	port := occupyPort(t)
	asked := stubDBContainerRunning(t, true, nil)

	if err := checkPortsFree(dbOnPort(port), nil); err != nil {
		t.Fatalf("a port held by ensemble's own running container is not a conflict, got: %v", err)
	}
	if len(*asked) != 1 || (*asked)[0] != "orders" {
		t.Errorf("preflight asked about %v, want exactly [orders]", *asked)
	}
}

func TestCheckPortsFreeStillReportsForeignOccupant(t *testing.T) {
	port := occupyPort(t)
	stubDBContainerRunning(t, false, nil)

	// This is Steven's actual case: the port is held by something that is not
	// an ensemble container (a podman VM proxy, another project's postgres).
	// Nothing about adoption should soften that — it is a real conflict.
	err := checkPortsFree(dbOnPort(port), nil)
	if err == nil {
		t.Fatal("expected a conflict: the port is held by something ensemble does not manage")
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("port %d", port)) ||
		!strings.Contains(err.Error(), "database orders") {
		t.Errorf("error %q does not name the conflicting port/database", err)
	}
}

// An unusable daemon must not silently excuse a conflict. "Cannot ask" is not
// "yes" — if it were, preflight would wave through a port held by an unrelated
// process and the failure would resurface later as an unexplained health-gate
// timeout, which is the exact confusion preflight exists to prevent.
func TestCheckPortsFreeReportsConflictWhenDockerCannotAnswer(t *testing.T) {
	port := occupyPort(t)
	stubDBContainerRunning(t, false, errors.New("Cannot connect to the Docker daemon"))

	if err := checkPortsFree(dbOnPort(port), nil); err == nil {
		t.Fatal("a daemon that cannot answer must leave the conflict reported, not excuse it")
	}
}

// The lookup only happens for a port that actually conflicts. Shelling out to
// docker for every configured database on every `up` would add daemon latency
// to the common path where nothing is wrong at all.
func TestCheckPortsFreeDoesNotConsultDockerWhenPortsAreFree(t *testing.T) {
	asked := stubDBContainerRunning(t, true, nil)

	if err := checkPortsFree(dbOnPort(freePort(t)), nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(*asked) != 0 {
		t.Errorf("docker was consulted for a free port (asked about %v)", *asked)
	}
}

// Adoption is scoped to databases. A service's port is not something the
// orchestrator reuses a container for, so a conflict there stays a conflict
// even while a database name happens to be adoptable.
func TestCheckPortsFreeDoesNotAdoptServicePorts(t *testing.T) {
	port := occupyPort(t)
	stubDBContainerRunning(t, true, nil)

	cfg := &config.Config{Services: map[string]config.Service{
		"api": {Run: "x", Port: port},
	}}
	if err := checkPortsFree(cfg, nil); err == nil {
		t.Fatal("a service port conflict must not be excused by container adoption")
	}
}

// Preflight is the first thing `up` does. A wedged daemon must not hang it —
// the lookup is bounded, and the bound is what this asserts.
func TestCheckPortsFreeIsNotHungByAWedgedDaemon(t *testing.T) {
	port := occupyPort(t)
	prev := dbContainerRunning
	dbContainerRunning = func(ctx context.Context, _ string) (bool, error) {
		<-ctx.Done() // a daemon that never answers
		return false, ctx.Err()
	}
	t.Cleanup(func() { dbContainerRunning = prev })

	done := make(chan error, 1)
	go func() { done <- checkPortsFree(dbOnPort(port), nil) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a daemon that never answered must not have excused the conflict")
		}
	case <-time.After(preflightDockerTimeout + 10*time.Second):
		t.Fatal("checkPortsFree hung on an unresponsive docker daemon")
	}
}
