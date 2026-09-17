package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caribou-crew/ensemble/retrace/runs"
)

func TestHelperReplayEncryptedRequest(t *testing.T) {
	mode := os.Getenv("RETRACE_TEST_HELPER")
	if !strings.HasPrefix(mode, "replay-encrypted-request-") {
		return
	}
	body := `{"account_number":"acct-live-secret","amount":1250}`
	switch mode {
	case "replay-encrypted-request-changed":
		body = `{"account_number":"acct-changed-secret","amount":1250}`
	case "replay-encrypted-request-type":
		body = `{"account_number":4111111111111111,"amount":1250}`
	case "replay-encrypted-request-missing":
		body = `{"amount":1250}`
	}
	req, err := http.NewRequest(http.MethodPost, os.Getenv("RETRACE_PROXY_URL")+"/charge", strings.NewReader(body))
	if err != nil {
		fmt.Fprintln(os.Stderr, "helper request:", err)
		os.Exit(9)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, "helper fetch:", err)
		os.Exit(9)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	os.Exit(0)
}

func TestHelperReplayRequestOnly(t *testing.T) {
	mode := os.Getenv("RETRACE_TEST_HELPER")
	if mode != "replay-request-only" && mode != "replay-request-only-no-auth" {
		return
	}
	req, err := http.NewRequest(http.MethodGet, os.Getenv("RETRACE_PROXY_URL")+"/cart", nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "helper request:", err)
		os.Exit(9)
	}
	if mode == "replay-request-only" {
		req.Header.Set("Authorization", "Bearer request-secret")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, "helper fetch:", err)
		os.Exit(9)
	}
	resp.Body.Close()
	os.Exit(0)
}

func TestReplayAssertRequestsIgnoresEncryptedResponsesAndRedactsRequests(t *testing.T) {
	const responseSecret = "account-response-secret"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Account", responseSecret)
		w.Write([]byte(`{"account_number":"` + responseSecret + `"}`))
	}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	t.Setenv("RETRACE_RECORDING_KEY", "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
	writeConfig(t, cwd, `app: web
redact:
  - authorization
  - field: account_number
    mode: encrypt
    why: exercise encrypted response replay
wire_rules:
  - headers:
      date: http-date
`)
	recordArgs := append([]string{"run", "--flow", "checkout", "--app", "web", "--upstream", upstream.URL},
		selfCmd(t, "TestHelperReplayRequestOnly")...)
	recorded := runRetrace(t, bin, cwd, "replay-request-only", recordArgs...)
	if recorded.code != 0 {
		t.Fatalf("record: exit = %d\nstdout: %s\nstderr: %s", recorded.code, recorded.stdout, recorded.stderr)
	}
	accepted := runRetrace(t, bin, cwd, "", "ref", "accept", "--flow", "checkout", "--app", "web")
	if accepted.code != 0 {
		t.Fatalf("ref accept: exit = %d\nstdout: %s\nstderr: %s", accepted.code, accepted.stdout, accepted.stderr)
	}

	replayArgs := append([]string{"replay", "--ref", "checkout", "--app", "web", "--assert-requests", "--json"},
		selfCmd(t, "TestHelperReplayRequestOnly")...)
	result := runRetrace(t, bin, cwd, "replay-request-only", replayArgs...)
	if result.code != 0 {
		t.Fatalf("exit = %d, want 0 — response fields are outside --assert-requests and the request matches after capture redaction\nstdout: %s\nstderr: %s",
			result.code, result.stdout, result.stderr)
	}
	for _, secret := range []string{responseSecret, "request-secret"} {
		if strings.Contains(result.stdout, secret) || strings.Contains(result.stderr, secret) {
			t.Fatalf("replay report leaked %q\nstdout: %s\nstderr: %s", secret, result.stdout, result.stderr)
		}
	}
	var report struct {
		RequestDiff struct {
			Changed int               `json:"changed"`
			Entries []json.RawMessage `json:"entries"`
		} `json:"requestDiff"`
	}
	if err := json.Unmarshal([]byte(result.stdout), &report); err != nil {
		t.Fatalf("decode replay report: %v\n%s", err, result.stdout)
	}
	if report.RequestDiff.Changed != 0 || len(report.RequestDiff.Entries) != 0 {
		t.Fatalf("requestDiff = %+v, want no request-side changes", report.RequestDiff)
	}

	settlePastRunIDResolution()
	missing := runRetrace(t, bin, cwd, "replay-request-only-no-auth", replayArgs...)
	if missing.code != exitGate {
		t.Fatalf("missing authorization: exit = %d, want %d — redaction must preserve header presence\nstdout: %s\nstderr: %s",
			missing.code, exitGate, missing.stdout, missing.stderr)
	}
	if strings.Contains(missing.stdout, "request-secret") || strings.Contains(missing.stderr, "request-secret") {
		t.Fatalf("missing-authorization report leaked the recorded credential\nstdout: %s\nstderr: %s", missing.stdout, missing.stderr)
	}
}

func TestReplayAssertRequestsComparesEncryptedMutationFieldsWithoutLeaks(t *testing.T) {
	const key = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	bin := buildRetrace(t)
	cwd := t.TempDir()
	t.Setenv("RETRACE_RECORDING_KEY", key)
	writeConfig(t, cwd, `app: web
redact:
  - field: account_number
    mode: encrypt
    why: compare sensitive mutation input
wire_rules:
  - headers:
      date: http-date
`)
	recordArgs := append([]string{"run", "--flow", "checkout", "--app", "web", "--upstream", upstream.URL}, selfCmd(t, "TestHelperReplayEncryptedRequest")...)
	recorded := runRetrace(t, bin, cwd, "replay-encrypted-request-same", recordArgs...)
	if recorded.code != 0 {
		t.Fatalf("record: exit = %d\nstdout: %s\nstderr: %s", recorded.code, recorded.stdout, recorded.stderr)
	}
	accepted := runRetrace(t, bin, cwd, "", "ref", "accept", "--flow", "checkout", "--app", "web")
	if accepted.code != 0 {
		t.Fatalf("ref accept: exit = %d\nstdout: %s\nstderr: %s", accepted.code, accepted.stdout, accepted.stderr)
	}

	replayArgs := append([]string{"replay", "--ref", "checkout", "--app", "web", "--assert-requests", "--json"}, selfCmd(t, "TestHelperReplayEncryptedRequest")...)
	for _, tc := range []struct {
		name string
		mode string
		want int
	}{
		{name: "same plaintext", mode: "replay-encrypted-request-same", want: 0},
		{name: "changed plaintext", mode: "replay-encrypted-request-changed", want: exitGate},
		{name: "changed type", mode: "replay-encrypted-request-type", want: exitGate},
		{name: "missing field", mode: "replay-encrypted-request-missing", want: exitGate},
	} {
		t.Run(tc.name, func(t *testing.T) {
			settlePastRunIDResolution()
			result := runRetrace(t, bin, cwd, tc.mode, replayArgs...)
			if result.code != tc.want {
				t.Fatalf("exit = %d, want %d\nstdout: %s\nstderr: %s", result.code, tc.want, result.stdout, result.stderr)
			}
			for _, secret := range []string{"acct-live-secret", "acct-changed-secret", "4111111111111111"} {
				if strings.Contains(result.stdout, secret) || strings.Contains(result.stderr, secret) {
					t.Fatalf("replay report leaked %q\nstdout: %s\nstderr: %s", secret, result.stdout, result.stderr)
				}
			}
			if tc.want != 0 {
				misses, err := os.ReadFile(filepath.Join(newestReplayRunDir(t, cwd, "web", "checkout"), "misses.jsonl"))
				if err != nil {
					t.Fatalf("read misses artifact: %v", err)
				}
				for _, secret := range []string{"acct-live-secret", "acct-changed-secret", "4111111111111111"} {
					if strings.Contains(string(misses), secret) {
						t.Fatalf("misses artifact leaked %q: %s", secret, misses)
					}
				}
			}
			if tc.want == 0 {
				wire, err := os.ReadFile(filepath.Join(newestReplayRunDir(t, cwd, "web", "checkout"), "wire.jsonl"))
				if err != nil {
					t.Fatalf("read observed wire: %v", err)
				}
				if strings.Contains(string(wire), "acct-live-secret") {
					t.Fatalf("observed wire leaked encrypted request plaintext: %s", wire)
				}
				if !strings.Contains(string(wire), "$enc:v1:") {
					t.Fatalf("observed wire did not preserve the encrypt rule: %s", wire)
				}
			}
		})
	}

	for _, badKey := range []string{"", "ffeeddccbbaa99887766554433221100ffeeddccbbaa99887766554433221100"} {
		runsBeforeBadKey := runs.ListRuns(replaysRoot(cwd), "web", "checkout")
		t.Setenv("RETRACE_RECORDING_KEY", badKey)
		settlePastRunIDResolution()
		failed := runRetrace(t, bin, cwd, "replay-encrypted-request-same", replayArgs...)
		if failed.code == 0 {
			t.Fatalf("missing/wrong key unexpectedly passed\nstdout: %s\nstderr: %s", failed.stdout, failed.stderr)
		}
		if strings.Contains(failed.stdout, "acct-live-secret") || strings.Contains(failed.stderr, "acct-live-secret") {
			t.Fatalf("missing/wrong-key failure leaked plaintext\nstdout: %s\nstderr: %s", failed.stdout, failed.stderr)
		}
		runsAfterBadKey := runs.ListRuns(replaysRoot(cwd), "web", "checkout")
		if len(runsAfterBadKey) != len(runsBeforeBadKey) {
			t.Fatalf("missing/wrong-key replay created an artifact directory: before=%v after=%v", runsBeforeBadKey, runsAfterBadKey)
		}
	}
}
