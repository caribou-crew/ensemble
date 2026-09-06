// Package mcp exposes Ensemble's bounded, read-only observability API as an
// MCP stdio server. The transport is deliberately small: one JSON-RPC message
// per line and exactly two tools, both backed by fixed HTTP GET endpoints.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// MaxMessageBytes is the largest JSON-RPC message accepted on the stdio
	// transport, excluding its newline delimiter.
	MaxMessageBytes = 1 << 20
	// MaxHTTPResponseBytes bounds a successful or failed control-plane
	// response before it can be copied into a tool result.
	MaxHTTPResponseBytes = 4 << 20

	httpTimeout = 10 * time.Second
)

const (
	protocolLatest = "2025-11-25"
	protocolJune   = "2025-06-18"
	protocolLegacy = "2024-11-05"
)

var errMessageTooLarge = errors.New("JSON-RPC message exceeds size limit")

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type server struct {
	baseURL     *url.URL
	http        *http.Client
	initialized bool
	ready       bool
	output      io.Writer
}

// Serve runs an MCP server over input and output until input reaches EOF.
// output receives protocol messages only. apiURL is the Ensemble control
// plane base URL; all tool requests are built from fixed endpoint paths.
func Serve(ctx context.Context, input io.Reader, output io.Writer, apiURL string) error {
	baseURL, err := parseBaseURL(apiURL)
	if err != nil {
		return err
	}
	s := &server{
		baseURL: baseURL,
		http: &http.Client{
			Timeout: httpTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		output: output,
	}
	reader := bufio.NewReaderSize(input, 64*1024)
	for {
		line, readErr := readBoundedLine(reader)
		if errors.Is(readErr, io.EOF) && len(line) == 0 {
			return nil
		}
		if errors.Is(readErr, errMessageTooLarge) {
			if err := s.writeError(nil, -32600, errMessageTooLarge.Error()); err != nil {
				return err
			}
		} else if readErr != nil && !errors.Is(readErr, io.EOF) {
			return fmt.Errorf("read MCP input: %w", readErr)
		} else if err := s.handle(ctx, line); err != nil {
			return err
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
	}
}

func parseBaseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimRight(raw, "/"))
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("invalid Ensemble API URL")
	}
	return u, nil
}

func readBoundedLine(r *bufio.Reader) ([]byte, error) {
	var line []byte
	tooLarge := false
	for {
		fragment, err := r.ReadSlice('\n')
		if err == nil {
			fragment = fragment[:len(fragment)-1]
		}
		remaining := MaxMessageBytes + 1 - len(line)
		if remaining > 0 {
			if len(fragment) > remaining {
				line = append(line, fragment[:remaining]...)
			} else {
				line = append(line, fragment...)
			}
		}
		if len(line) > MaxMessageBytes {
			tooLarge = true
		}
		if err == nil || errors.Is(err, io.EOF) {
			line = bytes.TrimSuffix(line, []byte{'\r'})
			if tooLarge {
				return nil, errMessageTooLarge
			}
			return line, err
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return nil, err
		}
	}
}

func (s *server) handle(ctx context.Context, line []byte) error {
	var req request
	if !utf8.Valid(line) || len(bytes.TrimSpace(line)) == 0 || json.Unmarshal(line, &req) != nil {
		return s.writeError(nil, -32700, "Parse error")
	}

	validEnvelope := req.JSONRPC == "2.0" && req.Method != "" && validID(req.ID)
	isNotification := validEnvelope && req.ID == nil
	if !validEnvelope {
		return s.writeError(nil, -32600, "Invalid Request")
	}
	if isNotification {
		s.handleNotification(req)
		return nil
	}

	switch req.Method {
	case "initialize":
		return s.initialize(req)
	case "ping":
		if !emptyParams(req.Params) {
			return s.writeError(req.ID, -32602, "Invalid params")
		}
		return s.writeResult(req.ID, map[string]any{})
	}
	if !s.initialized || !s.ready {
		return s.writeError(req.ID, -32002, "Server not initialized")
	}
	if req.Method == "tools/list" {
		if !emptyParams(req.Params) {
			return s.writeError(req.ID, -32602, "Invalid params")
		}
		return s.writeResult(req.ID, map[string]any{"tools": tools()})
	}
	if req.Method == "tools/call" {
		return s.callTool(ctx, req)
	}
	return s.writeError(req.ID, -32601, "Method not found")
}

func validID(id json.RawMessage) bool {
	if id == nil {
		return true
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(id))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return false
	}
	switch v := value.(type) {
	case string:
		return true
	case json.Number:
		return integerID(string(v))
	default:
		return false
	}
}

// MCP IDs are strings or mathematical integers. Check decimal scale exactly:
// float64 would accept a rounded fractional ID, and expanding a giant exponent
// as a rational would allow a small input to allocate arbitrarily large memory.
func integerID(raw string) bool {
	coefficient, power, _ := strings.Cut(strings.TrimPrefix(strings.ToLower(raw), "-"), "e")
	fraction := 0
	if dot := strings.IndexByte(coefficient, '.'); dot >= 0 {
		fraction = len(coefficient) - dot - 1
		coefficient = coefficient[:dot] + coefficient[dot+1:]
	}
	trimmed := strings.TrimRight(coefficient, "0")
	if trimmed == "" {
		return true
	}
	var exponent big.Int
	if power != "" {
		if _, ok := exponent.SetString(power, 10); !ok {
			return false
		}
	}
	required := int64(fraction - (len(coefficient) - len(trimmed)))
	return exponent.Cmp(big.NewInt(required)) >= 0
}

func emptyParams(raw json.RawMessage) bool {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return true
	}
	var params map[string]json.RawMessage
	if json.Unmarshal(raw, &params) != nil {
		return false
	}
	if meta, ok := params["_meta"]; ok {
		if !jsonObject(meta) {
			return false
		}
		delete(params, "_meta")
	}
	return len(params) == 0
}

func (s *server) handleNotification(req request) {
	if req.Method == "notifications/initialized" && s.initialized && emptyParams(req.Params) {
		s.ready = true
	}
}

func (s *server) initialize(req request) error {
	if s.initialized {
		return s.writeError(req.ID, -32600, "Already initialized")
	}
	var params struct {
		Meta            json.RawMessage `json:"_meta"`
		ProtocolVersion string          `json:"protocolVersion"`
		Capabilities    json.RawMessage `json:"capabilities"`
		ClientInfo      struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"clientInfo"`
	}
	if json.Unmarshal(req.Params, &params) != nil || params.ProtocolVersion == "" ||
		(len(params.Meta) != 0 && !jsonObject(params.Meta)) ||
		!jsonObject(params.Capabilities) || params.ClientInfo.Name == "" || params.ClientInfo.Version == "" {
		return s.writeError(req.ID, -32602, "Invalid params")
	}
	negotiated := protocolLatest
	if supportedProtocol(params.ProtocolVersion) {
		negotiated = params.ProtocolVersion
	}
	s.initialized = true
	return s.writeResult(req.ID, map[string]any{
		"protocolVersion": negotiated,
		"capabilities": map[string]any{
			"tools": map[string]any{"listChanged": false},
		},
		"serverInfo": map[string]any{
			"name":    "ensemble",
			"version": "dev",
		},
	})
}

func supportedProtocol(version string) bool {
	return version == protocolLatest || version == protocolJune || version == protocolLegacy
}

func jsonObject(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var object map[string]json.RawMessage
	return json.Unmarshal(raw, &object) == nil && object != nil
}

type toolDefinition struct {
	Name        string         `json:"name"`
	Title       string         `json:"title"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations annotations    `json:"annotations"`
}

type annotations struct {
	ReadOnlyHint    bool `json:"readOnlyHint"`
	DestructiveHint bool `json:"destructiveHint"`
	IdempotentHint  bool `json:"idempotentHint"`
	OpenWorldHint   bool `json:"openWorldHint"`
}

func tools() []toolDefinition {
	readOnly := annotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false}
	return []toolDefinition{
		{
			Name:        "ensemble_requests",
			Title:       "Search recent Ensemble requests",
			Description: "Search bounded request summaries in Ensemble's current live ring.",
			InputSchema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"service":       map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
					"path":          map[string]any{"type": "string", "minLength": 1, "maxLength": 512},
					"errorsOnly":    map[string]any{"type": "boolean"},
					"minDurationMs": map[string]any{"type": "number", "minimum": 0},
					"limit":         map[string]any{"type": "integer", "minimum": 1, "maximum": 100},
				},
			},
			Annotations: readOnly,
		},
		{
			Name:        "ensemble_explain_trace",
			Title:       "Explain an Ensemble trace",
			Description: "Explain observed hops and capture-quality evidence for one trace in Ensemble's current live ring.",
			InputSchema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"traceId": map[string]any{"type": "string", "minLength": 1, "maxLength": 256},
					"limit":   map[string]any{"type": "integer", "minimum": 1, "maximum": 200},
				},
				"required": []string{"traceId"},
			},
			Annotations: readOnly,
		},
	}
}

type callParams struct {
	Meta      json.RawMessage `json:"_meta"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func (s *server) callTool(ctx context.Context, req request) error {
	var params callParams
	if err := decodeStrict(req.Params, &params); err != nil || params.Name == "" ||
		(len(params.Meta) != 0 && !jsonObject(params.Meta)) {
		return s.writeError(req.ID, -32602, "Invalid params")
	}
	if len(params.Arguments) == 0 {
		params.Arguments = json.RawMessage(`{}`)
	} else if !jsonObject(params.Arguments) {
		return s.writeError(req.ID, -32602, "Invalid params")
	}

	var path string
	switch params.Name {
	case "ensemble_requests":
		query, err := requestQuery(params.Arguments)
		if err != nil {
			return s.writeResult(req.ID, toolError(err.Error()))
		}
		path = "/api/observability/requests"
		if encoded := query.Encode(); encoded != "" {
			path += "?" + encoded
		}
	case "ensemble_explain_trace":
		traceID, query, err := traceQuery(params.Arguments)
		if err != nil {
			return s.writeResult(req.ID, toolError(err.Error()))
		}
		path = "/api/observability/traces/" + escapePathSegment(traceID)
		if encoded := query.Encode(); encoded != "" {
			path += "?" + encoded
		}
	default:
		return s.writeError(req.ID, -32602, "Unknown tool")
	}

	structured, text, err := s.get(ctx, path)
	if err != nil {
		return s.writeResult(req.ID, toolError(err.Error()))
	}
	return s.writeResult(req.ID, map[string]any{
		"content":           []map[string]string{{"type": "text", "text": text}},
		"structuredContent": structured,
		"isError":           false,
	})
}

type requestArguments struct {
	Service       *string  `json:"service"`
	Path          *string  `json:"path"`
	ErrorsOnly    *bool    `json:"errorsOnly"`
	MinDurationMS *float64 `json:"minDurationMs"`
	Limit         *int     `json:"limit"`
}

func requestQuery(raw json.RawMessage) (url.Values, error) {
	var args requestArguments
	if err := decodeStrict(raw, &args); err != nil {
		return nil, errors.New("invalid ensemble_requests arguments")
	}
	query := make(url.Values)
	if args.Service != nil {
		if *args.Service == "" || len(*args.Service) > 128 {
			return nil, errors.New("service must contain 1 to 128 bytes")
		}
		query.Set("service", *args.Service)
	}
	if args.Path != nil {
		if *args.Path == "" || len(*args.Path) > 512 {
			return nil, errors.New("path must contain 1 to 512 bytes")
		}
		query.Set("path", *args.Path)
	}
	if args.ErrorsOnly != nil {
		query.Set("errorsOnly", strconv.FormatBool(*args.ErrorsOnly))
	}
	if args.MinDurationMS != nil {
		if math.IsNaN(*args.MinDurationMS) || math.IsInf(*args.MinDurationMS, 0) || *args.MinDurationMS < 0 {
			return nil, errors.New("minDurationMs must be non-negative")
		}
		query.Set("minDurationMs", strconv.FormatFloat(*args.MinDurationMS, 'g', -1, 64))
	}
	if args.Limit != nil {
		if *args.Limit < 1 || *args.Limit > 100 {
			return nil, errors.New("limit must be between 1 and 100")
		}
		query.Set("limit", strconv.Itoa(*args.Limit))
	}
	return query, nil
}

type traceArguments struct {
	TraceID *string `json:"traceId"`
	Limit   *int    `json:"limit"`
}

func traceQuery(raw json.RawMessage) (string, url.Values, error) {
	var args traceArguments
	if err := decodeStrict(raw, &args); err != nil {
		return "", nil, errors.New("invalid ensemble_explain_trace arguments")
	}
	if args.TraceID == nil || strings.TrimSpace(*args.TraceID) == "" || len(*args.TraceID) > 256 {
		return "", nil, errors.New("traceId must contain 1 to 256 bytes and cannot be blank")
	}
	query := make(url.Values)
	if args.Limit != nil {
		if *args.Limit < 1 || *args.Limit > 200 {
			return "", nil, errors.New("limit must be between 1 and 200")
		}
		query.Set("limit", strconv.Itoa(*args.Limit))
	}
	return *args.TraceID, query, nil
}

func decodeStrict(raw json.RawMessage, dst any) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) < 2 || trimmed[0] != '{' {
		return errors.New("expected JSON object")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &fields); err != nil || fields == nil {
		return errors.New("expected JSON object")
	}
	for _, value := range fields {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return errors.New("null field")
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("extra JSON value")
	}
	return nil
}

func escapePathSegment(value string) string {
	escaped := url.PathEscape(value)
	switch escaped {
	case ".":
		return "%2E"
	case "..":
		return "%2E%2E"
	default:
		return escaped
	}
}

func (s *server) get(ctx context.Context, endpoint string) (map[string]any, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.baseURL.String()+endpoint, nil)
	if err != nil {
		return nil, "", fmt.Errorf("build Ensemble API request: %w", err)
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("ensemble API request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxHTTPResponseBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("read Ensemble API response: %w", err)
	}
	if len(body) > MaxHTTPResponseBytes {
		return nil, "", fmt.Errorf("ensemble API response exceeds %d bytes", MaxHTTPResponseBytes)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("ensemble API returned HTTP %d", resp.StatusCode)
	}
	var structured map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&structured); err != nil || structured == nil || decoder.Decode(&struct{}{}) != io.EOF {
		return nil, "", errors.New("ensemble API returned an invalid JSON object")
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, body); err != nil {
		return nil, "", errors.New("ensemble API returned invalid JSON")
	}
	return structured, compact.String(), nil
}

func toolError(message string) map[string]any {
	return map[string]any{
		"content": []map[string]string{{"type": "text", "text": message}},
		"isError": true,
	}
}

func (s *server) writeResult(id json.RawMessage, result any) error {
	return s.write(response{JSONRPC: "2.0", ID: id, Result: result})
}

func (s *server) writeError(id json.RawMessage, code int, message string) error {
	if id == nil {
		id = json.RawMessage("null")
	}
	return s.write(response{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: message}})
}

func (s *server) write(value response) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode MCP response: %w", err)
	}
	encoded = append(encoded, '\n')
	n, err := s.output.Write(encoded)
	if err != nil {
		return fmt.Errorf("write MCP output: %w", err)
	}
	if n != len(encoded) {
		return fmt.Errorf("write MCP output: %w", io.ErrShortWrite)
	}
	return nil
}
