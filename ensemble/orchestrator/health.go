package orchestrator

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"
)

// healthPollInterval is both the polling cadence and the per-attempt
// network timeout for health/TCP/docker probes.
const healthPollInterval = 250 * time.Millisecond

// pollUntil calls probe every healthPollInterval until it reports true,
// ctx is done, or timeout elapses.
func pollUntil(ctx context.Context, timeout time.Duration, probe func() (bool, error)) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		ok, err := probe()
		if err == nil && ok {
			return nil
		}
		lastErr = err
		if time.Now().After(deadline) {
			if lastErr != nil {
				return fmt.Errorf("timed out after %s: %w", timeout, lastErr)
			}
			return fmt.Errorf("timed out after %s", timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(healthPollInterval):
		}
	}
}

// pollHealth polls url every healthPollInterval until it answers 2xx.
func pollHealth(ctx context.Context, url string, timeout time.Duration) error {
	client := &http.Client{Timeout: healthPollInterval}
	err := pollUntil(ctx, timeout, func() (bool, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return false, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return false, err
		}
		defer resp.Body.Close()
		return resp.StatusCode >= 200 && resp.StatusCode < 300, nil
	})
	if err != nil {
		return fmt.Errorf("health check %s: %w", url, err)
	}
	return nil
}

// pollTCP polls addr every healthPollInterval until a TCP dial succeeds.
func pollTCP(ctx context.Context, addr string, timeout time.Duration) error {
	err := pollUntil(ctx, timeout, func() (bool, error) {
		conn, err := net.DialTimeout("tcp", addr, healthPollInterval)
		if err != nil {
			return false, err
		}
		conn.Close()
		return true, nil
	})
	if err != nil {
		return fmt.Errorf("tcp dial %s: %w", addr, err)
	}
	return nil
}
