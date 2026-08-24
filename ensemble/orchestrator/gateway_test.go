package orchestrator

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/caribou-crew/ensemble/core/proxy"
	"github.com/caribou-crew/ensemble/ensemble/config"
)

func portOf(t *testing.T, srv *httptest.Server) int {
	t.Helper()
	_, p, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(p)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// TestUpWiresGateway: a gateway routes /products through the service's
// intercept port (two hops, From=gateway on the second) and /legacy to an
// unproxied service's real port, stripping the prefix.
func TestUpWiresGateway(t *testing.T) {
	catalog := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "catalog:"+r.URL.Path)
	}))
	defer catalog.Close()
	legacy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "legacy:"+r.URL.Path)
	}))
	defer legacy.Close()

	catalogProxy := freePort(t)
	gwPort := freePort(t)
	cfg := &config.Config{
		Dir: t.TempDir(),
		Services: map[string]config.Service{
			"catalog": {Run: "sleep 30", Port: portOf(t, catalog), Proxy: catalogProxy},
			"legacy":  {Run: "sleep 30", Port: portOf(t, legacy)},
		},
		Gateways: map[string]config.Gateway{
			"public": {Port: gwPort, Routes: []config.GatewayRoute{
				{Prefix: "/products", Service: "catalog"},
				{Prefix: "/legacy", Service: "legacy", StripPrefix: true},
			}},
		},
	}
	rec := proxy.NewRecorder(proxy.RecorderOpts{Ring: 64})
	px := proxy.New(rec)
	defer px.Close()
	o := New(cfg, px, Opts{LogDir: t.TempDir()})
	if err := o.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	defer o.Down()

	get := func(path string) string {
		t.Helper()
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d%s", gwPort, path))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return fmt.Sprintf("%d %s", resp.StatusCode, b)
	}
	if got := get("/products/1"); got != "200 catalog:/products/1" {
		t.Errorf("/products/1: %q", got)
	}
	if got := get("/legacy/x"); got != "200 legacy:/x" {
		t.Errorf("/legacy/x: %q", got)
	}
	if got := get("/nope"); !strings.HasPrefix(got, "404 ") {
		t.Errorf("/nope: %q", got)
	}

	var fromGateway int
	for _, h := range rec.Snapshot() {
		if h.To == "catalog" && h.From == "public" {
			fromGateway++
		}
	}
	if fromGateway != 1 {
		t.Errorf("catalog hops called by gateway: got %d, want 1", fromGateway)
	}
	if _, ok := o.Service("public"); ok {
		t.Error("gateway must not appear as a supervised ServiceState")
	}
}

// TestUpWiresGatewayRegex: a regex route matches a suffix that no prefix
// route claims, and prefix still wins where both could apply.
func TestUpWiresGatewayRegex(t *testing.T) {
	catalog := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "catalog:"+r.URL.Path)
	}))
	defer catalog.Close()
	assets := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "assets:"+r.URL.Path)
	}))
	defer assets.Close()

	gwPort := freePort(t)
	cfg := &config.Config{
		Dir: t.TempDir(),
		Services: map[string]config.Service{
			"catalog": {Run: "sleep 30", Port: portOf(t, catalog)},
			"assets":  {Run: "sleep 30", Port: portOf(t, assets)},
		},
		Gateways: map[string]config.Gateway{
			"public": {Port: gwPort, Routes: []config.GatewayRoute{
				{Prefix: "/products", Service: "catalog"},
				{Regex: `\.(jpg|png)$`, Service: "assets"},
			}},
		},
	}
	rec := proxy.NewRecorder(proxy.RecorderOpts{Ring: 64})
	px := proxy.New(rec)
	defer px.Close()
	o := New(cfg, px, Opts{LogDir: t.TempDir()})
	if err := o.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	defer o.Down()

	get := func(path string) string {
		t.Helper()
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d%s", gwPort, path))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return fmt.Sprintf("%d %s", resp.StatusCode, b)
	}
	if got := get("/img/cat.jpg"); got != "200 assets:/img/cat.jpg" {
		t.Errorf("/img/cat.jpg: %q", got)
	}
	if got := get("/products/cat.jpg"); got != "200 catalog:/products/cat.jpg" {
		t.Errorf("/products/cat.jpg: %q (prefix must win over regex)", got)
	}
	if got := get("/nope"); !strings.HasPrefix(got, "404 ") {
		t.Errorf("/nope: %q", got)
	}
}

// TestUpWiresGatewayRewrite: a prefix route with rewrite replaces the
// matched prefix (not just strips it), and a regex route with rewrite
// replaces only the matched substring.
func TestUpWiresGatewayRewrite(t *testing.T) {
	widgets := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "widgets:"+r.URL.Path)
	}))
	defer widgets.Close()

	gwPort := freePort(t)
	cfg := &config.Config{
		Dir: t.TempDir(),
		Services: map[string]config.Service{
			"widgets": {Run: "sleep 30", Port: portOf(t, widgets)},
		},
		Gateways: map[string]config.Gateway{
			"public": {Port: gwPort, Routes: []config.GatewayRoute{
				{Prefix: "/v1/widgets", Service: "widgets", Rewrite: "/internal/v1/widgets"},
				{Regex: `/legacy-export$`, Service: "widgets", Rewrite: "/internal/v1/export"},
			}},
		},
	}
	rec := proxy.NewRecorder(proxy.RecorderOpts{Ring: 64})
	px := proxy.New(rec)
	defer px.Close()
	o := New(cfg, px, Opts{LogDir: t.TempDir()})
	if err := o.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	defer o.Down()

	get := func(path string) string {
		t.Helper()
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d%s", gwPort, path))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return fmt.Sprintf("%d %s", resp.StatusCode, b)
	}
	if got := get("/v1/widgets/123"); got != "200 widgets:/internal/v1/widgets/123" {
		t.Errorf("/v1/widgets/123: %q", got)
	}
	if got := get("/v1/reports/legacy-export"); got != "200 widgets:/v1/reports/internal/v1/export" {
		t.Errorf("/v1/reports/legacy-export: %q", got)
	}
}

// TestUpWiresGatewayCORS: the gateway answers a preflight directly and adds
// CORS headers to a forwarded response, driven end to end through Up.
func TestUpWiresGatewayCORS(t *testing.T) {
	var upstreamCalls int
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		fmt.Fprint(w, "ok")
	}))
	defer svc.Close()

	gwPort := freePort(t)
	cfg := &config.Config{
		Dir: t.TempDir(),
		Services: map[string]config.Service{
			"svc": {Run: "sleep 30", Port: portOf(t, svc)},
		},
		Gateways: map[string]config.Gateway{
			"public": {Port: gwPort, Routes: []config.GatewayRoute{
				{Prefix: "/", Service: "svc"},
			}, CORS: &config.CORSConfig{
				AllowOrigins: []string{"http://localhost:3000"},
				AllowMethods: []string{"GET", "PUT"},
			}},
		},
	}
	rec := proxy.NewRecorder(proxy.RecorderOpts{Ring: 64})
	px := proxy.New(rec)
	defer px.Close()
	o := New(cfg, px, Opts{LogDir: t.TempDir()})
	if err := o.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	defer o.Down()

	req, _ := http.NewRequest(http.MethodOptions, fmt.Sprintf("http://127.0.0.1:%d/x", gwPort), nil)
	req.Header.Set("Origin", "http://localhost:3000")
	req.Header.Set("Access-Control-Request-Method", "PUT")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("preflight status = %d, want 204", resp.StatusCode)
	}
	if upstreamCalls != 0 {
		t.Errorf("preflight must not call upstream, got %d calls", upstreamCalls)
	}

	req, _ = http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/x", gwPort), nil)
	req.Header.Set("Origin", "http://localhost:3000")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "http://localhost:3000" {
		t.Errorf("Access-Control-Allow-Origin = %q", got)
	}
	if upstreamCalls != 1 {
		t.Errorf("forwarded request must call upstream once, got %d calls", upstreamCalls)
	}
}

// TestUpWiresGatewayCORSPassthrough: a mixed-backend gateway — one route's
// backend already emits its own CORS headers and must be left alone
// (cors_passthrough: true), another route's backend has none and still
// needs the gateway's cors: block. Driven end to end through Up.
func TestUpWiresGatewayCORSPassthrough(t *testing.T) {
	ownCORS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "http://custom.example")
		fmt.Fprint(w, "own-cors")
	}))
	defer ownCORS.Close()
	noCORS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "no-cors")
	}))
	defer noCORS.Close()

	gwPort := freePort(t)
	cfg := &config.Config{
		Dir: t.TempDir(),
		Services: map[string]config.Service{
			"panel":  {Run: "sleep 30", Port: portOf(t, ownCORS)},
			"widget": {Run: "sleep 30", Port: portOf(t, noCORS)},
		},
		Gateways: map[string]config.Gateway{
			"public": {Port: gwPort, Routes: []config.GatewayRoute{
				{Prefix: "/panel", Service: "panel", CORSPassthrough: true},
				{Prefix: "/", Service: "widget"},
			}, CORS: &config.CORSConfig{
				AllowOrigins: []string{"http://localhost:3000"},
				AllowMethods: []string{"GET", "PUT"},
			}},
		},
	}
	rec := proxy.NewRecorder(proxy.RecorderOpts{Ring: 64})
	px := proxy.New(rec)
	defer px.Close()
	o := New(cfg, px, Opts{LogDir: t.TempDir()})
	if err := o.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	defer o.Down()

	req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/panel/x", gwPort), nil)
	req.Header.Set("Origin", "http://localhost:3000")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if got := resp.Header.Values("Access-Control-Allow-Origin"); len(got) != 1 || got[0] != "http://custom.example" {
		t.Errorf("passthrough route Access-Control-Allow-Origin = %v, want exactly the backend's own", got)
	}

	req, _ = http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/widget", gwPort), nil)
	req.Header.Set("Origin", "http://localhost:3000")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "http://localhost:3000" {
		t.Errorf("non-passthrough route Access-Control-Allow-Origin = %q, want the gateway's own", got)
	}
}

func TestUpGatewayBindFailureNamesGateway(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	taken := ln.Addr().(*net.TCPAddr).Port

	cfg := &config.Config{
		Dir:      t.TempDir(),
		Services: map[string]config.Service{"svc": {Run: "sleep 30", Port: freePort(t)}},
		Gateways: map[string]config.Gateway{
			"public": {Port: taken, Routes: []config.GatewayRoute{{Prefix: "/", Service: "svc"}}},
		},
	}
	px := proxy.New(proxy.NewRecorder(proxy.RecorderOpts{Ring: 8}))
	defer px.Close()
	o := New(cfg, px, Opts{LogDir: t.TempDir()})
	err = o.Up(context.Background())
	if err == nil || !strings.Contains(err.Error(), "gateway public") {
		t.Fatalf("want bind error naming the gateway, got %v", err)
	}
	if _, ok := o.Service("svc"); ok {
		t.Error("service must not have been started when the gateway failed to bind")
	}
}
