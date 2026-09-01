// Package tui is a terminal UI client of ensemble's control-plane REST/SSE
// API — the same contract dashboard/ensemble-ui speaks, rendered in a
// terminal instead of a browser. See openspec/changes/terminal-ui.
package tui

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
	"github.com/caribou-crew/ensemble/ensemble/server"
)

// apiClient is every control-plane call a TUI panel needs. Client is its
// only production implementation; panels depend on the interface so tests
// can substitute a fake instead of a live HTTP server.
type apiClient interface {
	Status(ctx context.Context) (StatusResponse, error)
	Topology(ctx context.Context) (server.TopologyResponse, error)
	Restart(ctx context.Context, name string) (orchestrator.ServiceState, error)
	Flip(ctx context.Context, name string) (orchestrator.ServiceState, error)
	Seed(ctx context.Context, name string) (SeedResponse, error)
	// ServiceLogs fetches the last tail lines of name's service log — GET
	// /api/services/{name}/logs?tail=N, plain text.
	ServiceLogs(ctx context.Context, name string, tail int) (string, error)
	LatencyList(ctx context.Context) (LatencyListResponse, error)
	LatencyArmAll(ctx context.Context, enabled bool) (LatencyListResponse, error)
	LatencyReset(ctx context.Context) (LatencyListResponse, error)
	Profiles(ctx context.Context) (orchestrator.ProfilesState, error)
	ProfileUp(ctx context.Context, name string) (orchestrator.ProfilesState, error)
	ProfileDown(ctx context.Context, name string) (orchestrator.ProfilesState, error)
	// TrafficStream opens one SSE connection to /api/traffic/stream and
	// delivers hops until ctx is canceled or the connection drops, at
	// which point the channel is closed. Reconnection is handled by the
	// traffic panel (see stream.go), not here — this is a single attempt.
	TrafficStream(ctx context.Context, since uint64) (<-chan trace.Hop, error)
}

// Client is a thin REST/SSE client over ensemble's /api control plane,
// scoped to the endpoints the terminal UI uses. It deliberately mirrors
// ensemble/cmd/ensemble's Client rather than importing it: that Client
// lives in package main and doesn't expose Restart/Flip, which the
// Services panel needs.
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

// NewClient returns a Client targeting baseURL (e.g. "http://127.0.0.1:4700").
func NewClient(baseURL string) *Client {
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), HTTP: &http.Client{}}
}

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

// Topology fetches GET /api/topology, the only source of gateway nodes:
// gateways are static listeners the proxy binds at Up, not
// orchestrator-supervised nodes, so they never appear in StatusResponse.
func (c *Client) Topology(ctx context.Context) (server.TopologyResponse, error) {
	var out server.TopologyResponse
	err := c.do(ctx, http.MethodGet, "/api/topology", nil, &out)
	return out, err
}

// --- service actions ---

func (c *Client) Restart(ctx context.Context, name string) (orchestrator.ServiceState, error) {
	var out orchestrator.ServiceState
	err := c.do(ctx, http.MethodPost, "/api/services/"+url.PathEscape(name)+"/restart", nil, &out)
	return out, err
}

func (c *Client) Flip(ctx context.Context, name string) (orchestrator.ServiceState, error) {
	var out orchestrator.ServiceState
	err := c.do(ctx, http.MethodPost, "/api/services/"+url.PathEscape(name)+"/flip", nil, &out)
	return out, err
}

// --- seed ---

// SeedResponse mirrors POST /api/seed/{name}'s body, decoded regardless of
// status (200 or 500) since a partial-failure run still carries results.
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

// --- service logs ---

// ServiceLogs fetches the last tail lines of name's log as plain text —
// decoded by hand rather than through do(), which expects JSON.
func (c *Client) ServiceLogs(ctx context.Context, name string, tail int) (string, error) {
	path := fmt.Sprintf("/api/services/%s/logs?tail=%d", url.PathEscape(name), tail)
	status, data, err := c.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return "", err
	}
	if status >= 400 {
		return "", apiError(http.MethodGet, path, status, data)
	}
	return string(data), nil
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

// --- profiles ---

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

// --- traffic (SSE) ---

// TrafficStream connects to GET /api/traffic/stream?since=N and delivers
// hops on the returned channel until ctx is canceled or the connection
// closes, at which point the channel is closed. The returned error is set
// only when the initial connection/handshake fails. One attempt — see
// StreamTraffic (stream.go) for the reconnecting wrapper panels use.
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
