package orchestrator

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"context"

	"github.com/caribou-crew/ensemble/ensemble/config"
)

// ReadinessOverallState is the stack-level readiness signal — see
// Orchestrator.Readiness. Distinct from ServiceState's per-node Status:
// readiness is computed once, after on_ready, across the whole stack.
type ReadinessOverallState string

const (
	// ReadinessPending is the state before Up has decided whether
	// readiness checks even apply (or before Up has run at all).
	ReadinessPending ReadinessOverallState = "pending"
	// ReadinessChecking is set once the retry loop has started and at
	// least one check hasn't yet passed.
	ReadinessChecking ReadinessOverallState = "checking"
	ReadinessReady    ReadinessOverallState = "ready"
	ReadinessNotReady ReadinessOverallState = "not_ready"
)

// ReadinessCheckState is one configured check's live pass/fail state.
type ReadinessCheckState struct {
	Name          string    `json:"name"`
	Passed        bool      `json:"passed"`
	LastError     string    `json:"lastError,omitempty"`
	LastCheckedAt time.Time `json:"lastCheckedAt,omitzero"`
}

// ReadinessSnapshot is the full readiness picture: overall state plus
// each configured check's current status, in declared order.
type ReadinessSnapshot struct {
	State  ReadinessOverallState `json:"state"`
	Checks []ReadinessCheckState `json:"checks"`
}

// readinessRequestTimeout bounds a single readiness check's HTTP request —
// separate from the overall readiness.timeout_s budget, which bounds how
// long the check is *retried*, not any one attempt.
const readinessRequestTimeout = 5 * time.Second

// Readiness returns the current stack-level readiness snapshot. Before
// Up's on_ready phase decides one way or the other, State is
// ReadinessPending with no checks.
func (o *Orchestrator) Readiness() ReadinessSnapshot {
	o.readinessMu.Lock()
	defer o.readinessMu.Unlock()
	state := o.readinessState
	if state == "" {
		state = ReadinessPending
	}
	checks := make([]ReadinessCheckState, len(o.readinessCheckStates))
	copy(checks, o.readinessCheckStates)
	return ReadinessSnapshot{State: state, Checks: checks}
}

func (o *Orchestrator) setReadinessState(s ReadinessOverallState) {
	o.readinessMu.Lock()
	defer o.readinessMu.Unlock()
	o.readinessState = s
}

// failReadinessIfConfigured marks readiness not_ready when readiness: is
// configured — used when Up can't even attempt readiness checks (a node
// failed before on_ready could run, or on_ready itself failed). With no
// readiness: key at all, the stack is trivially ready regardless of any
// node/on_ready failure — the "no readiness configured" scenarios in the
// readiness-checks spec apply unconditionally, not just to a clean Up; a
// project that never opted into this feature shouldn't have `ensemble
// ready` start failing because of it.
func (o *Orchestrator) failReadinessIfConfigured() {
	if o.cfg.Readiness == nil {
		o.setReadinessState(ReadinessReady)
		return
	}
	o.logf("orchestrator: readiness: skipped — stack did not reach on_ready")
	o.setReadinessState(ReadinessNotReady)
}

// beginReadiness starts the post-on_ready readiness phase. With no
// readiness: configured (or a checks file with zero checks), the stack is
// immediately ready — existing behavior for every stack that doesn't opt
// in is unaffected. Otherwise a background retry loop runs until every
// check has passed at least once or readiness.timeout_s elapses; Up does
// NOT wait on this (see design.md's async decision) — ctx is Up's own
// context, so an external cancellation (e.g. SIGINT/SIGTERM, which cancels
// the context orch.Up was called with) stops the loop the same way it
// stops everything else Up started.
func (o *Orchestrator) beginReadiness(ctx context.Context) {
	readiness := o.cfg.Readiness
	checksFile := o.cfg.ReadinessChecks()
	if readiness == nil || checksFile == nil || len(checksFile.Checks) == 0 {
		o.setReadinessState(ReadinessReady)
		return
	}

	states := make([]ReadinessCheckState, len(checksFile.Checks))
	for i, c := range checksFile.Checks {
		states[i] = ReadinessCheckState{Name: c.Name}
	}
	o.readinessMu.Lock()
	o.readinessState = ReadinessChecking
	o.readinessCheckStates = states
	o.readinessMu.Unlock()

	o.logf("orchestrator: readiness: %d check(s) configured, starting", len(checksFile.Checks))
	checks := append([]config.ReadinessCheck(nil), checksFile.Checks...)
	go o.runReadinessLoop(ctx, *readiness, checks)
}

// runReadinessLoop retries every not-yet-passed check every
// r.RetryIntervalS until all have passed or r.TimeoutS elapses since the
// loop started — see design.md's "per-check retry, not per-round retry"
// decision: a check that already passed is never re-executed on a later
// tick, so a headers_from script minting one-time credentials is never
// re-run once its check succeeds.
func (o *Orchestrator) runReadinessLoop(ctx context.Context, r config.Readiness, checks []config.ReadinessCheck) {
	timeout := time.Duration(r.EffectiveTimeoutS()) * time.Second
	interval := time.Duration(r.EffectiveRetryIntervalS()) * time.Second
	deadline := time.Now().Add(timeout)

	pending := make(map[string]config.ReadinessCheck, len(checks))
	for _, c := range checks {
		pending[c.Name] = c
	}

	for {
		for name, chk := range pending {
			passed, checkErr := o.runOneReadinessCheck(ctx, chk)
			changed := o.updateReadinessCheck(name, passed, checkErr)
			switch {
			case passed:
				o.logf("orchestrator: readiness: check %s passed", name)
				delete(pending, name)
			case changed:
				o.logf("orchestrator: readiness: check %s failed: %s", name, checkErr)
			}
		}

		if len(pending) == 0 {
			o.logf("orchestrator: readiness: all checks passed")
			o.setReadinessState(ReadinessReady)
			return
		}
		if !time.Now().Before(deadline) {
			o.logf("orchestrator: readiness: not ready after %s — still failing: %s", timeout, pendingNames(pending))
			o.setReadinessState(ReadinessNotReady)
			return
		}

		select {
		case <-ctx.Done():
			o.logf("orchestrator: readiness: stopped before all checks passed — still pending: %s", pendingNames(pending))
			o.setReadinessState(ReadinessNotReady)
			return
		case <-time.After(interval):
		}
	}
}

// pendingNames renders the still-failing check names for a log line, in a
// stable order — map iteration order is random and would make consecutive
// runs of the same failure read as if the checks themselves changed.
func pendingNames(pending map[string]config.ReadinessCheck) string {
	names := make([]string, 0, len(pending))
	for name := range pending {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// updateReadinessCheck records one check's latest attempt result and
// reports whether its pass/fail outcome or error text changed since the
// last attempt — the retry loop uses this to log a failure once instead of
// on every retry tick.
func (o *Orchestrator) updateReadinessCheck(name string, passed bool, err error) bool {
	errText := ""
	if err != nil {
		errText = err.Error()
	}
	o.readinessMu.Lock()
	defer o.readinessMu.Unlock()
	for i := range o.readinessCheckStates {
		if o.readinessCheckStates[i].Name != name {
			continue
		}
		changed := o.readinessCheckStates[i].Passed != passed || o.readinessCheckStates[i].LastError != errText
		o.readinessCheckStates[i].Passed = passed
		o.readinessCheckStates[i].LastCheckedAt = time.Now()
		o.readinessCheckStates[i].LastError = errText
		return changed
	}
	return false
}

// runOneReadinessCheck resolves chk.Service the same way the gateway
// resolves a route's service (config.Config.RoutablePort), runs its
// headers_from script if any, issues the request, and evaluates its
// assert block. A false return always carries a non-nil error explaining
// which part failed.
func (o *Orchestrator) runOneReadinessCheck(ctx context.Context, chk config.ReadinessCheck) (bool, error) {
	port, _, ok := o.cfg.RoutablePort(chk.Service)
	if !ok {
		return false, fmt.Errorf("service %q has no routable port", chk.Service)
	}

	headers, err := runHeadersFromScript(chk.HeadersFrom, o.cfg.Dir)
	if err != nil {
		return false, err
	}

	url := fmt.Sprintf("http://127.0.0.1:%d%s", port, chk.Path)
	reqCtx, cancel := context.WithTimeout(ctx, readinessRequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return false, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	client := &http.Client{Timeout: readinessRequestTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return false, fmt.Errorf("request %s: %w", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, fmt.Errorf("read response body: %w", err)
	}

	if chk.Assert.Status != nil && resp.StatusCode != *chk.Assert.Status {
		return false, fmt.Errorf("status %d, want %d", resp.StatusCode, *chk.Assert.Status)
	}
	if chk.Assert.BodyJQ != "" {
		truthy, result, err := evaluateBodyJQ(body, chk.Assert.BodyJQ)
		if err != nil {
			return false, err
		}
		if !truthy {
			return false, fmt.Errorf("body_jq %q evaluated to %v (falsy)", chk.Assert.BodyJQ, result)
		}
	}
	return true, nil
}

// runHeadersFromScript executes scriptPath (resolved relative to workDir
// unless absolute) and parses its stdout as one "Header-Name: value" pair
// per non-blank line. No new trust boundary versus on_ready's own shell
// steps: an empty scriptPath is a no-op (nil, nil).
func runHeadersFromScript(scriptPath, workDir string) (map[string]string, error) {
	if scriptPath == "" {
		return nil, nil
	}
	path := scriptPath
	if !filepath.IsAbs(path) {
		path = filepath.Join(workDir, path)
	}

	var stdout, stderr bytes.Buffer
	cmd := exec.Command(path)
	cmd.Dir = workDir
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("headers_from %q: %w: %s", scriptPath, err, strings.TrimSpace(stderr.String()))
	}

	headers, err := parseHeaderLines(stdout.String())
	if err != nil {
		return nil, fmt.Errorf("headers_from %q: %w", scriptPath, err)
	}
	return headers, nil
}

// parseHeaderLines parses output as one "Header-Name: value" pair per
// non-blank line — the contract runHeadersFromScript documents.
func parseHeaderLines(output string) (map[string]string, error) {
	headers := map[string]string{}
	for line := range strings.SplitSeq(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		rawName, value, ok := strings.Cut(line, ":")
		if !ok {
			return nil, fmt.Errorf("invalid header line %q: expected \"Header-Name: value\"", line)
		}
		name := strings.TrimSpace(rawName)
		value = strings.TrimSpace(value)
		if name == "" {
			return nil, fmt.Errorf("invalid header line %q: empty header name", line)
		}
		headers[name] = value
	}
	return headers, nil
}
