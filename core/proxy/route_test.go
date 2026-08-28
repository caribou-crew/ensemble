package proxy

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/caribou-crew/ensemble/core/trace"
)

func TestResolveRoute(t *testing.T) {
	target := Target{Name: "gw", Routes: []Route{
		{Prefix: "/", Upstream: "http://root"},
		{Prefix: "/api", Upstream: "http://api"},
		{Prefix: "/api/orders", Upstream: "http://orders"},
		{Prefix: "/cart", Upstream: "http://cart", StripPrefix: true},
	}}
	cases := []struct {
		path, upstream, forward string
	}{
		{"/api/orders/7", "http://orders", "/api/orders/7"}, // longest wins
		{"/api/orders", "http://orders", "/api/orders"},     // exact match
		{"/api/users", "http://api", "/api/users"},
		{"/apiary", "http://root", "/apiary"}, // segment boundary: not /api
		{"/cartoon", "http://root", "/cartoon"},
		{"/cart/items", "http://cart", "/items"}, // stripped
		{"/cart", "http://cart", "/"},            // stripped to root
		{"/", "http://root", "/"},
		{"/anything/else", "http://root", "/anything/else"},
	}
	for _, c := range cases {
		up, fwd, ok := target.resolve(c.path)
		if !ok {
			t.Errorf("%s: no route", c.path)
			continue
		}
		if up != c.upstream || fwd != c.forward {
			t.Errorf("%s: got (%s, %s), want (%s, %s)", c.path, up, fwd, c.upstream, c.forward)
		}
	}
}

func TestResolveRouteRegexFallback(t *testing.T) {
	target := Target{Name: "gw", Routes: []Route{
		{Prefix: "/products", Upstream: "http://catalog"},
		{Regex: regexp.MustCompile(`\.(jpg|png)$`), Upstream: "http://assets"},
		{Regex: regexp.MustCompile(`^/v[0-9]+/legacy`), Upstream: "http://legacy"},
	}}
	cases := []struct {
		path, upstream string
		ok             bool
	}{
		{"/products/1", "http://catalog", true},       // prefix wins
		{"/img/cat.jpg", "http://assets", true},       // regex fallback
		{"/v2/legacy/x", "http://legacy", true},       // second regex, ^ anchor
		{"/nope", "", false},                          // no route matches
		{"/products.jpg", "http://assets", true},      // no prefix match (segment boundary): regex still applies
		{"/products/cat.jpg", "http://catalog", true}, // prefix DOES match here, and wins over the regex
	}
	for _, c := range cases {
		up, fwd, ok := target.resolve(c.path)
		if ok != c.ok {
			t.Errorf("%s: ok = %v, want %v", c.path, ok, c.ok)
			continue
		}
		if !ok {
			continue
		}
		if up != c.upstream {
			t.Errorf("%s: upstream = %q, want %q", c.path, up, c.upstream)
		}
		if fwd != c.path {
			t.Errorf("%s: regex/prefix-without-strip must forward unmodified path, got %q", c.path, fwd)
		}
	}
}

func TestResolveRouteRegexDeclarationOrder(t *testing.T) {
	// Among regex routes (no prefix routes at all), the first declared
	// match wins even if a later one would also match.
	target := Target{Name: "gw", Routes: []Route{
		{Regex: regexp.MustCompile(`\.json$`), Upstream: "http://first"},
		{Regex: regexp.MustCompile(`data\.json$`), Upstream: "http://second"},
	}}
	up, _, ok := target.resolve("/data.json")
	if !ok || up != "http://first" {
		t.Fatalf("got (%s, %v), want (http://first, true)", up, ok)
	}
}

func TestResolveRoutePrefixRewrite(t *testing.T) {
	target := Target{Name: "gw", Routes: []Route{
		{Prefix: "/v1/widgets", Upstream: "http://widgets", Rewrite: "/internal/v1/widgets"},
		{Prefix: "/", Upstream: "http://root", Rewrite: "/api"},
	}}
	cases := []struct{ path, forward string }{
		{"/v1/widgets/123", "/internal/v1/widgets/123"}, // remainder appended
		{"/v1/widgets", "/internal/v1/widgets"},         // exact match, no remainder
		{"/anything", "/api/anything"},                  // root prefix + rewrite: leading slash restored
	}
	for _, c := range cases {
		_, fwd, ok := target.resolve(c.path)
		if !ok {
			t.Errorf("%s: no route", c.path)
			continue
		}
		if fwd != c.forward {
			t.Errorf("%s: forward = %q, want %q", c.path, fwd, c.forward)
		}
	}
}

func TestResolveRouteRegexRewrite(t *testing.T) {
	target := Target{Name: "gw", Routes: []Route{
		{Regex: regexp.MustCompile(`/legacy-export$`), Upstream: "http://orders", Rewrite: "/internal/v1/export"},
	}}
	up, fwd, ok := target.resolve("/v1/reports/legacy-export")
	if !ok || up != "http://orders" {
		t.Fatalf("got (%s, %v), want (http://orders, true)", up, ok)
	}
	if want := "/v1/reports/internal/v1/export"; fwd != want {
		t.Errorf("forward = %q, want %q", fwd, want)
	}
}

func TestResolveRouteNoCatchAll(t *testing.T) {
	target := Target{Name: "gw", Routes: []Route{{Prefix: "/cart", Upstream: "http://cart"}}}
	if _, _, ok := target.resolve("/products"); ok {
		t.Fatal("expected no route for /products")
	}
	if _, _, ok := target.resolve("/cart/1"); !ok {
		t.Fatal("expected /cart/1 to route")
	}
}

func TestResolveRouteTrailingSlashPrefix(t *testing.T) {
	// A prefix written with a trailing slash behaves like one without.
	target := Target{Name: "gw", Routes: []Route{{Prefix: "/cart/", Upstream: "http://cart", StripPrefix: true}}}
	up, fwd, ok := target.resolve("/cart")
	if !ok || up != "http://cart" || fwd != "/" {
		t.Fatalf("got (%s, %s, %v)", up, fwd, ok)
	}
	if _, fwd, _ := target.resolve("/cart/x"); fwd != "/x" {
		t.Fatalf("got forward %q, want /x", fwd)
	}
}

// TestGatewayRoutesAcrossUpstreams drives one gateway listener in front of
// two upstreams — one of them behind its own intercept listener — and
// checks path/query forwarding, the 404 hop, and that the downstream hop
// names the gateway as its caller.
func TestGatewayRoutesAcrossUpstreams(t *testing.T) {
	rec := NewRecorder(RecorderOpts{Ring: 64, Redactor: mustRedactor(t, nil, 65536)})
	p := New(rec)
	defer p.Close()

	var catalogSaw, cartSaw string
	catalog := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		catalogSaw = r.URL.RequestURI()
		fmt.Fprint(w, `{"catalog":true}`)
	}))
	defer catalog.Close()
	catalogProxy, err := p.Serve(Target{Name: "catalog", Listen: "127.0.0.1:0", Upstream: catalog.URL})
	if err != nil {
		t.Fatal(err)
	}
	cart := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cartSaw = r.URL.RequestURI()
		fmt.Fprint(w, `{"cart":true}`)
	}))
	defer cart.Close()

	gw, err := p.Serve(Target{Name: "public", Listen: "127.0.0.1:0", Routes: []Route{
		{Prefix: "/products", Upstream: "http://" + catalogProxy},
		{Prefix: "/cart", Upstream: cart.URL, StripPrefix: true},
	}})
	if err != nil {
		t.Fatal(err)
	}

	get := func(path string) int {
		t.Helper()
		resp, err := http.Get("http://" + gw + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)
		return resp.StatusCode
	}

	if st := get("/products/1?x=y"); st != 200 {
		t.Fatalf("/products: status %d", st)
	}
	if catalogSaw != "/products/1?x=y" {
		t.Errorf("catalog saw %q, want /products/1?x=y", catalogSaw)
	}
	if st := get("/cart/items?limit=5"); st != 200 {
		t.Fatalf("/cart: status %d", st)
	}
	if cartSaw != "/items?limit=5" {
		t.Errorf("cart saw %q, want /items?limit=5", cartSaw)
	}
	if st := get("/nope"); st != 404 {
		t.Fatalf("/nope: status %d, want 404", st)
	}

	hops := rec.Snapshot()
	// Expect: gateway hop + catalog hop for /products, gateway hop for
	// /cart, gateway 404 hop for /nope.
	var gwHops, catalogHops []trace.Hop
	for _, h := range hops {
		switch h.To {
		case "public":
			gwHops = append(gwHops, h)
		case "catalog":
			catalogHops = append(catalogHops, h)
		}
	}
	if len(gwHops) != 3 {
		t.Fatalf("gateway hops: got %d, want 3: %+v", len(gwHops), gwHops)
	}
	if len(catalogHops) != 1 {
		t.Fatalf("catalog hops: got %d, want 1", len(catalogHops))
	}
	if catalogHops[0].From != "public" {
		t.Errorf("catalog hop From = %q, want public", catalogHops[0].From)
	}
	if catalogHops[0].TraceID != gwHops[0].TraceID || catalogHops[0].ParentSpanID != gwHops[0].SpanID {
		t.Errorf("catalog hop not parented to gateway hop: %+v vs %+v", catalogHops[0], gwHops[0])
	}
	var notFound *trace.Hop
	for i := range gwHops {
		if gwHops[i].Status == 404 {
			notFound = &gwHops[i]
		}
	}
	if notFound == nil || !strings.Contains(notFound.Err, "no route") || notFound.Path != "/nope" {
		t.Errorf("missing 404 hop with no-route Err: %+v", gwHops)
	}
}
