package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/caribou-crew/ensemble/ensemble/config"
)

// SQLRunner executes one seed SQL file (path) against dbName. targetDB
// optionally names a specific logical database on dbName's server,
// overriding its own default — empty means use dbName's default. Task 2.5
// owns the concrete pg/mysql/dynamo drivers; this is the seam Seed calls
// through, set via Orchestrator.SQLRunner.
type SQLRunner interface {
	RunFile(ctx context.Context, dbName, targetDB, path string) error
}

// SeedStepResult is the outcome of one executed seed step.
type SeedStepResult struct {
	Kind       string  `json:"kind"` // "sql" | "http"
	Ref        string  `json:"ref"`  // SeedSQL.File (as declared) or SeedHTTP.URL
	OK         bool    `json:"ok"`
	Err        string  `json:"err,omitempty"`
	DurationMs float64 `json:"durationMs"`
}

// seedHTTPTimeout bounds every HTTP seed step (brief: "plain http.Client
// with 10s timeout, 2xx = OK").
const seedHTTPTimeout = 10 * time.Second

// Seed runs name's seed steps in declared order — every SQL step, then
// every HTTP step, matching the order config.Seed's SQL/HTTP lists are
// declared in — and stops at the first failure. It returns a
// SeedStepResult for every step actually executed (not the steps skipped
// after a failure), alongside an error naming the failed step when one
// stops the run early.
func (o *Orchestrator) Seed(ctx context.Context, name string) ([]SeedStepResult, error) {
	seed, ok := o.cfg.Seeds[name]
	if !ok {
		return nil, fmt.Errorf("orchestrator: seed %q: not defined", name)
	}

	var results []SeedStepResult

	for _, s := range seed.SQL {
		res, err := o.runSeedSQL(ctx, s)
		results = append(results, res)
		if err != nil {
			return results, fmt.Errorf("orchestrator: seed %q: sql step %s: %w", name, s.File, err)
		}
	}

	for _, h := range seed.HTTP {
		res, err := runSeedHTTP(ctx, h)
		results = append(results, res)
		if err != nil {
			return results, fmt.Errorf("orchestrator: seed %q: http step %s: %w", name, h.URL, err)
		}
	}

	// Recorded only on the way out, after every step succeeded. A seed that
	// failed halfway left the data in a state no name describes, and stamping
	// the manifest with that name would tell retrace two runs started from
	// the same data when one of them started from rubble.
	o.noteSeed(name, time.Now())
	return results, nil
}

// runSeedSQL resolves s.File against Config.Dir (SeedSQL.File is declared
// relative to it) and hands it to o.SQLRunner.
func (o *Orchestrator) runSeedSQL(ctx context.Context, s config.SeedSQL) (SeedStepResult, error) {
	start := time.Now()
	res := SeedStepResult{Kind: "sql", Ref: s.File}

	var err error
	if o.SQLRunner == nil {
		err = errors.New("no SQL runner configured")
	} else {
		path := s.File
		if !filepath.IsAbs(path) {
			path = filepath.Join(o.cfg.Dir, path)
		}
		err = o.SQLRunner.RunFile(ctx, s.Database, s.TargetDB, path)
	}

	res.DurationMs = msSince(start)
	if err != nil {
		res.Err = err.Error()
		return res, err
	}
	res.OK = true
	return res, nil
}

// runSeedHTTP issues h as a plain HTTP request with a 10s timeout; any 2xx
// status is OK.
func runSeedHTTP(ctx context.Context, h config.SeedHTTP) (SeedStepResult, error) {
	start := time.Now()
	res := SeedStepResult{Kind: "http", Ref: h.URL}

	err := doSeedHTTP(ctx, h)

	res.DurationMs = msSince(start)
	if err != nil {
		res.Err = err.Error()
		return res, err
	}
	res.OK = true
	return res, nil
}

func doSeedHTTP(ctx context.Context, h config.SeedHTTP) error {
	method := h.Method
	if method == "" {
		method = http.MethodGet
	}

	ctx, cancel := context.WithTimeout(ctx, seedHTTPTimeout)
	defer cancel()

	var body io.Reader
	if h.Body != "" {
		body = strings.NewReader(h.Body)
	}
	req, err := http.NewRequestWithContext(ctx, method, h.URL, body)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	for k, v := range h.Headers {
		req.Header.Set(k, v)
	}

	client := &http.Client{Timeout: seedHTTPTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

func msSince(start time.Time) float64 {
	return float64(time.Since(start)) / float64(time.Millisecond)
}
