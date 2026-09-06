package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	serverapi "github.com/caribou-crew/ensemble/ensemble/server"
)

func TestCmdDoctorPostsProbeAndRendersPass(t *testing.T) {
	var request serverapi.DoctorRequest
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/doctor" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(serverapi.DoctorResponse{
			Verdict: serverapi.DoctorPass, Target: "entry", Path: "/health",
			HTTPStatus: 204, TraceID: "trace-1", SessionID: "session-1",
			ObservedTargets: []string{"entry", "leaf"},
			Propagation:     serverapi.DoctorPropagation{TraceContext: true, SessionContext: true, Linked: true},
		})
	}))
	t.Cleanup(ts.Close)

	var stdout, stderr bytes.Buffer
	code := cmdDoctor([]string{"--api-url", ts.URL, "--target", "entry", "--path", "/health", "--expect", "leaf", "--timeout", "750ms"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	if request.Target != "entry" || request.Path != "/health" || request.TimeoutMs != 750 || strings.Join(request.Expect, ",") != "leaf" {
		t.Fatalf("request = %+v", request)
	}
	if out := stdout.String(); !strings.Contains(out, "PASS") || !strings.Contains(out, "entry") || !strings.Contains(out, "trace-1") {
		t.Fatalf("stdout = %q", out)
	}
}

func doctorResultServer(t *testing.T, result serverapi.DoctorResponse, status int) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if status >= 400 {
			_, _ = w.Write([]byte(`{"error":"cannot run probe"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(result)
	}))
	t.Cleanup(ts.Close)
	return ts
}

func TestCmdDoctorJSONAndVerdictExitCodes(t *testing.T) {
	for _, verdict := range []serverapi.DoctorVerdict{serverapi.DoctorFail, serverapi.DoctorInconclusive} {
		t.Run(string(verdict), func(t *testing.T) {
			ts := doctorResultServer(t, serverapi.DoctorResponse{Verdict: verdict, Target: "entry", Path: "/", Reasons: []string{"evidence"}}, http.StatusOK)
			var stdout, stderr bytes.Buffer
			code := cmdDoctor([]string{"--api-url", ts.URL, "--target", "entry", "--path", "/", "--json"}, &stdout, &stderr)
			if code != 1 {
				t.Fatalf("exit = %d, want 1; stderr=%s", code, stderr.String())
			}
			var got serverapi.DoctorResponse
			if err := json.Unmarshal(stdout.Bytes(), &got); err != nil || got.Verdict != verdict {
				t.Fatalf("JSON = %s, err=%v", stdout.String(), err)
			}
		})
	}
}

func TestCmdDoctorRejectsArgumentsLocally(t *testing.T) {
	longTarget := strings.Repeat("x", 129)
	tests := [][]string{
		{"--path", "/"},
		{"--target", "entry"},
		{"--target", "entry", "--path", "http://example.test/"},
		{"--target", "entry", "--path", "/ok#fragment"},
		{"--target", "entry", "--path", "/", "--timeout", "0"},
		{"--target", "entry", "--path", "/", "--timeout", "31s"},
		{"--target", "entry", "--path", "/", "--expect", "leaf,,db"},
		{"--target", longTarget, "--path", "/"},
		{"--target", "entry", "--path", "/" + strings.Repeat("p", 2048)},
		{"--target", "entry", "--path", "/", "extra"},
	}
	for _, args := range tests {
		var stdout, stderr bytes.Buffer
		if code := cmdDoctor(args, &stdout, &stderr); code != 2 {
			t.Errorf("args %q: exit=%d, want 2; stdout=%s stderr=%s", args, code, stdout.String(), stderr.String())
		}
	}
}

func TestCmdDoctorBoundsArgumentsBeforeCallingAPI(t *testing.T) {
	many := make([]string, 65)
	for i := range many {
		many[i] = fmt.Sprintf("s%d", i)
	}
	tests := []struct {
		args []string
		want string
	}{
		{args: []string{"--target", strings.Repeat("x", 129), "--path", "/"}, want: "target"},
		{args: []string{"--target", "entry", "--path", "/" + strings.Repeat("p", 2048)}, want: "path"},
		{args: []string{"--target", "entry", "--path", "/", "--expect", strings.Join(many, ",")}, want: "expect"},
	}
	for _, tc := range tests {
		var stdout, stderr bytes.Buffer
		if code := cmdDoctor(tc.args, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "must not exceed") {
			t.Errorf("args %q: exit=%d stderr=%q, want local %s bound error", tc.args, code, stderr.String(), tc.want)
		}
	}
}

func TestCmdDoctorAPIFailureAndUnknownVerdictExitTwo(t *testing.T) {
	t.Run("API error", func(t *testing.T) {
		ts := doctorResultServer(t, serverapi.DoctorResponse{}, http.StatusBadRequest)
		var stdout, stderr bytes.Buffer
		if code := cmdDoctor([]string{"--api-url", ts.URL, "--target", "entry", "--path", "/"}, &stdout, &stderr); code != 2 {
			t.Fatalf("exit=%d, stdout=%s stderr=%s", code, stdout.String(), stderr.String())
		}
	})
	t.Run("unknown verdict", func(t *testing.T) {
		ts := doctorResultServer(t, serverapi.DoctorResponse{Verdict: "maybe", Target: "entry", Path: "/"}, http.StatusOK)
		var stdout, stderr bytes.Buffer
		if code := cmdDoctor([]string{"--api-url", ts.URL, "--target", "entry", "--path", "/"}, &stdout, &stderr); code != 2 {
			t.Fatalf("exit=%d, stdout=%s stderr=%s", code, stdout.String(), stderr.String())
		}
	})
}
