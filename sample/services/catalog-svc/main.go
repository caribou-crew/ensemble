// Command catalog-svc is the "brew" sample stack's product catalog: real
// CRUD over Postgres, fronted by ensemble's proxy like any other service.
// It knows nothing about ensemble beyond two ordinary HTTP headers — see
// forwardTraceHeaders.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Product struct {
	ID         int64     `json:"id"`
	Name       string    `json:"name"`
	PriceCents int64     `json:"price_cents"`
	CreatedAt  time.Time `json:"created_at"`
}

var pool *pgxpool.Pool

func main() {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		log.Fatal("DATABASE_URL is required")
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8081"
	}
	paymentsURL := os.Getenv("PAYMENTS_URL")

	ctx := context.Background()
	var err error
	pool, err = connectWithRetry(ctx, dsn, 30*time.Second)
	if err != nil {
		log.Fatalf("connect to database: %v", err)
	}
	defer pool.Close()

	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS products (
			id          BIGSERIAL PRIMARY KEY,
			name        TEXT NOT NULL UNIQUE,
			price_cents BIGINT NOT NULL,
			created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
		)`); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handleHealthz)
	mux.HandleFunc("GET /products", handleList)
	mux.HandleFunc("POST /products", handleCreate)
	mux.HandleFunc("GET /products/{id}", handleGet)
	mux.HandleFunc("PUT /products/{id}", handleUpdate)
	mux.HandleFunc("DELETE /products/{id}", handleDelete)
	mux.HandleFunc("POST /products/{id}/purchase", handlePurchase(paymentsURL))

	log.Printf("catalog-svc listening on :%s (payments=%s)", port, paymentsURL)
	log.Fatal(http.ListenAndServe(":"+port, mux))
}

// connectWithRetry polls until Postgres accepts connections. The container
// can take a few seconds to come up after `docker run` returns, and
// ensemble's health gate (which polls /healthz) is what actually blocks
// `ensemble up` — this just means catalog-svc itself doesn't need a
// separate readiness state machine.
func connectWithRetry(ctx context.Context, dsn string, timeout time.Duration) (*pgxpool.Pool, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		p, err := pgxpool.New(ctx, dsn)
		if err == nil {
			if err = p.Ping(ctx); err == nil {
				return p, nil
			}
			p.Close()
		}
		lastErr = err
		time.Sleep(500 * time.Millisecond)
	}
	return nil, fmt.Errorf("timed out after %s: %w", timeout, lastErr)
}

func handleHealthz(w http.ResponseWriter, r *http.Request) {
	if err := pool.Ping(r.Context()); err != nil {
		http.Error(w, "db unreachable", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func handleList(w http.ResponseWriter, r *http.Request) {
	rows, err := pool.Query(r.Context(), `SELECT id, name, price_cents, created_at FROM products ORDER BY id`)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	products := []Product{}
	for rows.Next() {
		var p Product
		if err := rows.Scan(&p.ID, &p.Name, &p.PriceCents, &p.CreatedAt); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		products = append(products, p)
	}
	writeJSON(w, http.StatusOK, products)
}

func handleGet(w http.ResponseWriter, r *http.Request) {
	var p Product
	err := pool.QueryRow(r.Context(),
		`SELECT id, name, price_cents, created_at FROM products WHERE id = $1`, r.PathValue("id"),
	).Scan(&p.ID, &p.Name, &p.PriceCents, &p.CreatedAt)
	if err != nil {
		http.Error(w, "product not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func handleCreate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name       string `json:"name"`
		PriceCents int64  `json:"price_cents"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}

	var p Product
	err := pool.QueryRow(r.Context(),
		`INSERT INTO products (name, price_cents) VALUES ($1, $2)
		 RETURNING id, name, price_cents, created_at`,
		in.Name, in.PriceCents,
	).Scan(&p.ID, &p.Name, &p.PriceCents, &p.CreatedAt)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

func handleUpdate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name       string `json:"name"`
		PriceCents int64  `json:"price_cents"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}

	var p Product
	err := pool.QueryRow(r.Context(),
		`UPDATE products SET name = $1, price_cents = $2 WHERE id = $3
		 RETURNING id, name, price_cents, created_at`,
		in.Name, in.PriceCents, r.PathValue("id"),
	).Scan(&p.ID, &p.Name, &p.PriceCents, &p.CreatedAt)
	if err != nil {
		http.Error(w, "product not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func handleDelete(w http.ResponseWriter, r *http.Request) {
	tag, err := pool.Exec(r.Context(), `DELETE FROM products WHERE id = $1`, r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if tag.RowsAffected() == 0 {
		http.Error(w, "product not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handlePurchase "buys" a product by calling out to the payments stub —
// this is the one call in the vertical slice that isn't a straight
// passthrough, so it's the one place that needs forwardTraceHeaders below.
func handlePurchase(paymentsURL string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var p Product
		err := pool.QueryRow(r.Context(),
			`SELECT id, name, price_cents, created_at FROM products WHERE id = $1`, r.PathValue("id"),
		).Scan(&p.ID, &p.Name, &p.PriceCents, &p.CreatedAt)
		if err != nil {
			http.Error(w, "product not found", http.StatusNotFound)
			return
		}
		if paymentsURL == "" {
			http.Error(w, "PAYMENTS_URL not configured", http.StatusServiceUnavailable)
			return
		}

		body, _ := json.Marshal(map[string]any{
			"product_id":   p.ID,
			"amount_cents": p.PriceCents,
		})
		req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, paymentsURL+"/charges", bytes.NewReader(body))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		req.Header.Set("content-type", "application/json")
		forwardTraceHeaders(req.Header, r.Header)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			http.Error(w, fmt.Sprintf("payments call failed: %v", err), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		w.Header().Set("content-type", "application/json")
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	}
}

// forwardTraceHeaders is the whole propagation contract: carry the W3C
// traceparent/baggage headers ensemble's proxy stamped on the inbound
// request onto any outbound request this service makes on its own behalf,
// so the hop chain in the dashboard doesn't break. No ensemble dependency
// required — just two headers.
func forwardTraceHeaders(dst, src http.Header) {
	if tp := src.Get("traceparent"); tp != "" {
		dst.Set("traceparent", tp)
	}
	if bg := src.Get("baggage"); bg != "" {
		dst.Set("baggage", bg)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
