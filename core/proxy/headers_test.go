package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Connection can name extension headers on multiple lines. None of those
// fields may cross the proxy in either direction; ordinary headers must.
func TestProxyStripsConnectionNamedHeaders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, name := range []string{"Connection", "X-Request-Hop", "X-Second-Hop"} {
			if got := r.Header.Get(name); got != "" {
				t.Errorf("upstream received hop-only %s: %q", name, got)
			}
		}
		if got := r.Header.Get("X-End-To-End"); got != "request" {
			t.Errorf("upstream end-to-end header = %q", got)
		}
		w.Header().Add("Connection", "X-Response-Hop")
		w.Header().Add("Connection", " x-second-hop , keep-alive")
		w.Header().Set("X-Response-Hop", "private-response")
		w.Header().Set("X-Second-Hop", "second-response")
		w.Header().Set("X-End-To-End", "response")
		io.WriteString(w, "ok")
	}))
	defer upstream.Close()
	p := New(NewRecorder(RecorderOpts{}))
	defer p.Close()
	request := httptest.NewRequest(http.MethodGet, "http://localhost/example", nil)
	request.Header.Add("Connection", "X-Request-Hop")
	request.Header.Add("Connection", " x-second-hop , keep-alive")
	request.Header.Set("X-Request-Hop", "private-request")
	request.Header.Set("X-Second-Hop", "second-request")
	request.Header.Set("X-End-To-End", "request")
	response := httptest.NewRecorder()
	p.handler(Target{Name: "api", Upstream: upstream.URL}).ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != "ok" {
		t.Fatalf("proxy response = %d %q", response.Code, response.Body.String())
	}
	for _, name := range []string{"Connection", "X-Response-Hop", "X-Second-Hop"} {
		if got := response.Header().Get(name); got != "" {
			t.Errorf("client received hop-only %s: %q", name, got)
		}
	}
	if got := response.Header().Get("X-End-To-End"); got != "response" {
		t.Errorf("client end-to-end header = %q", got)
	}
}
