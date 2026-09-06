package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/core/trace"
)

func writeSequenceHistory(t *testing.T, path string, seqs ...uint64) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := trace.NewWriter(f)
	for _, seq := range seqs {
		if err := w.Write(trace.Hop{Seq: seq, Method: "GET", Path: fmt.Sprintf("/old/%d", seq), Status: 200}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRunUpResumesTrafficSequenceAfterRetainedHistory(t *testing.T) {
	dir := t.TempDir()
	upPort, proxyPort, apiPort := freePort(t), freePort(t), freePort(t)
	cfgPath := writeConfig(t, dir, upPort, proxyPort)
	logPath := filepath.Join(dir, ".ensemble", "hops.jsonl")
	// Highest seq is neither the final line nor in the current generation.
	writeSequenceHistory(t, logPath, 1000, 999)
	writeSequenceHistory(t, logPath+".1", 2000, 1001)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- runUp(ctx, upOptions{ConfigPath: cfgPath, Addr: fmt.Sprintf("127.0.0.1:%d", apiPort)}, &bytes.Buffer{}, &bytes.Buffer{})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-result:
			if err != nil {
				t.Errorf("runUp: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("runUp did not stop")
		}
	})
	startStandinBackend(t, upPort, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	apiURL := fmt.Sprintf("http://127.0.0.1:%d", apiPort)
	waitHealthy(t, apiURL)
	waitServiceHealthy(t, NewClient(apiURL), "svc")
	client := &http.Client{Timeout: time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/current", proxyPort))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("current request status = %d", resp.StatusCode)
	}

	readPage := func(query string) ([]trace.Hop, bool) {
		t.Helper()
		resp, err := client.Get(apiURL + "/api/traffic/history" + query)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var page struct {
			Hops    []trace.Hop `json:"hops"`
			HasMore bool        `json:"hasMore"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
			t.Fatal(err)
		}
		return page.Hops, page.HasMore
	}
	// Persistence is asynchronous; wait for the actual new hop to reach disk.
	deadline := time.Now().Add(2 * time.Second)
	for {
		hops, _ := readPage("?limit=10")
		found := false
		for _, h := range hops {
			if h.Path == "/current" {
				found = true
				if h.Seq != 2001 {
					t.Fatalf("restarted recorder assigned seq %d, want 2001 after retained maximum", h.Seq)
				}
			}
		}
		if found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("current request never reached history")
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, tc := range []struct {
		query string
		seqs  []uint64
		more  bool
	}{
		{"?limit=2", []uint64{2001, 1000}, true},
		{"?limit=2&before=1000", []uint64{999}, false},
	} {
		hops, more := readPage(tc.query)
		var seqs []uint64
		for _, h := range hops {
			seqs = append(seqs, h.Seq)
		}
		if !slices.Equal(seqs, tc.seqs) || more != tc.more {
			t.Fatalf("history%s = %v hasMore=%v, want %v hasMore=%v", tc.query, seqs, more, tc.seqs, tc.more)
		}
	}
}
