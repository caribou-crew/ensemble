// Command edge-gw is the "brew" sample stack's entry point: emulates an
// edge gateway (envoy/nginx role) with a stub auth check in front of
// catalog-svc. Being a plain net/http/httputil reverse proxy, it forwards
// every header — including traceparent/baggage — to catalog's proxy port
// with no extra code, which is the other half of the propagation story:
// a transparent proxy gets it for free, an app-level call (see
// catalog-svc's handlePurchase) has to do it explicitly.
package main

import (
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
)

const demoToken = "demo-token"

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	catalogURL := os.Getenv("CATALOG_URL")
	if catalogURL == "" {
		log.Fatal("CATALOG_URL is required")
	}

	target, err := url.Parse(catalogURL)
	if err != nil {
		log.Fatalf("parse CATALOG_URL: %v", err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.Handle("/products", requireAuth(proxy))
	mux.Handle("/products/", requireAuth(proxy))

	log.Printf("edge-gw listening on :%s (catalog=%s)", port, catalogURL)
	log.Fatal(http.ListenAndServe(":"+port, mux))
}

// requireAuth is the auth stub: a fixed bearer token stands in for whatever
// real auth a company's edge actually does. Nothing else in the sample
// stack cares about auth — this exists so the trace shows a real 401 path.
func requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+demoToken {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
