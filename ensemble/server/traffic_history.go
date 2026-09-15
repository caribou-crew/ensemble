package server

// Traffic history and whole-session export: read-only surfaces over hops
// that have already left the in-memory ring. The ring (Recorder.Snapshot)
// only ever holds the most recent Ring hops; .ensemble/hops.jsonl holds
// everything ever recorded for this run, so "load earlier" in the
// dashboard and a whole-session HAR export both need to read it directly.

import (
	"container/heap"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/caribou-crew/ensemble/core/trace"
)

const (
	historyDefaultLimit = 100
	// historyMaxLimit mirrors logTailCap's role: a client can ask for an
	// absurd page size without turning one request into an unbounded scan.
	historyMaxLimit = 2000
)

// trafficHistoryPath is where `ensemble up` persists every recorded hop as
// NDJSON — the exact path cmd_up.go's runUp opens as a rotating writer.
func (s *server) trafficHistoryPath() string {
	return filepath.Join(s.Cfg.Dir, ".ensemble", "hops.jsonl")
}

// handleTrafficHistory serves GET /api/traffic/history?before=<seq>&limit=
// &errorsOnly=&session=&client=&method=&path=&status=: a newest-first page
// of hops.jsonl older than before, honoring the same errorsOnly/session/
// client filters as GET /api/traffic plus method/path/status (the UI's query
// grammar covers the rest client-side, same as the live view already
// does). Corrupt lines are skipped and counted, never fail the request; a
// missing hops.jsonl (nothing recorded yet) is an empty page, not a 404.
func (s *server) handleTrafficHistory(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	before := parseUint(q.Get("before"))
	if before == 0 {
		// No cursor given: page from the newest end of the file.
		before = math.MaxUint64
	}
	limit := parseInt(q.Get("limit"))
	if limit <= 0 {
		limit = historyDefaultLimit
	}
	limit = min(limit, historyMaxLimit)

	errorsOnly := parseBool(q.Get("errorsOnly"))
	session := q.Get("session")
	client := q.Get("client")
	method := strings.ToUpper(q.Get("method"))
	pathFilter := strings.ToLower(q.Get("path"))
	hasStatus := false
	status := 0
	if v := q.Get("status"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			hasStatus = true
			status = n
		}
	}

	// Client attribution over a single forward pass: the index holds the
	// traces seen SO FAR, which is exact for any chain that propagated the
	// identity in baggage (core/proxy writes it onto every such hop, so the
	// index is not even consulted) and best-effort for a chain that dropped
	// baggage — hops file order is completion order, and a trace is
	// recorded inner-first, so an inner hop can be tested before the entry
	// hop that names its client. Such a hop reads as unattributed, which is
	// the same answer the documented rule gives for any window that does
	// not contain its trace's client-carrying hop. The alternative is a
	// second pass over hops.jsonl per page, which is what this endpoint's
	// bounded-memory scan exists to avoid.
	clients := map[string]string{}
	match := func(h trace.Hop) bool {
		trace.ObserveClient(clients, h)
		if h.Seq >= before {
			return false
		}
		if errorsOnly && h.Status < 400 && h.Err == "" {
			return false
		}
		if session != "" && h.Session != session {
			return false
		}
		if method != "" && strings.ToUpper(h.Method) != method {
			return false
		}
		if pathFilter != "" && !strings.Contains(strings.ToLower(h.Path), pathFilter) {
			return false
		}
		if hasStatus && h.Status != status {
			return false
		}
		if !trace.MatchesClient(h, clients, client) {
			return false
		}
		return true
	}

	window, matched, corrupt, err := scanHopsFile(s.trafficHistoryPath(), limit, match)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// scanHopsFile returns bounded pages in ascending seq order; the endpoint's
	// contract is newest-first.
	reverseHops(window)
	writeJSON(w, http.StatusOK, map[string]any{
		"hops":         window,
		"corruptLines": corrupt,
		"hasMore":      matched > len(window),
	})
}

// handleSessionExport serves GET /api/sessions/{id}/export?format=har:
// every hop carrying session id, from the live ring plus disk history,
// deduped by seq and ordered chronologically, rendered as one HAR. Same
// per-request Host rewrite as handleTraceExport so the export is
// replayable.
func (s *server) handleSessionExport(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	format := r.URL.Query().Get("format")
	if format != "har" {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("unknown format %q, want har", format))
		return
	}
	hops, err := s.sessionHops(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, trace.ToHar(s.reachableHops(hops)))
}

// sessionHops unions every hop with Session == id from the in-memory ring
// and from disk history, deduped by seq (a hop can be in both — the ring
// hasn't necessarily rolled it out yet), sorted ascending by seq.
func (s *server) sessionHops(id string) ([]trace.Hop, error) {
	bySeq := map[uint64]trace.Hop{}
	for _, h := range s.Rec.Snapshot() {
		if h.Session == id {
			bySeq[h.Seq] = h
		}
	}
	diskHops, _, corrupt, err := scanHopsFile(s.trafficHistoryPath(), 0, func(h trace.Hop) bool {
		return h.Session == id
	})
	if err != nil {
		return nil, fmt.Errorf("cannot export complete session history: %w", err)
	}
	if corrupt > 0 {
		return nil, fmt.Errorf("cannot export complete session history: %d unreadable records", corrupt)
	}
	for _, h := range diskHops {
		bySeq[h.Seq] = h
	}
	out := make([]trace.Hop, 0, len(bySeq))
	for _, h := range bySeq {
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out, nil
}

func reverseHops(hops []trace.Hop) {
	for i, j := 0, len(hops)-1; i < j; i, j = i+1, j-1 {
		hops[i], hops[j] = hops[j], hops[i]
	}
}

// historyHopHeap keeps the lowest seq at the root so a bounded history
// page can replace its oldest hop when a newer match arrives.
type historyHopHeap []trace.Hop

func (h historyHopHeap) Len() int           { return len(h) }
func (h historyHopHeap) Less(i, j int) bool { return h[i].Seq < h[j].Seq }
func (h historyHopHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *historyHopHeap) Push(v any)        { *h = append(*h, v.(trace.Hop)) }
func (h *historyHopHeap) Pop() any {
	last := len(*h) - 1
	v := (*h)[last]
	(*h)[last] = trace.Hop{}
	*h = (*h)[:last]
	return v
}

// scanHopsFile scans path forward once, collecting hops for which match
// returns true. Streaming hops are persisted at close, so file order can
// differ from seq order. maxKeep > 0 uses a min-heap to retain the highest
// maxKeep matching seqs in O(maxKeep) memory, returning them in ascending
// seq order; maxKeep <= 0 keeps everything in file order. matched is the
// total number of hops that satisfied match, independent of eviction, so
// a caller windowing by maxKeep can tell whether older matches exist.
//
// Malformed lines are skipped and counted (corrupt), never fail the scan.
// A missing file is not an error — no history recorded yet is a normal
// empty result.
func scanHopsFile(path string, maxKeep int, match func(trace.Hop) bool) (hops []trace.Hop, matched, corrupt int, err error) {
	f, oerr := os.Open(path)
	if oerr != nil {
		if os.IsNotExist(oerr) {
			return nil, 0, 0, nil
		}
		return nil, 0, 0, oerr
	}
	defer f.Close()

	rd := trace.NewReader(f)
	var newest historyHopHeap
	// prevErr guards against a genuine bufio.Scanner-level failure (e.g. a
	// line over the reader's 16MB cap): trace.Reader.Next() then returns
	// the exact same stored error on every subsequent call (the Scanner
	// itself is done, not just past one bad line), which would spin
	// forever if treated as "skip and continue" like a per-line JSON
	// decode error is. A decode error, by contrast, leaves the Scanner
	// positioned at the next line — Next() naturally resumes past it, and
	// two decode errors are never the same error value, so comparing by
	// interface equality tells the two cases apart.
	var prevErr error
	for {
		h, nerr := rd.Next()
		if nerr == nil {
			prevErr = nil
			if !match(h) {
				continue
			}
			matched++
			if maxKeep <= 0 {
				hops = append(hops, h)
			} else if len(newest) < maxKeep {
				heap.Push(&newest, h)
			} else if h.Seq > newest[0].Seq {
				newest[0] = h
				heap.Fix(&newest, 0)
			}
			continue
		}
		if errors.Is(nerr, trace.ErrEOF) {
			break
		}
		corrupt++
		if nerr == prevErr {
			break
		}
		prevErr = nerr
	}
	if maxKeep > 0 {
		sort.Sort(newest)
		hops = []trace.Hop(newest)
	}
	return hops, matched, corrupt, nil
}
