package server

import (
	"net"
	"net/http"
	"net/url"
	"strings"
)

// The control plane has no authentication: it is a local dev tool, and
// binding to loopback (cmd/ensemble's defaultAPIAddr) is what keeps the
// network out. Loopback alone does NOT keep a *browser* out, though — any
// page the developer happens to have open can reach 127.0.0.1:
//
//   - CSRF: a cross-origin `fetch(..., {mode:"no-cors"})` or a plain form
//     POST is delivered and executed even though the attacker can't read
//     the response. That's enough to restart services, run seeds (arbitrary
//     configured SQL/HTTP), rewrite latency rules, or shut the stack down.
//     Handlers decode JSON regardless of Content-Type, so "simple request"
//     content types dodge the preflight that would otherwise stop this.
//   - DNS rebinding: attacker.example resolves to 127.0.0.1 on a second
//     lookup, after which the page's origin legitimately matches the API
//     and the same-origin policy stops protecting anything — every
//     captured request/response body (bearer tokens, PII) becomes readable.
//
// guard closes both: the Host header must name a host we're actually
// serving (rebinding sends the attacker's own domain there), and a
// cross-origin browser request is rejected outright.

// hostSet is the guard's allow-list of Host/Origin hostnames. Loopback
// literals and "localhost" are always allowed on top of the set's own
// entries; the single entry "*" disables host/origin matching entirely
// (see Deps.AllowedHosts).
type hostSet map[string]bool

// newHostSet builds the allow-list from hosts (hostnames, with or without
// a port — the port is ignored, since the port a request arrives on is
// already decided by the listener).
func newHostSet(hosts []string) hostSet {
	s := hostSet{}
	for _, h := range hosts {
		if h = strings.TrimSpace(h); h != "" {
			s[strings.ToLower(hostOnly(h))] = true
		}
	}
	return s
}

// any reports whether the set is the "*" wildcard — host/origin matching
// off, for a deliberately wide bind whose reachable hostnames can't be
// enumerated.
func (s hostSet) any() bool { return s["*"] }

// allows reports whether hostport (a Host header or an Origin's authority)
// names something this server legitimately answers as.
func (s hostSet) allows(hostport string) bool {
	if s.any() {
		return true
	}
	host := strings.ToLower(hostOnly(hostport))
	if host == "" {
		return false
	}
	if s[host] {
		return true
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// hostOnly strips a ":port" (and IPv6 brackets) from a host[:port] string.
func hostOnly(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return strings.Trim(hostport, "[]")
}

// guard wraps h with the browser-facing protections described above. It
// runs ahead of every route, including GETs — reading captured traffic is
// exactly what a rebinding attack is after.
func guard(allowed hostSet, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Sec-Fetch-Site is set by the browser itself and can't be forged
		// by page script, so it's checked even in wildcard mode — a wide
		// bind is a choice about the network, not an invitation to every
		// website the developer visits.
		if strings.EqualFold(r.Header.Get("Sec-Fetch-Site"), "cross-site") {
			writeErr(w, http.StatusForbidden, "cross-site browser requests are not permitted")
			return
		}
		if !allowed.allows(r.Host) {
			writeErr(w, http.StatusForbidden, "Host "+r.Host+" is not served here (DNS-rebinding guard)")
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && !originAllowed(allowed, origin) {
			writeErr(w, http.StatusForbidden, "cross-origin request from "+origin+" is not permitted")
			return
		}
		h.ServeHTTP(w, r)
	})
}

// originAllowed reports whether an Origin header value is one of ours. A
// value that isn't a parseable http(s) origin — notably the literal "null"
// a sandboxed iframe or a file:// page sends — is never allowed.
func originAllowed(allowed hostSet, origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	return allowed.allows(u.Host)
}
