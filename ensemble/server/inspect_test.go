package server_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/ensemble/config"
	"github.com/caribou-crew/ensemble/ensemble/inspector"
	"github.com/caribou-crew/ensemble/ensemble/server"
)

// fakeInspectorDriver is a minimal in-memory inspector.Driver for exercising
// the server's inspector endpoints without a live database. inspector's own
// test suite has an equivalent fakeDriver, but it's unexported there — this
// package's tests are black-box (package server_test), so it's
// reimplemented here rather than reaching into inspector's internals.
type fakeInspectorDriver struct {
	mu     sync.Mutex
	tables []inspector.Table
	rows   map[string][]map[string]any
	fps    map[string]string
}

func newFakeInspectorDriver(tables ...inspector.Table) *fakeInspectorDriver {
	f := &fakeInspectorDriver{tables: tables, rows: map[string][]map[string]any{}, fps: map[string]string{}}
	for _, tbl := range tables {
		f.fps[tbl.Name] = "v0"
	}
	return f
}

func (f *fakeInspectorDriver) Tables(ctx context.Context) ([]inspector.Table, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]inspector.Table, len(f.tables))
	copy(out, f.tables)
	return out, nil
}

func (f *fakeInspectorDriver) Rows(ctx context.Context, table string, limit, offset int) ([]map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rows := f.rows[table]
	if offset >= len(rows) {
		return nil, nil
	}
	end := min(offset+limit, len(rows))
	out := make([]map[string]any, end-offset)
	copy(out, rows[offset:end])
	return out, nil
}

func (f *fakeInspectorDriver) Fingerprint(ctx context.Context, table string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fps[table], nil
}

func (f *fakeInspectorDriver) setFingerprint(table, fp string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fps[table] = fp
}

func (f *fakeInspectorDriver) setRows(table string, rows []map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows[table] = rows
}

// inspectTestEnv is a bare server.New handler with a registered fake
// inspector driver — no orchestrator/proxy machinery, since the inspector
// and entity endpoints don't touch either.
type inspectTestEnv struct {
	ts   *httptest.Server
	cfg  *config.Config
	insp *inspector.Inspector
}

func newInspectTestEnv(t *testing.T, extraDeps func(*server.Deps)) *inspectTestEnv {
	t.Helper()
	cfg := &config.Config{
		Dir: t.TempDir(),
		Databases: map[string]config.Database{
			"primary": {Type: "postgres"},
			"cache":   {Type: "redis"}, // no inspector.Driver for redis — deliberately unregistered
		},
	}
	insp := inspector.New()

	deps := server.Deps{Cfg: cfg, Version: "test", Insp: insp, InspectPollInterval: 5 * time.Millisecond}
	if extraDeps != nil {
		extraDeps(&deps)
	}
	handler := server.New(deps)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return &inspectTestEnv{ts: ts, cfg: cfg, insp: insp}
}

func (e *inspectTestEnv) get(t *testing.T, path string) (*http.Response, []byte) {
	t.Helper()
	resp, err := http.Get(e.ts.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body %s: %v", path, err)
	}
	return resp, body
}

func TestDatabasesListsRegisteredIntersection(t *testing.T) {
	e := newInspectTestEnv(t, nil)
	e.insp.Register("primary", newFakeInspectorDriver(inspector.Table{Name: "users"}))
	// "cache" stays unregistered — it must not appear even though it's in
	// cfg.Databases.

	resp, body := e.get(t, "/api/databases")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	var got struct {
		Databases []struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"databases"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, body)
	}
	if len(got.Databases) != 1 || got.Databases[0].Name != "primary" || got.Databases[0].Type != "postgres" {
		t.Fatalf("databases = %+v, want exactly [{primary postgres}]", got.Databases)
	}
}

func TestDatabaseSchemaShape(t *testing.T) {
	e := newInspectTestEnv(t, nil)
	e.insp.Register("primary", newFakeInspectorDriver(
		inspector.Table{Name: "users", Columns: []inspector.Column{{Name: "id", Type: "int", Nullable: false}}},
		inspector.Table{Name: "orders"},
	))

	resp, body := e.get(t, "/api/databases/primary/schema")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	var got struct {
		Tables []inspector.Table `json:"tables"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, body)
	}
	if len(got.Tables) != 2 {
		t.Fatalf("tables = %+v, want 2", got.Tables)
	}
}

func TestDatabaseSchemaUnknownDbIs404(t *testing.T) {
	e := newInspectTestEnv(t, nil)
	resp, body := e.get(t, "/api/databases/nope/schema")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	var got map[string]string
	if err := json.Unmarshal(body, &got); err != nil || got["error"] == "" {
		t.Fatalf("expected {error:...} body, got %s", body)
	}
}

func TestDatabaseRowsDefaultLimitAndCap(t *testing.T) {
	e := newInspectTestEnv(t, nil)
	fd := newFakeInspectorDriver(inspector.Table{Name: "users"})
	rows := make([]map[string]any, 600)
	for i := range rows {
		rows[i] = map[string]any{"id": i}
	}
	fd.setRows("users", rows)
	e.insp.Register("primary", fd)

	// No ?limit=: defaults to 50.
	resp, body := e.get(t, "/api/databases/primary/rows?table=users")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	var got struct {
		Rows []map[string]any `json:"rows"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, body)
	}
	if len(got.Rows) != 50 {
		t.Fatalf("default limit: got %d rows, want 50", len(got.Rows))
	}

	// ?limit=1000 is capped at 500.
	resp, body = e.get(t, "/api/databases/primary/rows?table=users&limit=1000")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, body)
	}
	if len(got.Rows) != 500 {
		t.Fatalf("capped limit: got %d rows, want 500", len(got.Rows))
	}
}

func TestDatabaseRowsMissingTableIs400(t *testing.T) {
	e := newInspectTestEnv(t, nil)
	e.insp.Register("primary", newFakeInspectorDriver(inspector.Table{Name: "users"}))

	resp, body := e.get(t, "/api/databases/primary/rows")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
}

func TestDatabaseRowsInvalidLimitIs400(t *testing.T) {
	e := newInspectTestEnv(t, nil)
	e.insp.Register("primary", newFakeInspectorDriver(inspector.Table{Name: "users"}))

	resp, body := e.get(t, "/api/databases/primary/rows?table=users&limit=not-a-number")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
}

// TestInspectorEndpointsNilInspAre501 guards Deps.Insp's documented nil
// contract: every inspector-backed endpoint returns 501 rather than
// panicking when no Insp is configured.
func TestInspectorEndpointsNilInspAre501(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir()}
	handler := server.New(server.Deps{Cfg: cfg, Version: "test"})
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	for _, path := range []string{
		"/api/databases",
		"/api/databases/primary/schema",
		"/api/databases/primary/rows?table=users",
		"/api/inspector/stream",
	} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotImplemented {
			t.Errorf("GET %s status = %d, want 501", path, resp.StatusCode)
		}
	}
}

// TestInspectorStreamEmitsChangeEvent registers a fake driver, flips its
// fingerprint, and confirms one `event: change` arrives over SSE — the
// inspector analogue of TestTrafficStreamSSEReadsTwoEventsThenDisconnects.
func TestInspectorStreamEmitsChangeEvent(t *testing.T) {
	e := newInspectTestEnv(t, nil)
	fd := newFakeInspectorDriver(inspector.Table{Name: "users"})
	e.insp.Register("primary", fd)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.ts.URL+"/api/inspector/stream", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET stream: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q", ct)
	}

	// Give the poller a moment to establish its baseline fingerprint before
	// flipping it — otherwise the flip could land before the baseline poll,
	// in which case it's just the (unfired) baseline itself.
	time.Sleep(30 * time.Millisecond)
	fd.setFingerprint("users", "v1")

	reader := bufio.NewReader(resp.Body)
	var dataLine string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read stream: %v", err)
		}
		if strings.HasPrefix(line, "event: change") {
			dataLine, err = reader.ReadString('\n')
			if err != nil {
				t.Fatalf("read data line: %v", err)
			}
			break
		}
	}
	if dataLine == "" {
		t.Fatal("no change event observed within deadline")
	}
	dataLine = strings.TrimPrefix(strings.TrimSpace(dataLine), "data: ")
	var ev struct {
		DB    string    `json:"db"`
		Table string    `json:"table"`
		At    time.Time `json:"at"`
	}
	if err := json.Unmarshal([]byte(dataLine), &ev); err != nil {
		t.Fatalf("unmarshal event: %v (%q)", err, dataLine)
	}
	if ev.DB != "primary" || ev.Table != "users" || ev.At.IsZero() {
		t.Fatalf("event = %+v, want DB=primary Table=users At=<non-zero>", ev)
	}
}

// readOneChangeEvent reads stream frames from r until an `event: change`
// arrives (or deadline passes) and returns its raw JSON data line.
// The deadline must win the race against the read, not merely be consulted
// between completed lines: bufio.Reader.ReadString blocks indefinitely, and no
// read deadline is set on the connection. Looping on time.Now().Before(deadline)
// therefore never fires when it matters — a regression that stops delivering
// events blocks forever, the package hits Go's 10-minute timeout, and CI reports
// a goroutine dump instead of this test failing. Reading on a goroutine and
// selecting against the deadline turns that into a clean assertion failure.
func readOneChangeEvent(t *testing.T, r *bufio.Reader, deadline time.Time) string {
	t.Helper()

	type result struct {
		data string
		err  error
	}
	// Buffered so the reader goroutine cannot block forever if the deadline wins.
	ch := make(chan result, 1)
	go func() {
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				ch <- result{err: fmt.Errorf("read stream: %w", err)}
				return
			}
			if !strings.HasPrefix(line, "event: change") {
				continue
			}
			dataLine, err := r.ReadString('\n')
			if err != nil {
				ch <- result{err: fmt.Errorf("read data line: %w", err)}
				return
			}
			ch <- result{data: strings.TrimPrefix(strings.TrimSpace(dataLine), "data: ")}
			return
		}
	}()

	select {
	case res := <-ch:
		if res.err != nil {
			t.Fatal(res.err)
		}
		return res.data
	case <-time.After(time.Until(deadline)):
		t.Fatal("no change event observed within deadline")
		return ""
	}
}

// TestInspectorStreamFansOutToMultipleSubscribers guards the multi-subscriber
// path final-review-phase-3.md's Parked #1 flagged as untested: inspectHub
// multiplexes one inspector.Watch poller to N concurrent SSE clients, started
// lazily on the first subscriber and stopped again once the last one
// disconnects. This exercises exactly that lifecycle — two concurrent
// subscribers both observe the same change event, and a subscriber
// disconnecting (which tears down and, if it was the last one, stops the
// poller) does not disrupt a survivor or break a fresh subscriber that joins
// right after.
func TestInspectorStreamFansOutToMultipleSubscribers(t *testing.T) {
	e := newInspectTestEnv(t, nil)
	fd := newFakeInspectorDriver(inspector.Table{Name: "users"})
	e.insp.Register("primary", fd)

	open := func() (*http.Response, *bufio.Reader, context.CancelFunc) {
		ctx, cancel := context.WithCancel(t.Context())
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.ts.URL+"/api/inspector/stream", nil)
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET stream: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d", resp.StatusCode)
		}
		return resp, bufio.NewReader(resp.Body), cancel
	}

	resp1, r1, cancel1 := open()
	defer resp1.Body.Close()
	defer cancel1()
	resp2, r2, cancel2 := open()
	defer resp2.Body.Close()
	defer cancel2()

	// Give the (single, shared) poller a moment to establish its baseline before flipping.
	time.Sleep(30 * time.Millisecond)
	fd.setFingerprint("users", "v1")

	deadline := time.Now().Add(5 * time.Second)
	data1 := readOneChangeEvent(t, r1, deadline)
	data2 := readOneChangeEvent(t, r2, deadline)
	if data1 == "" || data1 != data2 {
		t.Fatalf("subscribers saw different events: %q vs %q", data1, data2)
	}

	// Subscriber 1 tabs away — its cancel() unregisters it and, since it's the FIRST of two,
	// must NOT stop the shared poller (only the last subscriber's cancel does that).
	cancel1()
	resp1.Body.Close()

	// A third subscriber joins right after. If the teardown above were slow (the pre-fix
	// bug: stop() blocking on in-flight queries while stopFn was already nil'd), this dial
	// would race a still-draining first poller into starting a stacked second one. Every
	// survivor/newcomer must still see exactly one event for the next flip.
	resp3, r3, cancel3 := open()
	defer resp3.Body.Close()
	defer cancel3()

	fd.setFingerprint("users", "v2")
	deadline = time.Now().Add(5 * time.Second)
	data2b := readOneChangeEvent(t, r2, deadline)
	data3 := readOneChangeEvent(t, r3, deadline)
	if data2b == "" || data2b != data3 {
		t.Fatalf("surviving/new subscribers saw different events: %q vs %q", data2b, data3)
	}
}
