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

func TestRunUpPreservesFirstHopAfterUnterminatedHistory(t *testing.T) {
	for _, kind := range []string{"complete record", "partial record"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			upPort, proxyPort, apiPort := freePort(t), freePort(t), freePort(t)
			cfgPath := writeConfig(t, dir, upPort, proxyPort)
			path := filepath.Join(dir, ".ensemble", "hops.jsonl")
			writeSequenceHistory(t, path, 20)
			tail := []byte(`{"seq":21`)
			wantSeqs := []uint64{20, 21}
			wantCorrupt := 1
			if kind == "complete record" {
				var err error
				tail, err = json.Marshal(trace.Hop{Schema: trace.SchemaVersion, Seq: 21, Path: "/old/21"})
				if err != nil {
					t.Fatal(err)
				}
				wantSeqs = []uint64{20, 21, 22}
				wantCorrupt = 0
			}
			f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.Write(tail); err != nil {
				t.Fatal(err)
			}
			f.Close()
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			result := make(chan error, 1)
			go func() {
				result <- runUp(ctx, upOptions{ConfigPath: cfgPath, Addr: fmt.Sprintf("127.0.0.1:%d", apiPort)}, &bytes.Buffer{}, &bytes.Buffer{})
			}()
			stopped := false
			stop := func() {
				cancel()
				if stopped {
					return
				}
				stopped = true
				select {
				case err := <-result:
					if err != nil {
						t.Errorf("runUp: %v", err)
					}
				case <-time.After(5 * time.Second):
					t.Error("runUp did not stop")
				}
			}
			t.Cleanup(stop)
			startStandinBackend(t, upPort, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "ok") }))
			apiURL := fmt.Sprintf("http://127.0.0.1:%d", apiPort)
			waitHealthy(t, apiURL)
			waitServiceHealthy(t, NewClient(apiURL), "svc")
			resp, err := (&http.Client{Timeout: time.Second}).Get(fmt.Sprintf("http://127.0.0.1:%d/first-after-restart", proxyPort))
			if err != nil {
				t.Fatal(err)
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("forwarded request status=%d", resp.StatusCode)
			}
			stop() // Flush the recorder before inspecting the recovered file.
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			prefix := append(append([]byte(nil), original...), '\n')
			if !bytes.HasPrefix(raw, prefix) {
				t.Error("startup did not preserve prior bytes and terminate their final line")
			}
			rd := trace.NewReader(bytes.NewReader(raw))
			var seqs []uint64
			corrupt := 0
			found := false
			for {
				h, err := rd.Next()
				if err == trace.ErrEOF {
					break
				}
				if err != nil {
					corrupt++
					continue
				}
				seqs = append(seqs, h.Seq)
				found = found || h.Path == "/first-after-restart"
			}
			if !slices.Equal(seqs, wantSeqs) || corrupt != wantCorrupt || !found {
				t.Errorf("readable sequences=%v corrupt=%d newHop=%v, want %v corrupt=%d and new hop", seqs, corrupt, found, wantSeqs, wantCorrupt)
			}
		})
	}
}
