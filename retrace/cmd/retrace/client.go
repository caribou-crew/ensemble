package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/capture"
)

// Client is a thin REST client over ensemble's /api control plane, modeled
// on ensemble/cmd/ensemble/client.go and holding the same discipline: the
// CLI is just another API consumer, and nothing here does work the server's
// handler does not.
//
// It talks to ensemble over plain net/http rather than importing the
// ensemble module. retrace must never depend on ensemble — Design §1
// promises a team can adopt retrace in CI without running ensemble at all,
// and the dependency direction stays retrace → core, ensemble → core.
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

// Compile-time proof that Client is what capture.StartAttached wants. The
// interface lives in package capture (the consumer); the implementation
// lives here (the CLI), so package capture never learns what HTTP is.
var _ capture.EnsembleClient = (*Client)(nil)

// NewClient targets baseURL (e.g. "http://127.0.0.1:4700"). No request
// timeout is set on the underlying http.Client — callers control per-call
// deadlines via ctx, and Drain's hop polls are already bounded by their own
// window.
func NewClient(baseURL string) *Client {
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), HTTP: &http.Client{}}
}

func (c *Client) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

// send performs one request and returns the live response. The caller owns
// resp.Body — SessionHops streams it rather than buffering, since a long
// flow's hop dump is the one response here that is not small.
func (c *Client) send(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("%s %s: encode request body: %w", method, path, err)
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, reader)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	return resp, nil
}

// do is the common case: a >=400 status becomes a Go error built from the
// server's {"error":"..."} body, and a 2xx body is decoded into out.
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	resp, err := c.send(ctx, method, path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("%s %s: read response: %w", method, path, err)
	}
	if resp.StatusCode >= 400 {
		return apiError(method, path, resp.StatusCode, data)
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

// Health reports whether an ensemble control plane is answering at BaseURL.
//
// The `ok` field is checked, not just the status code. Its zero value is
// false, which is the safe reading: a 200 from something that is not
// ensemble (any other dev server on 4700) would otherwise be taken as a
// live control plane, and `retrace run` would go on to POST a session at it
// and report the failure as ensemble refusing the run.
func (c *Client) Health(ctx context.Context) error {
	var out struct {
		OK      bool   `json:"ok"`
		Version string `json:"version"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/health", nil, &out); err != nil {
		return err
	}
	if !out.OK {
		return errors.New("GET /api/health: the control plane did not report ok")
	}
	return nil
}

// StartSession registers a retrace run with ensemble and returns the
// host:port of the session's client edge. The server answers 409 when the
// id is already active, 404 for an unknown entry service, and 400 when the
// entry has no proxy port; all three arrive here as an error carrying the
// server's own message.
func (c *Client) StartSession(ctx context.Context, id, entry string) (string, error) {
	var out struct {
		ID       string `json:"id"`
		EdgeAddr string `json:"edgeAddr"`
	}
	body := map[string]string{"id": id, "entry": entry}
	if err := c.do(ctx, http.MethodPost, "/api/sessions", body, &out); err != nil {
		return "", err
	}
	if out.EdgeAddr == "" {
		return "", fmt.Errorf("POST /api/sessions: ensemble returned no edgeAddr for session %q", id)
	}
	return out.EdgeAddr, nil
}

// SessionHops reads GET /api/sessions/{id}/hops, which is NDJSON — one
// trace.Hop per line — and NOT a JSON array. It is decoded with
// trace.NewReader, the same reader every other hop stream in this repo
// goes through, so blank lines are skipped and unknown fields tolerated.
func (c *Client) SessionHops(ctx context.Context, id string) ([]trace.Hop, error) {
	path := "/api/sessions/" + url.PathEscape(id) + "/hops"
	resp, err := c.send(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(resp.Body)
		return nil, apiError(http.MethodGet, path, resp.StatusCode, data)
	}
	var hops []trace.Hop
	r := trace.NewReader(resp.Body)
	for {
		h, err := r.Next()
		if errors.Is(err, trace.ErrEOF) {
			return hops, nil
		}
		if err != nil {
			return nil, fmt.Errorf("GET %s: %w", path, err)
		}
		hops = append(hops, h)
	}
}

// EndSession tears the session down and returns ensemble's own report on
// it. Called only after the hops have been drained and written: ensemble's
// SessionManager drops hops for a session it has already ended.
func (c *Client) EndSession(ctx context.Context, id string) (capture.EndReport, error) {
	var out capture.EndReport
	path := "/api/sessions/" + url.PathEscape(id)
	err := c.do(ctx, http.MethodDelete, path, nil, &out)
	return out, err
}
