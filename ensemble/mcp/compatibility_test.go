package mcp_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestServeAcceptsStandardRequestMetadata(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"hops":[]}`) }))
	defer api.Close()
	input := initializedInput(
		`{"jsonrpc":"2.0","id":2,"method":"ping","params":{"_meta":{"custom":null}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/list","params":{"_meta":{}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"ensemble_requests","arguments":{},"_meta":{"progressToken":"p1","extension":{"opaque":true}}}}`,
	)
	input = strings.Replace(input, `"method":"notifications/initialized"}`, `"method":"notifications/initialized","params":{"_meta":{"client":"integration"}}}`, 1)
	responses := serve(t, api.URL, input)
	if len(responses) != 4 {
		t.Fatalf("response count=%d, want4", len(responses))
	}
	for _, response := range responses {
		if response.Error != nil {
			t.Errorf("standard metadata rejected for id%s: %+v", response.ID, response.Error)
		}
	}
}

func TestServeIDsRequireExactIntegerValues(t *testing.T) {
	for _, id := range []string{`"id"`, `9007199254740993`, `1.0`, `1e3`, `100e-2`, `0e-999999999999999999999999999999`, `1e99999999999999999999999999`} {
		t.Run(id, func(t *testing.T) {
			responses := serve(t, "http://127.0.0.1:1", `{"jsonrpc":"2.0","id":`+id+`,"method":"ping"}`+"\n")
			if len(responses) != 1 || responses[0].Error != nil || string(responses[0].ID) != id {
				t.Fatalf("integer/string ID not preserved: %+v", responses)
			}
		})
	}
	for _, id := range []string{`null`, `1.25`, `1e-3`, `9007199254740993.1`, `1e-99999999999999999999999999`} {
		t.Run(id, func(t *testing.T) {
			responses := serve(t, "http://127.0.0.1:1", `{"jsonrpc":"2.0","id":`+id+`,"method":"ping"}`+"\n")
			if len(responses) != 1 || responses[0].Error == nil || responses[0].Error.Code != -32600 {
				t.Fatalf("invalid ID accepted: %+v", responses)
			}
		})
	}
}
