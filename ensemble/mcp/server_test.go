package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/caribou-crew/ensemble/core/proxy"
	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/ensemble/mcp"
	ensembleServer "github.com/caribou-crew/ensemble/ensemble/server"
)

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func initializedInput(messages ...string) string {
	all := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
	}
	all = append(all, messages...)
	return strings.Join(all, "\n") + "\n"
}

func decodeToolResult(t *testing.T, response rpcResponse) struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StructuredContent map[string]any `json:"structuredContent"`
	IsError           bool           `json:"isError"`
} {
	t.Helper()
	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StructuredContent map[string]any `json:"structuredContent"`
		IsError           bool           `json:"isError"`
	}
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatalf("decode tool result: %v", err)
	}
	return result
}

func serve(t *testing.T, apiURL, input string) []rpcResponse {
	t.Helper()
	var output bytes.Buffer
	if err := mcp.Serve(context.Background(), strings.NewReader(input), &output, apiURL); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	dec := json.NewDecoder(&output)
	var responses []rpcResponse
	for {
		var response rpcResponse
		if err := dec.Decode(&response); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("decode protocol output %q: %v", output.String(), err)
		}
		responses = append(responses, response)
	}
	return responses
}

func TestServeLifecycleListsAndCallsFixedReadOnlyTools(t *testing.T) {
	type seenRequest struct {
		method string
		path   string
		query  url.Values
	}
	seen := make(chan seenRequest, 1)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- seenRequest{method: r.Method, path: r.URL.Path, query: r.URL.Query()}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"scope":{"source":"live_ring"},"requests":[{"sequence":7,"traceId":"trace-7"}]}`)
	}))
	t.Cleanup(api.Close)

	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1.50e2,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":"list-id","method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"ensemble_requests","arguments":{"service":"edge/gateway","path":"/orders?q=a b","errorsOnly":true,"minDurationMs":12.5,"limit":8}}}`,
	}, "\n") + "\n"

	responses := serve(t, api.URL, input)
	if len(responses) != 3 {
		t.Fatalf("responses = %d, want 3 (initialized notification must be silent)", len(responses))
	}
	if got := string(responses[0].ID); got != "1.50e2" {
		t.Errorf("initialize id = %s, want exact input token 1.50e2", got)
	}
	var initialized struct {
		ProtocolVersion string `json:"protocolVersion"`
		Capabilities    struct {
			Tools struct {
				ListChanged bool `json:"listChanged"`
			} `json:"tools"`
		} `json:"capabilities"`
		ServerInfo struct {
			Name string `json:"name"`
		} `json:"serverInfo"`
	}
	if err := json.Unmarshal(responses[0].Result, &initialized); err != nil {
		t.Fatalf("decode initialize result: %v", err)
	}
	if initialized.ProtocolVersion != "2025-11-25" || initialized.ServerInfo.Name != "ensemble" {
		t.Errorf("initialize result = %+v", initialized)
	}
	if initialized.Capabilities.Tools.ListChanged {
		t.Error("static tool catalog advertised listChanged")
	}

	if got := string(responses[1].ID); got != `"list-id"` {
		t.Errorf("list id = %s", got)
	}
	var listed struct {
		Tools []struct {
			Name        string `json:"name"`
			InputSchema struct {
				AdditionalProperties bool `json:"additionalProperties"`
			} `json:"inputSchema"`
			Annotations struct {
				ReadOnlyHint    bool `json:"readOnlyHint"`
				DestructiveHint bool `json:"destructiveHint"`
				OpenWorldHint   bool `json:"openWorldHint"`
			} `json:"annotations"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(responses[1].Result, &listed); err != nil {
		t.Fatalf("decode tools/list: %v", err)
	}
	if len(listed.Tools) != 2 || listed.Tools[0].Name != "ensemble_requests" || listed.Tools[1].Name != "ensemble_explain_trace" {
		t.Fatalf("tool names = %+v", listed.Tools)
	}
	for _, tool := range listed.Tools {
		if !tool.Annotations.ReadOnlyHint || tool.Annotations.DestructiveHint || tool.Annotations.OpenWorldHint {
			t.Errorf("tool %q annotations = %+v", tool.Name, tool.Annotations)
		}
		if tool.InputSchema.AdditionalProperties {
			t.Errorf("tool %q accepts unspecified properties", tool.Name)
		}
	}

	request := <-seen
	if request.method != http.MethodGet || request.path != "/api/observability/requests" {
		t.Fatalf("API request = %s %s, want fixed GET /api/observability/requests", request.method, request.path)
	}
	wantQuery := url.Values{
		"service":       {"edge/gateway"},
		"path":          {"/orders?q=a b"},
		"errorsOnly":    {"true"},
		"minDurationMs": {"12.5"},
		"limit":         {"8"},
	}
	if request.query.Encode() != wantQuery.Encode() {
		t.Errorf("API query = %q, want %q", request.query.Encode(), wantQuery.Encode())
	}

	var called struct {
		StructuredContent map[string]any `json:"structuredContent"`
		Content           []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(responses[2].Result, &called); err != nil {
		t.Fatalf("decode tools/call: %v", err)
	}
	if called.IsError || called.StructuredContent["scope"] == nil {
		t.Errorf("tool result = %+v", called)
	}
	if len(called.Content) != 1 || called.Content[0].Type != "text" {
		t.Fatalf("content = %+v", called.Content)
	}
	var textContent map[string]any
	if err := json.Unmarshal([]byte(called.Content[0].Text), &textContent); err != nil {
		t.Fatalf("text content is not JSON: %q: %v", called.Content[0].Text, err)
	}
	if textContent["scope"] == nil {
		t.Errorf("text content = %+v", textContent)
	}
}

func TestServeCallsRealEnsembleObservationAPI(t *testing.T) {
	recorder := proxy.NewRecorder(proxy.RecorderOpts{})
	t.Cleanup(recorder.Close)
	recorder.Record(trace.Hop{TraceID: "", SpanID: "ambient", To: "edge", Method: "GET", Path: "/ambient", Status: 200})
	recorder.Record(trace.Hop{TraceID: "team/a", SpanID: "observed", To: "edge", Method: "GET", Path: "/trace", Status: 503})
	api := httptest.NewServer(ensembleServer.New(ensembleServer.Deps{Rec: recorder}))
	t.Cleanup(api.Close)

	responses := serve(t, api.URL, initializedInput(
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"ensemble_requests","arguments":{"path":"/ambient"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"ensemble_explain_trace","arguments":{"traceId":"team/a"}}}`,
	))
	if len(responses) != 3 {
		t.Fatalf("responses = %d, want 3", len(responses))
	}
	requests := decodeToolResult(t, responses[1])
	if requests.IsError {
		t.Fatalf("requests tool failed: %+v", requests)
	}
	hops, ok := requests.StructuredContent["hops"].([]any)
	if !ok || len(hops) != 1 || hops[0].(map[string]any)["traceId"] != "" {
		t.Fatalf("ambient request was not preserved without a trace ID: %+v", requests.StructuredContent)
	}
	explanation := decodeToolResult(t, responses[2])
	if explanation.IsError || explanation.StructuredContent["traceId"] != "team/a" {
		t.Fatalf("trace explanation = %+v", explanation)
	}
}

func TestServeNegotiatesCompatibleProtocolVersionsAndPings(t *testing.T) {
	for _, requested := range []string{"2025-11-25", "2025-06-18", "2024-11-05", "2099-01-01"} {
		t.Run(requested, func(t *testing.T) {
			input := `{"jsonrpc":"2.0","id":"init","method":"initialize","params":{"protocolVersion":` + strconv.Quote(requested) + `,"capabilities":{},"clientInfo":{"name":"test","version":"1"}}}` + "\n" +
				`{"jsonrpc":"2.0","id":2,"method":"ping"}` + "\n"
			responses := serve(t, "http://127.0.0.1:1", input)
			if len(responses) != 2 || responses[0].Error != nil || responses[1].Error != nil {
				t.Fatalf("responses = %+v", responses)
			}
			var initialized struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			if err := json.Unmarshal(responses[0].Result, &initialized); err != nil {
				t.Fatal(err)
			}
			want := requested
			if requested == "2099-01-01" {
				want = "2025-11-25"
			}
			if initialized.ProtocolVersion != want {
				t.Errorf("negotiated = %q, want %q", initialized.ProtocolVersion, want)
			}
		})
	}
}

func TestServeProtocolErrorsAndNotifications(t *testing.T) {
	input := initializedInput(
		`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":99}}`,
		`{"jsonrpc":"2.0","method":"unknown/notification"}`,
		`{"jsonrpc":"2.0","id":"method-id","method":"resources/list"}`,
		`{"jsonrpc":"2.0","id":9,"method":"tools/list","params":{"cursor":"unexpected"}}`,
		`{"jsonrpc":"2.0","id":false,"method":"ping"}`,
		`{"jsonrpc":`,
	)
	responses := serve(t, "http://127.0.0.1:1", input)
	if len(responses) != 5 {
		t.Fatalf("responses = %d, want initialize plus four request/errors; notifications must be silent", len(responses))
	}
	wants := []struct {
		id   string
		code int
	}{
		{`"method-id"`, -32601},
		{"9", -32602},
		{"null", -32600},
		{"null", -32700},
	}
	for i, want := range wants {
		got := responses[i+1]
		if string(got.ID) != want.id || got.Error == nil || got.Error.Code != want.code {
			t.Errorf("response %d = id %s error %+v, want id %s code %d", i+1, got.ID, got.Error, want.id, want.code)
		}
	}
}

func TestServeRejectsNonUTF8TransportMessage(t *testing.T) {
	input := append([]byte(`{"jsonrpc":"2.0","id":1,"method":"`), 0xff)
	input = append(input, []byte(`"}`+"\n")...)
	responses := serve(t, "http://127.0.0.1:1", string(input))
	if len(responses) != 1 || responses[0].Error == nil || responses[0].Error.Code != -32700 {
		t.Fatalf("response = %+v, want parse error for invalid UTF-8", responses)
	}
}

func TestServeRejectsToolArgumentsWithoutCallingAPI(t *testing.T) {
	var calls atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.WriteString(w, `{}`)
	}))
	t.Cleanup(api.Close)

	messages := []string{
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"ensemble_explain_trace","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"ensemble_explain_trace","arguments":{"traceId":"   "}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"ensemble_requests","arguments":{"limit":101}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"ensemble_requests","arguments":{"minDurationMs":-0.1}}}`,
		`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"ensemble_requests","arguments":{"url":"http://attacker.invalid","method":"POST"}}}`,
		`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"ensemble_requests","arguments":{"limit":null}}}`,
		`{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"ensemble_requests","arguments":{"errorsOnly":null}}}`,
	}
	responses := serve(t, api.URL, initializedInput(messages...))
	if calls.Load() != 0 {
		t.Fatalf("invalid calls reached API %d times", calls.Load())
	}
	if len(responses) != len(messages)+1 {
		t.Fatalf("responses = %d, want %d", len(responses), len(messages)+1)
	}
	for i, response := range responses[1:] {
		result := decodeToolResult(t, response)
		if !result.IsError || len(result.Content) != 1 {
			t.Errorf("invalid call %d result = %+v", i, result)
		}
	}
}

func TestServeRejectsNullOrNonObjectToolArgumentsAsProtocolErrors(t *testing.T) {
	responses := serve(t, "http://127.0.0.1:1", initializedInput(
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"ensemble_requests","arguments":null}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"ensemble_requests","arguments":[]}}`,
	))
	for _, response := range responses[1:] {
		if response.Error == nil || response.Error.Code != -32602 {
			t.Errorf("response = %+v, want invalid params protocol error", response)
		}
	}
}

func TestServeExplainTraceUsesEscapedFixedGET(t *testing.T) {
	requestURI := make(chan string, 1)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		requestURI <- r.RequestURI
		io.WriteString(w, `{"traceId":"team/a?b #c","hops":[]}`)
	}))
	t.Cleanup(api.Close)

	responses := serve(t, api.URL, initializedInput(
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"ensemble_explain_trace","arguments":{"traceId":"team/a?b #c","limit":17}}}`,
	))
	if result := decodeToolResult(t, responses[1]); result.IsError {
		t.Fatalf("tool result = %+v", result)
	}
	if got, want := <-requestURI, "/api/observability/traces/team%2Fa%3Fb%20%23c?limit=17"; got != want {
		t.Errorf("RequestURI = %q, want %q", got, want)
	}
}

func TestServeEncodesDotOnlyTracePathSegments(t *testing.T) {
	requestURIs := make(chan string, 2)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestURIs <- r.RequestURI
		io.WriteString(w, `{}`)
	}))
	t.Cleanup(api.Close)

	responses := serve(t, api.URL, initializedInput(
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"ensemble_explain_trace","arguments":{"traceId":"."}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"ensemble_explain_trace","arguments":{"traceId":".."}}}`,
	))
	for _, response := range responses[1:] {
		if result := decodeToolResult(t, response); result.IsError {
			t.Fatalf("dot trace call failed: %+v", result)
		}
	}
	for _, want := range []string{"/api/observability/traces/%2E", "/api/observability/traces/%2E%2E"} {
		if got := <-requestURIs; got != want {
			t.Errorf("RequestURI = %q, want %q", got, want)
		}
	}
}

func TestServeRESTFailuresAreToolErrorsAndRedirectsAreNotFollowed(t *testing.T) {
	var redirected atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("service") {
		case "redirect":
			http.Redirect(w, r, "/write-like-target", http.StatusFound)
		case "http-error":
			http.Error(w, "private upstream details", http.StatusServiceUnavailable)
		default:
			redirected.Add(1)
			io.WriteString(w, `{}`)
		}
	}))
	t.Cleanup(api.Close)

	responses := serve(t, api.URL, initializedInput(
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"ensemble_requests","arguments":{"service":"redirect"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"ensemble_requests","arguments":{"service":"http-error"}}}`,
	))
	if redirected.Load() != 0 {
		t.Fatalf("redirect target was followed %d times", redirected.Load())
	}
	for _, response := range responses[1:] {
		result := decodeToolResult(t, response)
		if !result.IsError || len(result.Content) != 1 {
			t.Errorf("REST failure result = %+v", result)
		}
		if strings.Contains(result.Content[0].Text, "private upstream details") {
			t.Errorf("REST error leaked response body: %q", result.Content[0].Text)
		}
	}
}

func TestServeBoundsInputAndHTTPResponseAndContinuesAfterOversizedMessage(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Derived independently from the specified 4 MiB response ceiling.
		io.WriteString(w, `{"padding":"`+strings.Repeat("x", 4<<20)+`"}`)
	}))
	t.Cleanup(api.Close)

	// Derived independently from the specified 1 MiB message ceiling.
	oversized := strings.Repeat("x", (1<<20)+1)
	input := oversized + "\n" + initializedInput(
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"ensemble_requests","arguments":{}}}`,
	)
	responses := serve(t, api.URL, input)
	if len(responses) != 3 {
		t.Fatalf("responses = %d, want oversize error, initialize response, tool error", len(responses))
	}
	if responses[0].Error == nil || responses[0].Error.Code != -32600 {
		t.Errorf("oversize response = %+v", responses[0])
	}
	result := decodeToolResult(t, responses[2])
	if !result.IsError || !strings.Contains(result.Content[0].Text, "exceeds") {
		textLen := 0
		if len(result.Content) > 0 {
			textLen = len(result.Content[0].Text)
		}
		t.Errorf("oversized HTTP response isError=%v content=%d textBytes=%d", result.IsError, len(result.Content), textLen)
	}
}

func TestServeAcceptsMessageAtExactInputLimit(t *testing.T) {
	message := `{"jsonrpc":"2.0","id":1,"method":"unknown"}`
	message += strings.Repeat(" ", (1<<20)-len(message))
	responses := serve(t, "http://127.0.0.1:1", message+"\n")
	if len(responses) != 1 || responses[0].Error == nil {
		t.Fatalf("response = %+v", responses)
	}
	if responses[0].Error.Code == -32600 {
		t.Errorf("exactly 1 MiB message was rejected as oversized: %+v", responses[0].Error)
	}
}

func TestServeRejectsInvalidAPIBaseURLBeforeReading(t *testing.T) {
	reader := readerThatFails{t: t}
	var output bytes.Buffer
	if err := mcp.Serve(context.Background(), reader, &output, "file:///tmp/socket"); err == nil {
		t.Fatal("Serve accepted non-HTTP API URL")
	}
	if output.Len() != 0 {
		t.Fatalf("protocol output = %q", output.String())
	}
}

func TestServeRejectsAPIURLCredentialsWithoutEchoingThem(t *testing.T) {
	var output bytes.Buffer
	err := mcp.Serve(context.Background(), readerThatFails{t: t}, &output, "http://api-user:secret-token@127.0.0.1:4700")
	if err == nil {
		t.Fatal("Serve accepted API URL credentials")
	}
	if strings.Contains(err.Error(), "api-user") || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("error reflected URL credentials: %v", err)
	}
}

func TestServeReportsShortProtocolWrites(t *testing.T) {
	input := `{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\n"
	if err := mcp.Serve(context.Background(), strings.NewReader(input), shortWriter{}, "http://127.0.0.1:1"); err == nil {
		t.Fatal("Serve ignored a short protocol write")
	}
}

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return 1, nil
}

type readerThatFails struct{ t *testing.T }

func (r readerThatFails) Read([]byte) (int, error) {
	r.t.Fatal("invalid API URL should be rejected before reading input")
	return 0, io.EOF
}
