package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestAPIHostPolicy pins both halves of the bind decision: which Host
// headers the browser guard will accept, and whether the user is warned
// that they just published an unauthenticated control plane.
func TestAPIHostPolicy(t *testing.T) {
	cases := []struct {
		addr      string
		wantHosts []string
		wantWarn  bool
	}{
		{addr: "127.0.0.1:4700", wantHosts: nil, wantWarn: false},
		{addr: "localhost:4700", wantHosts: nil, wantWarn: false},
		{addr: "[::1]:4700", wantHosts: nil, wantWarn: false},
		{addr: ":4700", wantHosts: []string{"*"}, wantWarn: true},
		{addr: "0.0.0.0:4700", wantHosts: []string{"*"}, wantWarn: true},
		{addr: "[::]:4700", wantHosts: []string{"*"}, wantWarn: true},
		{addr: "192.168.1.20:4700", wantHosts: []string{"192.168.1.20"}, wantWarn: true},
		{addr: "dev-box.local:4700", wantHosts: []string{"dev-box.local"}, wantWarn: true},
	}
	for _, tc := range cases {
		hosts, warning := apiHostPolicy(tc.addr)
		if !reflect.DeepEqual(hosts, tc.wantHosts) {
			t.Errorf("apiHostPolicy(%q) hosts = %v, want %v", tc.addr, hosts, tc.wantHosts)
		}
		if got := warning != ""; got != tc.wantWarn {
			t.Errorf("apiHostPolicy(%q) warned = %v, want %v", tc.addr, got, tc.wantWarn)
		}
	}
}

// TestUp_WarnsOnExposedBind proves the warning actually reaches the user
// on a wide bind — the policy table above only proves the string exists.
func TestUp_WarnsOnExposedBind(t *testing.T) {
	if _, warning := apiHostPolicy("0.0.0.0:4700"); !strings.Contains(warning, "unauthenticated") {
		t.Fatalf("exposure warning does not mention it is unauthenticated: %q", warning)
	}
}

// TestUp_HopLogIsNotWorldReadable: .ensemble/hops.jsonl holds verbatim
// captured request/response bodies, so it must not be readable by other
// users on the machine.
func TestUp_HopLogIsNotWorldReadable(t *testing.T) {
	upPort := freePort(t) // stand-in backend bound after runUp's preflight check — see startStandinBackend

	// Own dir (rather than startEnsemble's, which keeps its temp dir to
	// itself) so the .ensemble directory runUp creates can be stat'ed.
	dir := t.TempDir()
	cfgPath := writeConfig(t, dir, upPort, freePort(t))
	apiPort := freePort(t)

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	go func() {
		result <- runUp(ctx, upOptions{
			ConfigPath: cfgPath,
			Addr:       fmt.Sprintf("127.0.0.1:%d", apiPort),
		}, stdout, stderr)
	}()
	startStandinBackend(t, upPort, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello from svc"))
	}))
	waitHealthy(t, "http://127.0.0.1:"+strconv.Itoa(apiPort))
	defer func() {
		cancel()
		select {
		case <-result:
		case <-time.After(5 * time.Second):
			t.Error("runUp did not return in time")
		}
	}()

	hops := filepath.Join(dir, ".ensemble", "hops.jsonl")
	fi, err := os.Stat(hops)
	if err != nil {
		t.Fatalf("stat hops log: %v", err)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("hops.jsonl mode = %#o, want no group/other bits", perm)
	}

	di, err := os.Stat(filepath.Dir(hops))
	if err != nil {
		t.Fatalf("stat .ensemble dir: %v", err)
	}
	if perm := di.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf(".ensemble mode = %#o, want no group/other bits", perm)
	}
}
