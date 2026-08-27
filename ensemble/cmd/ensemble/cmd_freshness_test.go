package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestCmdFreshnessTableShape(t *testing.T) {
	ts := fakeStatusServer(t, `{"services":[
		{"name":"catalog","status":"healthy","freshness":{"branch":"main","behindBranch":0,"behindDefault":3,"defaultBranch":"main","checkedAt":"2026-08-27T10:00:00Z"}},
		{"name":"stub","status":"healthy"}
	],"readiness":{"state":"ready","checks":[]}}`)

	var stdout, stderr bytes.Buffer
	code := cmdFreshness([]string{"--api-url", ts.URL}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "catalog") || !strings.Contains(out, "main") {
		t.Errorf("stdout missing expected row:\n%s", out)
	}
	if strings.Contains(out, "stub") {
		t.Errorf("stdout should skip a service with no freshness state:\n%s", out)
	}
}

func TestCmdFreshnessNoEligibleServices(t *testing.T) {
	ts := fakeStatusServer(t, `{"services":[{"name":"svc","status":"healthy"}],"readiness":{"state":"ready","checks":[]}}`)

	var stdout, stderr bytes.Buffer
	code := cmdFreshness([]string{"--api-url", ts.URL}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "SERVICE") {
		t.Errorf("stdout missing header row:\n%s", out)
	}
	if strings.Contains(out, "svc\t") {
		t.Errorf("stdout should have no data row for a service with no freshness state:\n%s", out)
	}
}

func TestCmdFreshnessJSON(t *testing.T) {
	ts := fakeStatusServer(t, `{"services":[{"name":"svc","status":"healthy","freshness":{"branch":"main","behindBranch":1,"behindDefault":1,"defaultBranch":"main","checkedAt":"2026-08-27T10:00:00Z"}}],"readiness":{"state":"ready","checks":[]}}`)

	var stdout, stderr bytes.Buffer
	code := cmdFreshness([]string{"--api-url", ts.URL, "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"behindBranch": 1`) {
		t.Errorf("stdout missing freshness field in JSON:\n%s", stdout.String())
	}
}
