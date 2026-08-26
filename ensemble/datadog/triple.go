package datadog

import (
	"context"
	"fmt"
	"strings"
)

// QueryPercentileTriple runs queryTemplate three times — once per
// percentile, substituting the literal "{P}" substring with "50", "95",
// then "99" — and returns the three resolved values, converted to
// milliseconds. This is the piece shared by `from-datadog` and `apply`: a
// LatencyRule always carries all three percentiles together, in
// milliseconds (see core/proxy.LatencyRule), but Datadog trace-duration
// metrics (e.g. "trace.http.server.request.duration", the metric family
// this feature targets) report in seconds — 0.06 means 60ms — so every
// value is multiplied by 1000 here, once, rather than by each caller.
func QueryPercentileTriple(ctx context.Context, c Client, queryTemplate string, windowMinutes int) (p50, p95, p99 float64, err error) {
	p50, err = c.QueryPercentile(ctx, strings.ReplaceAll(queryTemplate, "{P}", "50"), windowMinutes)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("p50: %w", err)
	}
	p95, err = c.QueryPercentile(ctx, strings.ReplaceAll(queryTemplate, "{P}", "95"), windowMinutes)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("p95: %w", err)
	}
	p99, err = c.QueryPercentile(ctx, strings.ReplaceAll(queryTemplate, "{P}", "99"), windowMinutes)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("p99: %w", err)
	}
	const secondsToMs = 1000
	return p50 * secondsToMs, p95 * secondsToMs, p99 * secondsToMs, nil
}
