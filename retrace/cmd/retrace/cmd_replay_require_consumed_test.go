package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestHelperRequireConsumed(t *testing.T) {
	mode := os.Getenv("RETRACE_TEST_HELPER")
	if mode != "require-consumed-record" && mode != "require-consumed-replay" {
		return
	}
	paths := []string{"/cart"}
	if mode == "require-consumed-record" {
		paths = append(paths, "/orders")
	}
	for _, path := range paths {
		resp, err := http.Get(os.Getenv("RETRACE_PROXY_URL") + path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "helper fetch:", err)
			os.Exit(9)
		}
		resp.Body.Close()
	}
	os.Exit(0)
}

func TestReplayRequireConsumedFailsWhenRecordedExchangeIsUnused(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	writeConfig(t, cwd, dateRuleConfig)
	recordArgs := append([]string{"run", "--flow", "checkout", "--app", "web", "--upstream", upstream.URL},
		selfCmd(t, "TestHelperRequireConsumed")...)
	recorded := runRetrace(t, bin, cwd, "require-consumed-record", recordArgs...)
	if recorded.code != 0 {
		t.Fatalf("record: exit = %d\nstdout: %s\nstderr: %s", recorded.code, recorded.stdout, recorded.stderr)
	}
	accepted := runRetrace(t, bin, cwd, "", "ref", "accept", "--flow", "checkout", "--app", "web")
	if accepted.code != 0 {
		t.Fatalf("ref accept: exit = %d\nstdout: %s\nstderr: %s", accepted.code, accepted.stdout, accepted.stderr)
	}

	plainArgs := append([]string{"replay", "--ref", "checkout", "--app", "web", "--json"},
		selfCmd(t, "TestHelperRequireConsumed")...)
	plain := runRetrace(t, bin, cwd, "require-consumed-replay", plainArgs...)
	if plain.code != 0 {
		t.Fatalf("default replay: exit = %d, want 0 — unused exchanges remain informational without --require-consumed\nstdout: %s\nstderr: %s",
			plain.code, plain.stdout, plain.stderr)
	}
	settlePastRunIDResolution()

	replayArgs := append([]string{"replay", "--ref", "checkout", "--app", "web", "--require-consumed", "--json"},
		selfCmd(t, "TestHelperRequireConsumed")...)
	result := runRetrace(t, bin, cwd, "require-consumed-replay", replayArgs...)
	if result.code != exitGate {
		t.Fatalf("exit = %d, want %d — /orders was recorded but the green test command never requested it\nstdout: %s\nstderr: %s",
			result.code, exitGate, result.stdout, result.stderr)
	}
	var report struct {
		Served int `json:"served"`
		Unused []struct {
			Path string `json:"path"`
		} `json:"unused"`
		Test struct {
			ExitCode int `json:"exitCode"`
		} `json:"test"`
	}
	if err := json.Unmarshal([]byte(result.stdout), &report); err != nil {
		t.Fatalf("decode replay report: %v\nstdout: %s\nstderr: %s", err, result.stdout, result.stderr)
	}
	if report.Served != 1 || report.Test.ExitCode != 0 {
		t.Fatalf("served = %d, test.exitCode = %d, want 1 and 0", report.Served, report.Test.ExitCode)
	}
	if len(report.Unused) != 1 || report.Unused[0].Path != "/orders" {
		t.Fatalf("unused = %+v, want only /orders", report.Unused)
	}
}

func TestReplayRequireConsumedIsDocumentedInFlagHelp(t *testing.T) {
	result := runRetrace(t, buildRetrace(t), t.TempDir(), "", "replay", "--help")
	if result.code != exitUsage {
		t.Fatalf("replay --help exit = %d, want %d\nstdout: %s\nstderr: %s",
			result.code, exitUsage, result.stdout, result.stderr)
	}
	if want := "-require-consumed"; !strings.Contains(result.stderr, want) {
		t.Fatalf("replay --help does not mention %s:\n%s", want, result.stderr)
	}
	if want := "leaves any recorded exchange unused"; !strings.Contains(result.stderr, want) {
		t.Fatalf("replay --help does not explain the consumption gate:\n%s", result.stderr)
	}
}
