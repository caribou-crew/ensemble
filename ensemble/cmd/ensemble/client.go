package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/caribou-crew/ensemble/core/proxy"
	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/ensemble/orchestrator"
)

// Client is a thin REST client over ensemble's /api control plane. Every
// method here does exactly what the server's handler does and nothing
// more — no business logic lives here that bypasses the API (the
// "API-first parity" constraint): the CLI is just another API consumer.
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

// NewClient returns a Client targeting baseURL (e.g.
// "http://127.0.0.1:4700"). No request timeout is set on the underlying
// http.Client — callers control per-call deadlines via ctx, and the
// traffic --follow SSE stream is intentionally long-lived.
func NewClient(baseURL string) *Client {
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), HTTP: &http.Client{}}
}

// request performs one HTTP call and returns the raw status/body. Transport
// failures (dial, timeout, body read) are returned as err; a >=400 HTTP
// status is NOT an error here — callers that want the server's
// {"error":"..."} convention translated into a Go error should use do.
func (c *Client) request(ctx context.Context, method, path string, body any) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, fmt.Errorf("%s %s: encode request body: %w", method, path, err)
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, reader)
	if err != nil {
		return 0, nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("%s %s: read response: %w", method, path, err)
	}
	return resp.StatusCode, data, nil
}

// do wraps request for the common case: a >=400 status becomes a Go error
// built from the server's {"error":"..."} body, and a successful response
// is JSON-decoded into out (skipped if out is nil).
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	status, data, err := c.request(ctx, method, path, body)
	if err != nil {
		return err
	}
	if status >= 400 {
		return apiError(method, path, status, data)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("%s %s: decode response: %w", method, path, err)
	}
	return nil
}

func apiError(method, path string, status int, body []byte) error {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error != "" {
		return fmt.Errorf("%s %s: %d: %s", method, path, status, e.Error)
	}
	return fmt.Errorf("%s %s: %d: %s", method, path, status, strings.TrimSpace(string(body)))
}

// --- status ---

// StatusResponse mirrors GET /api/status's body.
type StatusResponse struct {
	Services []orchestrator.ServiceState `json:"services"`
}

func (c *Client) Status(ctx context.Context) (StatusResponse, error) {
	var out StatusResponse
	err := c.do(ctx, http.MethodGet, "/api/status", nil, &out)
	return out, err
}

// --- variant ---

// SetVariant switches name to one of its declared variants and returns
// the resulting state (POST /api/services/{name}/variant).
func (c *Client) SetVariant(ctx context.Context, name, variant string) (orchestrator.ServiceState, error) {
	var out orchestrator.ServiceState
	err := c.do(ctx, http.MethodPost, "/api/services/"+url.PathEscape(name)+"/variant", map[string]string{"variant": variant}, &out)
	return out, err
}

// --- profiles ---

// Health reports whether a control plane answers at BaseURL — the
// `ensemble up <profile>` attach-vs-cold-start fork.
func (c *Client) Health(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "/api/health", nil, nil)
}

func (c *Client) Profiles(ctx context.Context) (orchestrator.ProfilesState, error) {
	var out orchestrator.ProfilesState
	err := c.do(ctx, http.MethodGet, "/api/profiles", nil, &out)
	return out, err
}

func (c *Client) ProfileUp(ctx context.Context, name string) (orchestrator.ProfilesState, error) {
	var out orchestrator.ProfilesState
	err := c.do(ctx, http.MethodPost, "/api/profiles/"+url.PathEscape(name)+"/up", nil, &out)
	return out, err
}

func (c *Client) ProfileDown(ctx context.Context, name string) (orchestrator.ProfilesState, error) {
	var out orchestrator.ProfilesState
	err := c.do(ctx, http.MethodPost, "/api/profiles/"+url.PathEscape(name)+"/down", nil, &out)
	return out, err
}

// --- shutdown ---

// ShutdownResponse mirrors POST /api/shutdown's body.
type ShutdownResponse struct {
	OK bool `json:"ok"`
}

func (c *Client) Shutdown(ctx context.Context) (ShutdownResponse, error) {
	var out ShutdownResponse
	err := c.do(ctx, http.MethodPost, "/api/shutdown", nil, &out)
	return out, err
}

// --- seed ---

// SeedResponse mirrors POST /api/seed/{name}'s body. The server writes this
// same shape on both success (200) and failure (500, partial Results plus
// Error) — Seed decodes the body regardless of status so a partial-failure
// run's results aren't discarded as a generic transport error.
type SeedResponse struct {
	Results []orchestrator.SeedStepResult `json:"results"`
	OK      bool                          `json:"ok"`
	Error   string                        `json:"error,omitempty"`
}

func (c *Client) Seed(ctx context.Context, name string) (SeedResponse, error) {
	var out SeedResponse
	path := "/api/seed/" + url.PathEscape(name)
	_, data, err := c.request(ctx, http.MethodPost, path, nil)
	if err != nil {
		return out, err
	}
	if jsonErr := json.Unmarshal(data, &out); jsonErr != nil {
		return out, fmt.Errorf("POST %s: decode response: %w", path, jsonErr)
	}
	return out, nil
}

// --- latency ---

// LatencyListResponse mirrors the {"rules": [...]} body every /api/latency*
// endpoint returns.
type LatencyListResponse struct {
	Rules []proxy.LatencyRule `json:"rules"`
}

func (c *Client) LatencyList(ctx context.Context) (LatencyListResponse, error) {
	var out LatencyListResponse
	err := c.do(ctx, http.MethodGet, "/api/latency", nil, &out)
	return out, err
}

func (c *Client) LatencySet(ctx context.Context, rule proxy.LatencyRule) (LatencyListResponse, error) {
	var out LatencyListResponse
	err := c.do(ctx, http.MethodPut, "/api/latency", rule, &out)
	return out, err
}

func (c *Client) LatencyArmAll(ctx context.Context, enabled bool) (LatencyListResponse, error) {
	var out LatencyListResponse
	err := c.do(ctx, http.MethodPost, "/api/latency/arm-all", map[string]bool{"enabled": enabled}, &out)
	return out, err
}

func (c *Client) LatencyReset(ctx context.Context) (LatencyListResponse, error) {
	var out LatencyListResponse
	err := c.do(ctx, http.MethodPost, "/api/latency/reset", nil, &out)
	return out, err
}

// --- traffic ---

// TrafficResponse mirrors GET /api/traffic's body.
type TrafficResponse struct {
	Hops []trace.Hop `json:"hops"`
}

func (c *Client) Traffic(ctx context.Context, since uint64, limit int, errorsOnly bool) (TrafficResponse, error) {
	q := url.Values{}
	if since > 0 {
		q.Set("since", strconv.FormatUint(since, 10))
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if errorsOnly {
		q.Set("errorsOnly", "true")
	}
	path := "/api/traffic"
	if enc := q.Encode(); enc != "" {
		path += "?" + enc
	}
	var out TrafficResponse
	err := c.do(ctx, http.MethodGet, path, nil, &out)
	return out, err
}

// TrafficStream connects to GET /api/traffic/stream?since=N (SSE) and
// delivers hops on the returned channel until ctx is canceled or the
// connection closes, at which point the channel is closed. The returned
// error is set only when the initial connection/handshake fails.
func (c *Client) TrafficStream(ctx context.Context, since uint64) (<-chan trace.Hop, error) {
	q := url.Values{}
	if since > 0 {
		q.Set("since", strconv.FormatUint(since, 10))
	}
	path := "/api/traffic/stream"
	if enc := q.Encode(); enc != "" {
		path += "?" + enc
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", path, err)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", path, err)
	}
	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, apiError(http.MethodGet, path, resp.StatusCode, data)
	}

	out := make(chan trace.Hop)
	go func() {
		defer close(out)
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
		for sc.Scan() {
			data, ok := strings.CutPrefix(sc.Text(), "data: ")
			if !ok {
				continue // SSE "event:" lines, heartbeat comments, blank separators
			}
			var h trace.Hop
			if json.Unmarshal([]byte(data), &h) != nil {
				continue
			}
			select {
			case out <- h:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

// --- traces ---

// traceLogicalHop mirrors server's unexported logicalHop DTO (its JSON
// shape, not the type — that struct is intentionally unexported since it's
// a response-shaping detail, not a shared model).
type traceLogicalHop struct {
	Hop            *trace.Hop `json:"hop"`
	Origin         *trace.Hop `json:"origin"`
	Via            []string   `json:"via,omitempty"`
	Index          int        `json:"index"`
	StatusMismatch bool       `json:"statusMismatch,omitempty"`
}

// TraceResponse mirrors GET /api/traces/{traceId}'s body.
type TraceResponse struct {
	Hops    []trace.Hop       `json:"hops"`
	Logical []traceLogicalHop `json:"logical"`
}

func (c *Client) Trace(ctx context.Context, traceID string) (TraceResponse, error) {
	var out TraceResponse
	err := c.do(ctx, http.MethodGet, "/api/traces/"+url.PathEscape(traceID), nil, &out)
	return out, err
}

// TraceExport returns the raw export body for format ("har"|"curl"|"raw")
// verbatim — har's body is JSON text, curl/raw are plain text, and export
// is meant to be consumed as-is (piped to a file, pasted into a terminal),
// not re-decoded.
func (c *Client) TraceExport(ctx context.Context, traceID, format string) (string, error) {
	path := fmt.Sprintf("/api/traces/%s/export?format=%s", url.PathEscape(traceID), url.QueryEscape(format))
	status, data, err := c.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return "", err
	}
	if status >= 400 {
		return "", apiError(http.MethodGet, path, status, data)
	}
	return string(data), nil
}
