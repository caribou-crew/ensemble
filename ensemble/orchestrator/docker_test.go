package orchestrator

import (
	"reflect"
	"strconv"
	"testing"

	"github.com/caribou-crew/ensemble/ensemble/config"
)

// Task 3.6 defects D1 (published on 0.0.0.0, shadowing a developer's own
// database) and D2 (host db.Port used as the container port, so any
// non-default port is dead on arrival): dockerRunDatabaseArgs must publish
// to loopback explicitly, and must map a database's Type to its image's
// real container-side port rather than reusing the host port.
func TestDockerRunDatabaseArgsPublishesLoopbackOnRealContainerPort(t *testing.T) {
	got := dockerRunDatabaseArgs("maindb", config.Database{
		Image: "postgres:16",
		Type:  "postgres",
		Port:  55433,
	})
	want := []string{"run", "-d", "--name", "ensemble-maindb", "-p", "127.0.0.1:55433:5432", "postgres:16"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
}

func TestDockerRunDatabaseArgsContainerPortTable(t *testing.T) {
	cases := []struct {
		typ  string
		want int
	}{
		{"postgres", 5432},
		{"mysql", 3306},
		{"redis", 6379},
		{"dynamodb", 8000},
		{"localstack", 4566},
	}
	for _, c := range cases {
		args := dockerRunDatabaseArgs("db", config.Database{Image: "img", Type: c.typ, Port: 9999})
		wantFlag := "127.0.0.1:9999:" + strconv.Itoa(c.want)
		if !containsPair(args, "-p", wantFlag) {
			t.Errorf("type %q: args = %v, want -p %s", c.typ, args, wantFlag)
		}
	}
}

// An unknown/empty type falls back to db.Port as the container port —
// today's pre-3.6 behavior — rather than guessing.
func TestDockerRunDatabaseArgsUnknownTypeFallsBackToHostPort(t *testing.T) {
	args := dockerRunDatabaseArgs("db", config.Database{Image: "img", Type: "", Port: 7000})
	want := "127.0.0.1:7000:7000"
	if !containsPair(args, "-p", want) {
		t.Errorf("args = %v, want -p %s", args, want)
	}
}

// Step 3: ContainerPort is a config escape hatch that wins over the Type
// table when set, and is purely additive (no existing config changes
// meaning when left zero — covered by the other tests above).
func TestDockerRunDatabaseArgsContainerPortOverride(t *testing.T) {
	args := dockerRunDatabaseArgs("db", config.Database{
		Image:         "img",
		Type:          "postgres",
		Port:          5555,
		ContainerPort: 15432,
	})
	want := "127.0.0.1:5555:15432"
	if !containsPair(args, "-p", want) {
		t.Errorf("args = %v, want -p %s", args, want)
	}
}

func TestDockerRunDatabaseArgsEnvAndImage(t *testing.T) {
	args := dockerRunDatabaseArgs("db", config.Database{
		Image: "postgres:16",
		Type:  "postgres",
		Port:  5432,
		Env:   map[string]string{"POSTGRES_PASSWORD": "secret"},
	})
	if args[len(args)-1] != "postgres:16" {
		t.Errorf("image not last: args = %v", args)
	}
	if !containsPair(args, "-e", "POSTGRES_PASSWORD=secret") {
		t.Errorf("args = %v, missing env flag", args)
	}
}

// No Port set: no -p flag at all (matches pre-3.6 behavior for a
// port-less database entry).
func TestDockerRunDatabaseArgsNoPort(t *testing.T) {
	args := dockerRunDatabaseArgs("db", config.Database{Image: "img", Type: "postgres"})
	for i, a := range args {
		if a == "-p" {
			t.Fatalf("unexpected -p flag at %d: args = %v", i, args)
		}
	}
}

func containsPair(args []string, flag, val string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == val {
			return true
		}
	}
	return false
}
