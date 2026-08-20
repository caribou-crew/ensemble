package main

import (
	"errors"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// TestBinaryExitCodesPropagateThroughOSExit — the in-process TestRun*
// tests in main_test.go exercise the run(args, stdout, stderr) seam
// directly; nothing exercises main()'s own os.Exit(run(...)) wiring, so a
// bug there (e.g. os.Exit(0) instead of os.Exit(run(...))) would go
// undetected. This builds the real binary and runs it as a subprocess to
// pin actual process exit codes.
//
// Per global-constraints.md ("Never assert a CLI exit code through `go
// run`"): `go run` collapses every non-zero child status to its own exit
// 1, so an assertion built on `exec.Command("go", "run", ...)` would pass
// for exitUsage (3) only by accident and would not catch it becoming 1 or
// 2. This builds a binary with `go build` and execs THAT, reading the
// child's real status via exec.ExitError.ExitCode().
func TestBinaryExitCodesPropagateThroughOSExit(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping go-build subprocess test in -short mode")
	}
	bin := filepath.Join(t.TempDir(), "retrace")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	cases := []struct {
		name string
		args []string
		want int
	}{
		{"version is exitOK", []string{"--version"}, exitOK},
		{"unknown command is exitUsage", []string{"bogus"}, exitUsage},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cmd := exec.Command(bin, c.args...)
			err := cmd.Run()
			got := 0
			var exitErr *exec.ExitError
			switch {
			case err == nil:
				got = 0
			case errors.As(err, &exitErr):
				got = exitErr.ExitCode()
			default:
				t.Fatalf("running binary: %v", err)
			}
			if got != c.want {
				t.Fatalf("exit code = %d, want %d", got, c.want)
			}
		})
	}
}
