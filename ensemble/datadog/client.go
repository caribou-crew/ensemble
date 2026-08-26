// Package datadog is a minimal client for Datadog's metrics query API,
// used to pull real percentile latency numbers into ensemble's LatencyStore
// (see openspec/changes/datadog-latency-import). It has no dependency on
// ensemble/config or ensemble/orchestrator — callers resolve site/
// credentials themselves and pass plain strings in.
package datadog

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// requestTimeout bounds a single QueryPercentile HTTP call. Datadog is a
// real network hop (unlike readiness checks against localhost), so this is
// looser than readiness's 5s.
const requestTimeout = 10 * time.Second

// Client queries a single Datadog percentile metric over a time window and
// returns one representative value (see HTTPClient's doc comment for the
// reduction it applies). query is a fully-substituted Datadog metric query
// (e.g. "p50:trace.http.server.request.duration{service:billing}") — percentile
// template substitution ("{P}" -> "50"/"95"/"99") is the caller's job, done
// by QueryPercentileTriple.
type Client interface {
	QueryPercentile(ctx context.Context, query string, windowMinutes int) (float64, error)
}

// HTTPClient is the real Client implementation, backed by Datadog's
// GET /api/v1/query. A window's result is a pointlist of [timestamp, value]
// samples; QueryPercentile reduces it to one number by averaging the
// non-null values — the simplest deterministic stand-in for "eyeball the
// graph and type in roughly what it's been" (see design.md).
type HTTPClient struct {
	Site   string
	APIKey string
	AppKey string
	// BaseURL overrides the "https://api.<Site>" default — set in tests to
	// point at an httptest.Server.
	BaseURL string

	httpClient *http.Client
}

// NewHTTPClient builds an HTTPClient for site ("datadoghq.com", "datadoghq.eu", ...)
// using apiKey/appKey for auth.
func NewHTTPClient(site, apiKey, appKey string) *HTTPClient {
	return &HTTPClient{Site: site, APIKey: apiKey, AppKey: appKey}
}

func (c *HTTPClient) baseURL() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return "https://api." + c.Site
}

type queryResponse struct {
	Status string        `json:"status"`
	Error  string        `json:"error"`
	Series []querySeries `json:"series"`
}

type querySeries struct {
	Pointlist [][2]*float64 `json:"pointlist"`
}

// QueryPercentile issues one GET /api/v1/query for the last windowMinutes
// and returns the average of every non-null point across every returned
// series. An empty (no data) result is an error naming the query and
// window, not a silently-returned zero.
func (c *HTTPClient) QueryPercentile(ctx context.Context, query string, windowMinutes int) (float64, error) {
	reqCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	now := time.Now()
	from := now.Add(-time.Duration(windowMinutes) * time.Minute).Unix()
	to := now.Unix()

	params := url.Values{}
	params.Set("from", fmt.Sprintf("%d", from))
	params.Set("to", fmt.Sprintf("%d", to))
	params.Set("query", query)
	reqURL := fmt.Sprintf("%s/api/v1/query?%s", c.baseURL(), params.Encode())
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, reqURL, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("DD-API-KEY", c.APIKey)
	req.Header.Set("DD-APPLICATION-KEY", c.AppKey)

	client := c.httpClient
	if client == nil {
		client = &http.Client{Timeout: requestTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("datadog query %q: %w", query, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, fmt.Errorf("datadog query %q: read response body: %w", query, err)
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("datadog query %q: status %d: %s", query, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed queryResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return 0, fmt.Errorf("datadog query %q: parse response: %w", query, err)
	}
	if parsed.Status == "error" {
		return 0, fmt.Errorf("datadog query %q: %s", query, parsed.Error)
	}

	var sum float64
	var count int
	for _, series := range parsed.Series {
		for _, point := range series.Pointlist {
			if point[1] == nil {
				continue
			}
			sum += *point[1]
			count++
		}
	}
	if count == 0 {
		return 0, fmt.Errorf("datadog query %q returned no data points in the last %dm", query, windowMinutes)
	}
	return sum / float64(count), nil
}
