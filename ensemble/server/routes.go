package server

import (
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/caribou-crew/ensemble/core/proxy"
	"github.com/caribou-crew/ensemble/core/trace"
)

// routes registers every /api endpoint on mux. Mutating endpoints (POST/
// PUT/DELETE that change orchestrator, latency, or session state) are
// wrapped with withAnnotation so every mutation lands a control-plane hop
// in the Recorder — the "mutations logged as annotation events" contract.
func (s *server) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("GET /api/topology", s.handleTopology)

	mux.HandleFunc("POST /api/services/{name}/restart", s.withAnnotation(s.handleServiceRestart))
	mux.HandleFunc("POST /api/services/{name}/flip", s.withAnnotation(s.handleServiceFlip))
	mux.HandleFunc("POST /api/services/{name}/variant", s.withAnnotation(s.handleServiceVariant))

	mux.HandleFunc("POST /api/seed/{name}", s.withAnnotation(s.handleSeed))

	mux.HandleFunc("GET /api/profiles", s.handleProfiles)
	mux.HandleFunc("POST /api/profiles/{name}/up", s.withAnnotation(s.handleProfileUp))
	mux.HandleFunc("POST /api/profiles/{name}/down", s.withAnnotation(s.handleProfileDown))

	mux.HandleFunc("GET /api/traffic", s.handleTraffic)
	mux.HandleFunc("GET /api/traffic/stream", s.handleTrafficStream)

	mux.HandleFunc("GET /api/traces/{traceId}", s.handleTrace)
	mux.HandleFunc("GET /api/traces/{traceId}/export", s.handleTraceExport)

	mux.HandleFunc("GET /api/latency", s.handleLatencyList)
	mux.HandleFunc("PUT /api/latency", s.withAnnotation(s.handleLatencyUpsert))
	mux.HandleFunc("DELETE /api/latency", s.withAnnotation(s.handleLatencyDelete))
	mux.HandleFunc("POST /api/latency/arm-all", s.withAnnotation(s.handleLatencyArmAll))
	mux.HandleFunc("POST /api/latency/reset", s.withAnnotation(s.handleLatencyReset))

	mux.HandleFunc("POST /api/sessions", s.withAnnotation(s.handleSessionStart))
	mux.HandleFunc("DELETE /api/sessions/{id}", s.withAnnotation(s.handleSessionEnd))
	mux.HandleFunc("GET /api/sessions/{id}/hops", s.handleSessionHops)

	mux.HandleFunc("GET /api/databases", s.handleDatabases)
	mux.HandleFunc("GET /api/databases/{name}/schema", s.handleDatabaseSchema)
	mux.HandleFunc("GET /api/databases/{name}/rows", s.handleDatabaseRows)
	mux.HandleFunc("GET /api/inspector/stream", s.handleInspectorStream)

	// The discovery route ("GET /api/entities", exact) and the passthrough
	// ("/api/entities/{name}/{path...}") share the /api/entities prefix;
	// ServeMux resolves the ambiguity by preferring the more specific
	// pattern, so registration order here doesn't matter, but listing the
	// exact match first documents the intent.
	//
	// The passthrough is registered once per method rather than as a single
	// method-less ("any") pattern: net/http.ServeMux's own conflict check
	// rejects a method-less wildcard-path pattern alongside "GET /" (the
	// dashboard SPA fallback, registered in New) — neither pattern is
	// strictly more specific than the other in both the method and path
	// dimensions at once, which ServeMux treats as an unresolvable
	// ambiguity and panics on at registration time. A method-qualified
	// pattern doesn't have that problem (same shape as every other GET
	// route here coexisting with the "GET /" fallback), so entity requests
	// are proxied per explicit verb instead.
	mux.HandleFunc("GET /api/entities", s.handleEntities)
	for _, method := range []string{
		http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodOptions,
	} {
		// Both registered (not just the {path...} one): "/{name}/{path...}"
		// requires a "/" delimiter before the wildcard, so it does NOT
		// match a bare "/api/entities/{name}" (no trailing slash, no
		// subpath) — e.g. POST /api/entities/users to create a resource at
		// an entity's root. Relying on ServeMux's subtree-redirect instead
		// (registering only the {path...} form) would 301 that request,
		// and net/http.Client's redirect handling converts POST to GET on
		// a 301/302, silently dropping the body — the second, exact
		// registration avoids that trap entirely rather than working
		// around it.
		mux.HandleFunc(method+" /api/entities/{name}", s.handleEntityProxy)
		mux.HandleFunc(method+" /api/entities/{name}/{path...}", s.handleEntityProxy)
	}

	mux.HandleFunc("GET /api/openapi.json", s.handleOpenAPI)

	mux.HandleFunc("POST /api/shutdown", s.withAnnotation(s.handleShutdown))
}

// --- JSON helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// --- annotation middleware ---

// statusWriter captures the status code a handler actually wrote, so the
// annotation hop recorded after it runs reflects the real response, not an
// assumption.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// withAnnotation wraps a mutating handler so every call — success or
// failure — records a control-plane hop into the Recorder: To
// "ensemble-control", Method the HTTP method, Path the endpoint, Status the
// response code actually written.
func (s *server) withAnnotation(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		h(sw, r)
		if s.Rec != nil {
			s.Rec.Record(trace.Hop{
				To:     "ensemble-control",
				Method: r.Method,
				Path:   r.URL.Path,
				Status: sw.status,
				T:      trace.Timings{Start: start, DoneMs: msSince(start)},
			})
		}
	}
}

func msSince(t time.Time) float64 {
	return float64(time.Since(t)) / float64(time.Millisecond)
}

// --- health / status / topology ---

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": s.Version})
}

func (s *server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"services": s.Orch.States()})
}

// TopologyNode is one node in the topology graph: a service, database,
// stub, or gateway, with its live status (when the orchestrator tracks it)
// and whether clients call it directly.
type TopologyNode struct {
	Name     string `json:"name"`
	Category string `json:"category"` // "service" | "database" | "stub" | "gateway"
	Status   string `json:"status"`
	Entry    bool   `json:"entry,omitempty"`
	// Variant/Variants are set only for a service declaring
	// config.Service.Variants: the current choice and every option, so a
	// client can offer the switch exactly where it applies.
	Variant  string   `json:"variant,omitempty"`
	Variants []string `json:"variants,omitempty"`
}

// TopologyEdge is a directed dependency: From calls/depends on To.
type TopologyEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// TopologyResponse is the full graph GET /api/topology returns.
type TopologyResponse struct {
	Nodes []TopologyNode `json:"nodes"`
	Edges []TopologyEdge `json:"edges"`
}

func (s *server) handleTopology(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.buildTopology())
}

func (s *server) buildTopology() TopologyResponse {
	cfg := s.Cfg

	statusFor := func(name string) string {
		if st, ok := s.Orch.Service(name); ok {
			return string(st.Status)
		}
		return "stopped"
	}

	nodeNames := map[string]bool{}
	for name := range cfg.Services {
		nodeNames[name] = true
	}
	for name := range cfg.Databases {
		nodeNames[name] = true
	}
	for name := range cfg.Stubs {
		nodeNames[name] = true
	}
	for name := range cfg.Gateways {
		nodeNames[name] = true
	}

	svcNames := sortedKeys(cfg.Services)
	dbNames := sortedKeys(cfg.Databases)
	stubNames := sortedKeys(cfg.Stubs)
	gwNames := sortedKeys(cfg.Gateways)

	nodes := make([]TopologyNode, 0, len(svcNames)+len(dbNames)+len(stubNames)+len(gwNames))
	for _, name := range svcNames {
		svc := cfg.Services[name]
		cur, avail := s.Orch.Variant(name)
		nodes = append(nodes, TopologyNode{Name: name, Category: "service", Status: statusFor(name), Entry: svc.Entry, Variant: cur, Variants: avail})
	}
	for _, name := range dbNames {
		nodes = append(nodes, TopologyNode{Name: name, Category: "database", Status: statusFor(name)})
	}
	for _, name := range stubNames {
		// Stubs are config-defined fakes, not orchestrator-supervised nodes
		// (Task 2.2/2.3 never starts them) — "static" says so rather than
		// borrowing a lifecycle Status that doesn't apply.
		nodes = append(nodes, TopologyNode{Name: name, Category: "stub", Status: "static"})
	}
	for _, name := range gwNames {
		// A gateway is a static listener the proxy binds at Up, not a
		// supervised node — same lifecycle story as a stub. Clients call
		// it directly by definition, so it is always an entry.
		nodes = append(nodes, TopologyNode{Name: name, Category: "gateway", Status: "static", Entry: true})
	}

	// portToService resolves an env-wired "127.0.0.1:<port>" reference back
	// to the service whose intercept port that is.
	portToService := map[int]string{}
	for _, name := range svcNames {
		if p := cfg.Services[name].Proxy; p > 0 {
			portToService[p] = name
		}
	}

	seen := map[string]bool{}
	var edges []TopologyEdge
	addEdge := func(from, to string) {
		key := from + "\x00" + to
		if seen[key] {
			return
		}
		seen[key] = true
		edges = append(edges, TopologyEdge{From: from, To: to})
	}

	for _, name := range svcNames {
		svc := cfg.Services[name]
		for _, dep := range svc.DependsOn {
			if nodeNames[dep] {
				addEdge(name, dep)
			}
		}
		for _, v := range svc.Env {
			for port, target := range portToService {
				if target == name {
					continue
				}
				if strings.Contains(v, fmt.Sprintf("127.0.0.1:%d", port)) {
					addEdge(name, target)
				}
			}
		}
	}
	for _, name := range gwNames {
		for _, route := range cfg.Gateways[name].Routes {
			if nodeNames[route.Service] {
				addEdge(name, route.Service)
			}
		}
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].From != edges[j].From {
			return edges[i].From < edges[j].From
		}
		return edges[i].To < edges[j].To
	})

	return TopologyResponse{Nodes: nodes, Edges: edges}
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// --- service lifecycle mutations ---

func (s *server) handleServiceRestart(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := s.Cfg.Services[name]; !ok {
		writeErr(w, http.StatusNotFound, fmt.Sprintf("service %q not found", name))
		return
	}
	if err := s.Orch.Restart(r.Context(), name); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	st, _ := s.Orch.Service(name)
	writeJSON(w, http.StatusOK, st)
}

func (s *server) handleServiceFlip(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := s.Cfg.Services[name]; !ok {
		writeErr(w, http.StatusNotFound, fmt.Sprintf("service %q not found", name))
		return
	}
	if err := s.Orch.Flip(r.Context(), name); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	st, _ := s.Orch.Service(name)
	writeJSON(w, http.StatusOK, st)
}

// handleServiceVariant switches a service to one of its declared variants
// (config.Service.Variants) — see orchestrator.SetVariant. 404 for an
// unknown service, 400 for a service without variants or an unknown
// variant name, 500 when the switch itself fails.
func (s *server) handleServiceVariant(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	svc, ok := s.Cfg.Services[name]
	if !ok {
		writeErr(w, http.StatusNotFound, fmt.Sprintf("service %q not found", name))
		return
	}
	var req struct {
		Variant string `json:"variant"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if len(svc.Variants) == 0 {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("service %q declares no variants", name))
		return
	}
	if _, ok := svc.Variants[req.Variant]; !ok {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("service %q has no variant %q (have %s)", name, req.Variant, strings.Join(svc.VariantNames(), ", ")))
		return
	}
	if err := s.Orch.SetVariant(r.Context(), name, req.Variant); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	st, _ := s.Orch.Service(name)
	writeJSON(w, http.StatusOK, st)
}

// --- profiles ---

func (s *server) handleProfiles(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Orch.Profiles())
}

// knownProfile 404s for a profile the config never mentions.
func (s *server) knownProfile(w http.ResponseWriter, name string) bool {
	for _, p := range s.Cfg.ProfileNames() {
		if p == name {
			return true
		}
	}
	writeErr(w, http.StatusNotFound, fmt.Sprintf("profile %q not found", name))
	return false
}

func (s *server) handleProfileUp(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !s.knownProfile(w, name) {
		return
	}
	if err := s.Orch.UpProfiles(r.Context(), []string{name}); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.Orch.Profiles())
}

func (s *server) handleProfileDown(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !s.knownProfile(w, name) {
		return
	}
	if err := s.Orch.DownProfiles([]string{name}); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.Orch.Profiles())
}

// --- seed ---

func (s *server) handleSeed(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := s.Cfg.Seeds[name]; !ok {
		writeErr(w, http.StatusNotFound, fmt.Sprintf("seed %q not defined", name))
		return
	}
	results, err := s.Orch.Seed(r.Context(), name)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"results": results,
			"ok":      false,
			"error":   err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results, "ok": true})
}

// --- traffic ---

func parseUint(s string) uint64 {
	if s == "" {
		return 0
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0
	}
	return v
}

func parseInt(s string) int {
	if s == "" {
		return 0
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return v
}

func parseBool(s string) bool {
	v, _ := strconv.ParseBool(s)
	return v
}

func (s *server) handleTraffic(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	sinceParam := q.Get("since")
	since := parseUint(sinceParam)
	limit := parseInt(q.Get("limit"))
	errorsOnly := parseBool(q.Get("errorsOnly"))
	session := q.Get("session")

	hops := s.Rec.Snapshot()
	out := make([]trace.Hop, 0, len(hops))
	for _, h := range hops {
		if h.Seq <= since {
			continue
		}
		if errorsOnly && h.Status < 400 && h.Err == "" {
			continue
		}
		if session != "" && h.Session != session {
			continue
		}
		out = append(out, h)
	}
	if limit > 0 && len(out) > limit {
		if sinceParam != "" {
			// Cursor paging (since + limit): return the OLDEST `limit`
			// hops after since, oldest first, so a client that advances
			// its cursor to the last hop it received and re-polls never
			// skips the hops in between — the newest-`limit` behavior
			// below would silently drop everything before the tail of a
			// burst larger than limit.
			out = out[:limit]
		} else {
			// No cursor: `limit` alone means "the most recent N" — a tail
			// view for a client that isn't paging.
			out = out[len(out)-limit:]
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"hops": out})
}

// --- traces ---

// logicalHop is a JSON-friendly view of trace.LogicalHop — the core type's
// fields are exported but untagged, so mapping here keeps the API's casing
// consistent (camelCase) without adding tags to a shared core type for one
// consumer.
type logicalHop struct {
	Hop            *trace.Hop `json:"hop"`
	Origin         *trace.Hop `json:"origin"`
	Via            []string   `json:"via,omitempty"`
	Index          int        `json:"index"`
	StatusMismatch bool       `json:"statusMismatch,omitempty"`
}

func hopsForTrace(all []trace.Hop, traceID string) []trace.Hop {
	out := make([]trace.Hop, 0)
	for _, h := range all {
		if h.TraceID == traceID {
			out = append(out, h)
		}
	}
	return out
}

func (s *server) handleTrace(w http.ResponseWriter, r *http.Request) {
	traceID := r.PathValue("traceId")
	hops := hopsForTrace(s.Rec.Snapshot(), traceID)

	logical := trace.CollapseRelays(hops, true)
	out := make([]logicalHop, len(logical))
	for i, l := range logical {
		out[i] = logicalHop{Hop: l.Hop, Origin: l.Origin, Via: l.Via, Index: l.Index, StatusMismatch: l.StatusMismatch}
	}

	writeJSON(w, http.StatusOK, map[string]any{"hops": hops, "logical": out})
}

// reachableHop rewrites a hop's Host header to the loopback intercept
// address ensemble actually proxies for h.To, so exports reproduce a
// request that can be replayed. `To` is a logical service name — never
// resolvable on its own (see trace.HopURL) — and the recorded Host header
// can likewise be a container-network name the exporting machine can't
// reach. When `To` doesn't match a configured service with an assigned
// proxy port (an unregistered stub, an external host, ...), the hop is
// returned unchanged: that's the best information export already had.
func (s *server) reachableHop(h trace.Hop) trace.Hop {
	svc, ok := s.Cfg.Services[h.To]
	if !ok || svc.Proxy <= 0 {
		return h
	}
	headers := make(map[string]string, len(h.Req.Headers)+1)
	maps.Copy(headers, h.Req.Headers)
	headers["host"] = fmt.Sprintf("127.0.0.1:%d", svc.Proxy)
	h.Req.Headers = headers
	return h
}

func (s *server) reachableHops(hops []trace.Hop) []trace.Hop {
	out := make([]trace.Hop, len(hops))
	for i, h := range hops {
		out[i] = s.reachableHop(h)
	}
	return out
}

func (s *server) handleTraceExport(w http.ResponseWriter, r *http.Request) {
	traceID := r.PathValue("traceId")
	hops := s.reachableHops(hopsForTrace(s.Rec.Snapshot(), traceID))
	format := r.URL.Query().Get("format")

	switch format {
	case "har":
		writeJSON(w, http.StatusOK, trace.ToHar(hops))
	case "curl":
		lines := make([]string, len(hops))
		for i, h := range hops {
			lines[i] = trace.ToCurl(h)
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, strings.Join(lines, "\n\n"))
	case "raw":
		parts := make([]string, len(hops))
		for i, h := range hops {
			parts[i] = trace.ToRawRequest(h) + "\n\n" + trace.ToRawResponse(h)
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, strings.Join(parts, "\n\n---\n\n"))
	default:
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("unknown format %q, want har|curl|raw", format))
	}
}

// --- latency ---

func (s *server) handleLatencyList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"rules": s.Lat.Rules()})
}

func (s *server) handleLatencyUpsert(w http.ResponseWriter, r *http.Request) {
	var rule proxy.LatencyRule
	if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if rule.Target == "" {
		writeErr(w, http.StatusBadRequest, "target is required")
		return
	}
	s.Lat.Set(rule)
	writeJSON(w, http.StatusOK, map[string]any{"rules": s.Lat.Rules()})
}

func (s *server) handleLatencyDelete(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	target, path := q.Get("target"), q.Get("path")
	if target == "" {
		writeErr(w, http.StatusBadRequest, "target is required")
		return
	}
	s.Lat.Remove(target, path)
	writeJSON(w, http.StatusOK, map[string]any{"rules": s.Lat.Rules()})
}

func (s *server) handleLatencyArmAll(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	s.Lat.ArmAll(body.Enabled)
	writeJSON(w, http.StatusOK, map[string]any{"rules": s.Lat.Rules()})
}

func (s *server) handleLatencyReset(w http.ResponseWriter, r *http.Request) {
	s.Lat.Reset()
	writeJSON(w, http.StatusOK, map[string]any{"rules": s.Lat.Rules()})
}

// --- sessions ---

func (s *server) handleSessionStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID    string `json:"id"`
		Entry string `json:"entry"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if req.ID == "" || req.Entry == "" {
		writeErr(w, http.StatusBadRequest, "id and entry are required")
		return
	}
	// The entry is whatever the client would call directly: a service's
	// intercept port, or a gateway's port.
	var entryPort int
	if gw, ok := s.Cfg.Gateways[req.Entry]; ok {
		entryPort = gw.Port
	} else {
		svc, ok := s.Cfg.Services[req.Entry]
		if !ok {
			writeErr(w, http.StatusNotFound, fmt.Sprintf("entry service %q not found", req.Entry))
			return
		}
		if svc.Proxy <= 0 {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("entry service %q has no proxy port", req.Entry))
			return
		}
		entryPort = svc.Proxy
	}
	upstream := fmt.Sprintf("http://127.0.0.1:%d", entryPort)
	ses, err := s.Sessions.Start(req.ID, req.Entry, upstream)
	if err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": ses.ID, "edgeAddr": ses.EdgeAddr})
}

func (s *server) handleSessionEnd(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ses := s.Sessions.End(id)
	if ses == nil {
		writeErr(w, http.StatusNotFound, fmt.Sprintf("session %q not found", id))
		return
	}
	hops := ses.Hops()
	verdict, reasons := ses.Verdict()
	writeJSON(w, http.StatusOK, map[string]any{
		"id":      id,
		"hops":    len(hops),
		"verdict": verdict,
		"reasons": reasons,
	})
}

func (s *server) handleSessionHops(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ses := s.Sessions.Get(id)
	if ses == nil {
		writeErr(w, http.StatusNotFound, fmt.Sprintf("session %q not found", id))
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	enc := json.NewEncoder(w)
	for _, h := range ses.Hops() {
		_ = enc.Encode(h)
	}
}

// --- shutdown ---

// handleShutdown lets a loopback caller (cmd/ensemble's `down`) request a
// graceful stop of the whole `up` process. Guarded to loopback since it
// has no other auth and a local dev tool has no business accepting this
// from the network. Shutdown is invoked asynchronously, after the response
// is written, so the handler (and withAnnotation's post-hop recording)
// completes before Serve's graceful drain begins tearing the HTTP server
// down around it.
func (s *server) handleShutdown(w http.ResponseWriter, r *http.Request) {
	if !isLoopbackAddr(r.RemoteAddr) {
		writeErr(w, http.StatusForbidden, "shutdown is only permitted from loopback")
		return
	}
	if s.Shutdown == nil {
		writeErr(w, http.StatusNotImplemented, "shutdown not configured")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	go s.Shutdown()
}

// isLoopbackAddr reports whether remoteAddr (an http.Request.RemoteAddr,
// "host:port" or occasionally bare host) resolves to a loopback address.
func isLoopbackAddr(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
