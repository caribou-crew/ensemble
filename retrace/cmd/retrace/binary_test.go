package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

var testBinaryDir string

// All subprocess tests exercise the same source and build flags. Build once
// per suite, lazily so helper subprocesses and tests without a CLI do not
// compile anything. Each invocation still gets its own working directory,
// environment and process; only the immutable executable is shared.
var testRetraceBinary = sync.OnceValues(func() (string, error) {
	dir, err := os.MkdirTemp("", "retrace-test-binary-")
	if err != nil {
		return "", err
	}
	testBinaryDir = dir
	bin := filepath.Join(dir, "retrace")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		return "", fmt.Errorf("go build: %w\n%s", err, out)
	}
	return bin, nil
})

func TestMain(m *testing.M) {
	code := m.Run()
	if testBinaryDir != "" {
		os.RemoveAll(testBinaryDir)
	}
	os.Exit(code)
}
