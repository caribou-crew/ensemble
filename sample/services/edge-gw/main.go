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
	catalogProxy := mustProxy("CATALOG_URL")
	storefrontProxy := mustProxy("STOREFRONT_URL")

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.Handle("/products", requireAuth(catalogProxy))
	mux.Handle("/products/", requireAuth(catalogProxy))
	mux.Handle("/cart/", requireAuth(storefrontProxy))

	log.Printf("edge-gw listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, withCORS(mux)))
}

// withCORS lets web-app (served from its own dev-server origin) call edge-gw
// directly from the browser — a real edge/envoy layer owns CORS the same
// way it owns auth, so it belongs here rather than in every client.
func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// mustProxy builds a reverse proxy to the service named by the env var
// envName (its proxy port, so the hop gets captured — never the real port).
func mustProxy(envName string) http.Handler {
	raw := os.Getenv(envName)
	if raw == "" {
		log.Fatalf("%s is required", envName)
	}
	target, err := url.Parse(raw)
	if err != nil {
		log.Fatalf("parse %s: %v", envName, err)
	}
	return httputil.NewSingleHostReverseProxy(target)
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
