package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/caribou-crew/ensemble/ensemble/inspector"
)

// inspectPollInterval is how often the server's inspector.Watch poller
// checks every registered driver for changes (see inspectHub). 2s per the
// brief — frequent enough for a dashboard to feel live, cheap enough not to
// hammer a local dev database.
const inspectPollInterval = 2 * time.Second

// defaultRowsLimit/maxRowsLimit bound GET /api/databases/{name}/rows'
// ?limit= — unset defaults to defaultRowsLimit, and any requested value
// above maxRowsLimit is silently capped rather than rejected (an explicit,
// generous ceiling protects against a client accidentally asking for an
// entire large table in one response).
const (
	defaultRowsLimit = 50
	maxRowsLimit     = 500
)

// --- GET /api/databases ---

// databaseInfo is one entry in GET /api/databases' response.
type databaseInfo struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// handleDatabases lists every ensemble.yaml database that also has a live
// Driver registered on Insp — cfg.Databases alone isn't enough, since a
// database type ensemble doesn't have an inspector Driver for (redis,
// localstack) is provisioned but never inspectable.
func (s *server) handleDatabases(w http.ResponseWriter, r *http.Request) {
	if s.Insp == nil {
		writeErr(w, http.StatusNotImplemented, "inspector not configured")
		return
	}
	out := make([]databaseInfo, 0, len(s.Cfg.Databases))
	for _, name := range sortedKeys(s.Cfg.Databases) {
		if !s.Insp.Has(name) {
			continue
		}
		out = append(out, databaseInfo{Name: name, Type: s.Cfg.Databases[name].Type})
	}
	writeJSON(w, http.StatusOK, map[string]any{"databases": out})
}

// --- GET /api/databases/{name}/schema ---

func (s *server) handleDatabaseSchema(w http.ResponseWriter, r *http.Request) {
	if s.Insp == nil {
		writeErr(w, http.StatusNotImplemented, "inspector not configured")
		return
	}
	name := r.PathValue("name")
	if !s.Insp.Has(name) {
		writeErr(w, http.StatusNotFound, fmt.Sprintf("database %q not found", name))
		return
	}
	tables, err := s.Insp.Schema(r.Context(), name)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tables": tables})
}

// --- GET /api/databases/{name}/rows ---

func (s *server) handleDatabaseRows(w http.ResponseWriter, r *http.Request) {
	if s.Insp == nil {
		writeErr(w, http.StatusNotImplemented, "inspector not configured")
		return
	}
	name := r.PathValue("name")
	if !s.Insp.Has(name) {
		writeErr(w, http.StatusNotFound, fmt.Sprintf("database %q not found", name))
		return
	}

	q := r.URL.Query()
	table := q.Get("table")
	if table == "" {
		writeErr(w, http.StatusBadRequest, "table is required")
		return
	}

	limit := defaultRowsLimit
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			writeErr(w, http.StatusBadRequest, "invalid limit")
			return
		}
		limit = n
	}
	if limit > maxRowsLimit {
		limit = maxRowsLimit
	}

	offset := 0
	if v := q.Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeErr(w, http.StatusBadRequest, "invalid offset")
			return
		}
		offset = n
	}

	rows, err := s.Insp.Rows(r.Context(), name, table, limit, offset)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rows": rows})
}

// --- GET /api/inspector/stream ---

// changeEventJSON is the wire shape of inspector.ChangeEvent — its own
// fields are untagged (a core-ish type shared with the poller internals),
// so this maps to the dashboard's camelCase contract without adding tags to
// a type this package doesn't own.
type changeEventJSON struct {
	DB    string    `json:"db"`
	Table string    `json:"table"`
	At    time.Time `json:"at"`
}

// handleInspectorStream serves GET /api/inspector/stream: SSE `event:
// change` per inspector.ChangeEvent, fanned out via s.hub, with the same
// heartbeat/disconnect framing as handleTrafficStream.
func (s *server) handleInspectorStream(w http.ResponseWriter, r *http.Request) {
	if s.Insp == nil || s.hub == nil {
		writeErr(w, http.StatusNotImplemented, "inspector not configured")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	ch, cancel := s.hub.subscribe()
	defer cancel()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case ev, open := <-ch:
			if !open {
				return
			}
			b, err := json.Marshal(changeEventJSON{DB: ev.DB, Table: ev.Table, At: ev.At})
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "event: change\ndata: %s\n\n", b); err != nil {
				return
			}
			flusher.Flush()
		case <-ticker.C:
			if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// --- inspectHub: single Watch poller fanned out to N SSE subscribers ---

// inspectHub multiplexes inspector.Inspector.Watch's single change-event
// channel to any number of concurrent SSE clients. The server owns exactly
// one Watch poller (started lazily, on the first subscriber — nothing
// polls every registered driver's tables until a dashboard is actually
// watching) rather than one per connected client, and stops it again once
// the last subscriber disconnects.
type inspectHub struct {
	insp     *inspector.Inspector
	interval time.Duration

	mu     sync.Mutex
	subs   map[chan inspector.ChangeEvent]struct{}
	stopFn func()
}

func newInspectHub(insp *inspector.Inspector, interval time.Duration) *inspectHub {
	return &inspectHub{
		insp:     insp,
		interval: interval,
		subs:     map[chan inspector.ChangeEvent]struct{}{},
	}
}

// subscribe registers a new SSE client, starting the poller if it isn't
// already running. The returned cancel unregisters the client and, if it
// was the last one, stops the poller — safe to call more than once.
func (h *inspectHub) subscribe() (<-chan inspector.ChangeEvent, func()) {
	h.mu.Lock()
	ch := make(chan inspector.ChangeEvent, 16)
	h.subs[ch] = struct{}{}
	if h.stopFn == nil {
		events, stop := h.insp.Watch(h.interval)
		h.stopFn = stop
		go h.pump(events)
	}
	h.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subs, ch)
			var stop func()
			if len(h.subs) == 0 {
				stop = h.stopFn
				h.stopFn = nil
			}
			h.mu.Unlock()
			if stop != nil {
				stop()
			}
			close(ch)
		})
	}
	return ch, cancel
}

// pump reads insp.Watch's single event stream and fans each event out to
// every current subscriber, dropping (never blocking) on a slow one — same
// non-blocking-fan-out shape as proxy.Recorder.Record's subscriber loop.
// Exits when events closes (the poller was stopped).
func (h *inspectHub) pump(events <-chan inspector.ChangeEvent) {
	for ev := range events {
		h.mu.Lock()
		for ch := range h.subs {
			select {
			case ch <- ev:
			default:
			}
		}
		h.mu.Unlock()
	}
}
