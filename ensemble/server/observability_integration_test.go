package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/caribou-crew/ensemble/core/proxy"
	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/ensemble/mcp"
)

// Exercises the public MCP -> REST -> recorder path with an actual BFF -> BE
// -> provider call. Assertions observe network results, not a mocked tool map.
func TestMCPExplainsActualProxyChain(t *testing.T) {
	red, err := trace.NewRedactor(nil, 65536, nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := proxy.NewRecorder(proxy.RecorderOpts{Redactor: red})
	px := proxy.New(rec)
	t.Cleanup(px.Close)
	var next string
	for _, name := range []string{"provider", "be", "bff"} {
		downstream := next
		up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if downstream != "" {
				req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, downstream+"/data", nil)
				if err != nil {
					http.Error(w, err.Error(), 500)
					return
				}
				for _, header := range []string{"traceparent", "baggage"} {
					req.Header.Set(header, r.Header.Get(header))
				}
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					http.Error(w, err.Error(), 502)
					return
				}
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
			w.Header().Set("Content-Type", "application/json")
			if downstream == "" {
				w.WriteHeader(503)
			}
			fmt.Fprint(w, `{"payload":"not-for-agent-context","token":"capture-secret"}`)
		}))
		t.Cleanup(up.Close)
		addr, err := px.Serve(proxy.Target{Name: name, Listen: "127.0.0.1:0", Upstream: up.URL})
		if err != nil {
			t.Fatal(err)
		}
		next = "http://" + addr
	}
	resp, err := http.Get(next + "/start")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	hops := rec.Snapshot()
	if len(hops) != 3 {
		t.Fatalf("actual chain captured %d hops", len(hops))
	}
	input := bytes.Buffer{}
	enc := json.NewEncoder(&input)
	for _, v := range []any{
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "integration-test", "version": "1"}}},
		map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"},
		map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": "ensemble_requests", "arguments": map[string]any{"errorsOnly": true}}},
		map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": map[string]any{"name": "ensemble_explain_trace", "arguments": map[string]any{"traceId": hops[0].TraceID}}},
	} {
		if err := enc.Encode(v); err != nil {
			t.Fatal(err)
		}
	}
	var output bytes.Buffer
	if err := mcp.Serve(context.Background(), &input, &output, observationAPI(t, rec)); err != nil {
		t.Fatal(err)
	}
	raw := output.String()
	for _, secret := range []string{"not-for-agent-context", "capture-secret"} {
		if strings.Contains(raw, secret) {
			t.Fatalf("tool leaked payload %s", secret)
		}
	}
	dec := json.NewDecoder(&output)
	for index := 1; index <= 3; index++ {
		var message struct {
			ID     int `json:"id"`
			Error  any `json:"error"`
			Result struct {
				IsError bool           `json:"isError"`
				Data    map[string]any `json:"structuredContent"`
			} `json:"result"`
		}
		if err := dec.Decode(&message); err != nil {
			t.Fatalf("response %d: %v\n%s", index, err, raw)
		}
		if message.ID != index || message.Error != nil || message.Result.IsError {
			t.Fatalf("RPC failure: %s", raw)
		}
		if index == 2 {
			rows := message.Result.Data["hops"].([]any)
			if len(rows) != 1 || rows[0].(map[string]any)["to"] != "provider" {
				t.Fatalf("wrong observed failure: %s", raw)
			}
		}
		if index == 3 {
			data := message.Result.Data
			if data["observedHops"] != float64(3) || data["scope"].(map[string]any)["completeness"] != "unknown" {
				t.Fatalf("trace evidence incorrect: %s", raw)
			}
			if !strings.Contains(raw, "http-error") {
				t.Fatalf("missing provider error finding: %s", raw)
			}
		}
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		t.Fatalf("unexpected protocol output: %v", err)
	}
}
