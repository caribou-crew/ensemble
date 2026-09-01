package server

// Traffic history and whole-session export: read-only surfaces over hops
// that have already left the in-memory ring. The ring (Recorder.Snapshot)
// only ever holds the most recent Ring hops; .ensemble/hops.jsonl holds
// everything ever recorded for this run, so "load earlier" in the
// dashboard and a whole-session HAR export both need to read it directly.

import (
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
// &errorsOnly=&session=&method=&path=&status=: a newest-first page of
// hops.jsonl older than before, honoring the same errorsOnly/session
// filters as GET /api/traffic plus method/path/status (the UI's query
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

	match := func(h trace.Hop) bool {
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
		return true
	}

	window, matched, corrupt, err := scanHopsFile(s.trafficHistoryPath(), limit, match)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// scanHopsFile returns file order (oldest first); the endpoint's
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
	writeJSON(w, http.StatusOK, trace.ToHar(s.reachableHops(s.sessionHops(id))))
}

// sessionHops unions every hop with Session == id from the in-memory ring
// and from disk history, deduped by seq (a hop can be in both — the ring
// hasn't necessarily rolled it out yet), sorted ascending by seq.
func (s *server) sessionHops(id string) []trace.Hop {
	bySeq := map[uint64]trace.Hop{}
	for _, h := range s.Rec.Snapshot() {
		if h.Session == id {
			bySeq[h.Seq] = h
		}
	}
	diskHops, _, _, err := scanHopsFile(s.trafficHistoryPath(), 0, func(h trace.Hop) bool {
		return h.Session == id
	})
	if err == nil {
		for _, h := range diskHops {
			bySeq[h.Seq] = h
		}
	}
	out := make([]trace.Hop, 0, len(bySeq))
	for _, h := range bySeq {
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}

func reverseHops(hops []trace.Hop) {
	for i, j := 0, len(hops)-1; i < j; i, j = i+1, j-1 {
		hops[i], hops[j] = hops[j], hops[i]
	}
}

// scanHopsFile scans path forward once, collecting every hop for which
// match returns true, in file order (oldest first — Recorder.Record
// appends in strict seq order, under the same lock that assigns seq, so
// the file is always seq-monotonic). maxKeep > 0 bounds memory by
// evicting the oldest kept match once more than maxKeep have been seen —
// since the file is seq-ascending, what survives is always the newest
// maxKeep matches; maxKeep <= 0 keeps everything. matched is the total
// number of hops that satisfied match, independent of eviction, so a
// caller windowing by maxKeep can tell whether older matches exist.
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
			hops = append(hops, h)
			if maxKeep > 0 && len(hops) > maxKeep {
				hops = hops[1:]
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
	return hops, matched, corrupt, nil
}
