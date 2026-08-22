package proxy

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
)

// CORSPolicy is a gateway Target's cross-origin resource sharing
// configuration. A nil *CORSPolicy (the default) disables CORS entirely:
// no response headers are added and OPTIONS requests are forwarded like
// any other request.
type CORSPolicy struct {
	// AllowOrigins lists the origins allowed to read a response. A single
	// "*" entry allows any origin, but only when AllowCredentials is
	// false — the Fetch spec forbids the combination, and ensemble's
	// config validation rejects it before it ever reaches here.
	AllowOrigins     []string
	AllowCredentials bool
	AllowMethods     []string
	AllowHeaders     []string
	// MaxAgeSeconds, when > 0, sets Access-Control-Max-Age.
	MaxAgeSeconds int
}

// headers computes the CORS response headers for a request whose Origin
// header value is origin. ok is false when origin is empty or not
// allowed, in which case no headers should be added and no preflight
// short-circuit should be taken — the request just proceeds normally (the
// browser enforces CORS client-side; ensemble doesn't need to reject
// anything server-side).
func (c *CORSPolicy) headers(origin string) (h http.Header, ok bool) {
	if c == nil || origin == "" {
		return nil, false
	}
	allow := ""
	switch {
	case len(c.AllowOrigins) == 1 && c.AllowOrigins[0] == "*" && !c.AllowCredentials:
		allow = "*"
	case slices.Contains(c.AllowOrigins, origin):
		allow = origin
	default:
		return nil, false
	}
	h = http.Header{}
	h.Set("Access-Control-Allow-Origin", allow)
	if allow != "*" {
		// A per-origin response varies by the Origin request header, so
		// caches must not conflate it with a response for another origin.
		h.Add("Vary", "Origin")
	}
	if c.AllowCredentials {
		h.Set("Access-Control-Allow-Credentials", "true")
	}
	if len(c.AllowMethods) > 0 {
		h.Set("Access-Control-Allow-Methods", strings.Join(c.AllowMethods, ", "))
	}
	if len(c.AllowHeaders) > 0 {
		h.Set("Access-Control-Allow-Headers", strings.Join(c.AllowHeaders, ", "))
	}
	if c.MaxAgeSeconds > 0 {
		h.Set("Access-Control-Max-Age", strconv.Itoa(c.MaxAgeSeconds))
	}
	return h, true
}

// isPreflight reports whether r is a CORS preflight request per the Fetch
// spec: an OPTIONS request carrying Access-Control-Request-Method.
func isPreflight(r *http.Request) bool {
	return r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != ""
}
