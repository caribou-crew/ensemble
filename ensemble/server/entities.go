package server

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

// entityProxyClient forwards requests to a configured entity's Base. A
// bounded timeout guards against a misconfigured/unreachable Base wedging
// the caller forever — matching inspector.DynamoDriver's own default.
var entityProxyClient = &http.Client{Timeout: 30 * time.Second}

// hopByHopHeaders are per-connection headers that must not be forwarded
// verbatim across a proxy hop (RFC 7230 §6.1, plus the historical
// Proxy-Connection some clients still send).
var hopByHopHeaders = []string{
	"Connection", "Keep-Alive", "Proxy-Connection", "Proxy-Authenticate",
	"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
}

func stripHopByHopHeaders(h http.Header) {
	for _, k := range hopByHopHeaders {
		h.Del(k)
	}
}

// --- GET /api/entities ---

// entityInfo is one entry in GET /api/entities' response: the dashboard's
// entity-page discovery list.
type entityInfo struct {
	Name  string       `json:"name"`
	ID    string       `json:"id"`
	Links []entityLink `json:"links,omitempty"`
}

// entityLink mirrors config.EntityLink — see its doc comment for the
// {{column}} template contract. Resolved client-side, not here: this
// endpoint only relays the raw label/template pair.
type entityLink struct {
	Label    string `json:"label"`
	Template string `json:"template"`
}

func (s *server) handleEntities(w http.ResponseWriter, r *http.Request) {
	out := make([]entityInfo, 0, len(s.Cfg.Entities))
	for _, name := range sortedKeys(s.Cfg.Entities) {
		e := s.Cfg.Entities[name]
		// config.Validate requires entity.base but says nothing about entity.id, so an
		// entity configured without one is a valid config — defaulting here (rather than
		// forwarding "" verbatim) keeps every consumer of this endpoint, CLI/agents
		// included, from having to know "id" is the fallback (final review I4).
		id := e.ID
		if id == "" {
			id = "id"
		}
		links := make([]entityLink, len(e.Links))
		for i, l := range e.Links {
			links[i] = entityLink{Label: l.Label, Template: l.Template}
		}
		out = append(out, entityInfo{Name: name, ID: id, Links: links})
	}
	writeJSON(w, http.StatusOK, map[string]any{"entities": out})
}

// --- ANY /api/entities/{name}/{path...} ---

// handleEntityProxy reverse-proxies to cfg.Entities[name].Base + {path...},
// forwarding method/query/body/headers (minus hop-by-hop ones) and relaying
// the upstream's status/headers/body back verbatim. This deliberately does
// NOT go through withAnnotation: when Base points at an ensemble intercept
// port, the proxy's own Recorder already captures the hop on the way
// through — annotating here too would double-record it under a different
// shape.
func (s *server) handleEntityProxy(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ent, ok := s.Cfg.Entities[name]
	if !ok {
		writeErr(w, http.StatusNotFound, fmt.Sprintf("entity %q not found", name))
		return
	}

	target, err := joinEntityURL(ent.Base, r.PathValue("path"), r.URL.RawQuery)
	if err != nil {
		writeErr(w, http.StatusBadGateway, fmt.Sprintf("entity %q: %v", name, err))
		return
	}

	proxyReq, err := http.NewRequestWithContext(r.Context(), r.Method, target, r.Body)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	proxyReq.Header = r.Header.Clone()
	proxyReq.ContentLength = r.ContentLength
	stripHopByHopHeaders(proxyReq.Header)

	resp, err := entityProxyClient.Do(proxyReq)
	if err != nil {
		writeErr(w, http.StatusBadGateway, fmt.Sprintf("entity %q: %v", name, err))
		return
	}
	defer resp.Body.Close()

	respHeader := w.Header()
	for k, vv := range resp.Header {
		for _, v := range vv {
			respHeader.Add(k, v)
		}
	}
	stripHopByHopHeaders(respHeader)
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// joinEntityURL builds the upstream URL for a passthrough request: base
// (cfg.Entities[name].Base) with subPath appended to its existing path, and
// rawQuery forwarded verbatim.
//
// subPath is rooted ("/"+subPath) before path.Clean'ing it, which is the
// standard idiom for defusing path traversal: path.Clean on a rooted path
// can never produce a result that climbs above "/", so a subPath containing
// "../../etc/passwd" (however it got there — PathValue("path") is already
// mux-cleaned in practice, but this doesn't rely on that) collapses to
// something still under base's own path rather than escaping it. This is
// what keeps the passthrough from being usable to reach arbitrary hosts or
// files outside the configured Base.
func joinEntityURL(base, subPath, rawQuery string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("invalid base %q: %w", base, err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("invalid base %q: must be an absolute http(s) URL", base)
	}

	cleaned := path.Clean("/" + subPath)
	if cleaned == "/" {
		cleaned = ""
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + cleaned
	u.RawQuery = rawQuery
	return u.String(), nil
}
