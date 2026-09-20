package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSuiteImportCLI(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	inventory := `{"schema":"retrace/suites/1","suites":[{"id":"taxi","title":"Taxi","version":"v1","platforms":["web"],"features":[{"id":"login","title":"Login","flows":[{"id":"sign-in","title":"Sign in","requiredPlanes":["functional"]}]}]}]}`
	report := `{"schema":"retrace/suite-attempt/1","suiteId":"taxi","suiteVersion":"v1","attemptId":"attempt-1","platform":"web","git":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","branch":"main","dirty":false},"baselineId":"legacy","policyId":"p1","startedAt":"2026-09-20T00:00:00Z","finishedAt":"2026-09-20T00:01:00Z","results":[{"flowId":"sign-in","planes":{"functional":"pass","wire":"not-applicable","visual":"not-applicable"}}]}`
	for name, data := range map[string]string{"retrace.suites.json": inventory, "report.json": report} {
		if err := os.WriteFile(filepath.Join(cwd, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var out, errOut bytes.Buffer
	args := []string{"suite", "import", "--file", "report.json", "--json"}
	if code := run(args, &out, &errOut); code != 0 {
		t.Fatalf("import: %d %s", code, &errOut)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || got["attemptId"] != "attempt-1" {
		t.Fatalf("JSON: %v %s", err, &out)
	}
	stored := filepath.Join(cwd, ".retrace", "suites", "taxi", "attempt-1.json")
	before, err := os.ReadFile(stored)
	if err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errOut.Reset()
	if code := run(args, &out, &errOut); code != exitUsage || errOut.Len() == 0 {
		t.Fatalf("duplicate accepted: %d %s", code, &errOut)
	}
	after, err := os.ReadFile(stored)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("duplicate modified report: %v", err)
	}
	for _, args := range [][]string{{"suite"}, {"suite", "unknown"}, {"suite", "import"}, {"suite", "import", "--file", "missing"}, {"suite", "import", "--file", "report.json", "extra"}} {
		if code := run(args, &out, &errOut); code != exitUsage {
			t.Fatalf("invalid args %v: %d", args, code)
		}
	}
}
