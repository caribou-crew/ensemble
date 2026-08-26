// Command order-stub is the "brew" sample stack's default backing for the
// order service (see ensemble.yaml's `order` service `variants:` block): an
// in-memory reimplementation of order-svc's checkout flow (OrderController,
// ../order-svc) with no database and no JVM, so the money path works with
// no JDK installed. It talks to catalog-svc, user-svc, the payments stub,
// and the same Redis notify queue the real order-svc does — reproducing
// exactly the requests and responses storefront-bff/ops-bff already expect.
// Swap to the real implementation on demand with `ensemble variant order
// real` (or `ensemble up --variant order=real`).
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

const notifyQueueKey = "orders:notify"

type orderItem struct {
	ID             int64 `json:"id"`
	ProductID      int64 `json:"product_id"`
	Quantity       int   `json:"quantity"`
	UnitPriceCents int64 `json:"unit_price_cents"`
}

type order struct {
	ID         int64       `json:"id"`
	UserID     int64       `json:"user_id"`
	Status     string      `json:"status"`
	TotalCents int64       `json:"total_cents"`
	CreatedAt  time.Time   `json:"created_at"`
	Items      []orderItem `json:"items"`
}

type createOrderRequest struct {
	UserID int64 `json:"user_id"`
	Items  []struct {
		ProductID int64 `json:"product_id"`
		Quantity  int   `json:"quantity"`
	} `json:"items"`
}

// store is the whole "database": in-memory only, reset on restart — the
// stub's entire reason to exist is skipping a real datastore.
var store struct {
	mu     sync.Mutex
	orders []order
	nextID int64
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8083"
	}
	catalogURL := requireEnv("CATALOG_URL")
	userURL := requireEnv("USER_URL")
	paymentsURL := requireEnv("PAYMENTS_URL")
	store.nextID = 1

	rdb := redis.NewClient(&redis.Options{
		Addr: os.Getenv("REDIS_HOST") + ":" + os.Getenv("REDIS_PORT"),
	})
	defer rdb.Close()

	mux := http.NewServeMux()
	// order-svc's real (Spring) variant serves health at /actuator/health —
	// `health:` is a service-level field shared by every variant (see
	// ensemble.yaml), so the stub answers the same path.
	mux.HandleFunc("GET /actuator/health", handleHealth)
	mux.HandleFunc("GET /orders", handleList)
	mux.HandleFunc("GET /orders/{id}", handleGet)
	mux.HandleFunc("POST /orders", handleCreate(catalogURL, userURL, paymentsURL, rdb))

	log.Printf("order-stub listening on :%s (catalog=%s user=%s payments=%s)", port, catalogURL, userURL, paymentsURL)
	log.Fatal(http.ListenAndServe(":"+port, mux))
}

func requireEnv(name string) string {
	v := os.Getenv(name)
	if v == "" {
		log.Fatalf("%s is required", name)
	}
	return v
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "UP"})
}

func handleList(w http.ResponseWriter, r *http.Request) {
	store.mu.Lock()
	defer store.mu.Unlock()
	writeJSON(w, http.StatusOK, store.orders)
}

func handleGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, o := range store.orders {
		if fmt.Sprint(o.ID) == id {
			writeJSON(w, http.StatusOK, o)
			return
		}
	}
	w.WriteHeader(http.StatusNotFound)
}

// handleCreate mirrors OrderController.create: look up the user, price
// every line item against catalog-svc, charge the total via the payments
// stub, then — same as the real order-svc — push a notify-worker job onto
// the shared Redis queue only when the charge succeeded.
func handleCreate(catalogURL, userURL, paymentsURL string, rdb *redis.Client) http.HandlerFunc {
	client := &http.Client{Timeout: 5 * time.Second}

	return func(w http.ResponseWriter, r *http.Request) {
		var in createOrderRequest
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}

		userReq, _ := http.NewRequestWithContext(r.Context(), http.MethodGet,
			fmt.Sprintf("%s/users/%d", userURL, in.UserID), nil)
		forwardTraceHeaders(userReq.Header, r.Header)
		userResp, err := client.Do(userReq)
		if err != nil || userResp.StatusCode != http.StatusOK {
			if userResp != nil {
				userResp.Body.Close()
			}
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("unknown user %d", in.UserID)})
			return
		}
		userResp.Body.Close()

		items := make([]orderItem, 0, len(in.Items))
		var total int64
		for _, line := range in.Items {
			prodReq, _ := http.NewRequestWithContext(r.Context(), http.MethodGet,
				fmt.Sprintf("%s/products/%d", catalogURL, line.ProductID), nil)
			forwardTraceHeaders(prodReq.Header, r.Header)
			prodResp, err := client.Do(prodReq)
			if err != nil || prodResp.StatusCode != http.StatusOK {
				if prodResp != nil {
					prodResp.Body.Close()
				}
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("unknown product %d", line.ProductID)})
				return
			}
			var product struct {
				PriceCents int64 `json:"price_cents"`
			}
			body, _ := io.ReadAll(prodResp.Body)
			prodResp.Body.Close()
			if err := json.Unmarshal(body, &product); err != nil {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": "malformed catalog response"})
				return
			}

			items = append(items, orderItem{
				ProductID:      line.ProductID,
				Quantity:       line.Quantity,
				UnitPriceCents: product.PriceCents,
			})
			total += product.PriceCents * int64(line.Quantity)
		}

		chargeBody, _ := json.Marshal(map[string]any{"amount_cents": total, "user_id": in.UserID})
		chargeReq, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, paymentsURL+"/charges", bytes.NewReader(chargeBody))
		chargeReq.Header.Set("content-type", "application/json")
		forwardTraceHeaders(chargeReq.Header, r.Header)
		chargeResp, chargeErr := client.Do(chargeReq)
		status := "payment_failed"
		if chargeErr == nil && chargeResp.StatusCode/100 == 2 {
			status = "paid"
		}
		if chargeResp != nil {
			chargeResp.Body.Close()
		}

		store.mu.Lock()
		saved := order{
			ID:         store.nextID,
			UserID:     in.UserID,
			Status:     status,
			TotalCents: total,
			CreatedAt:  time.Now().UTC(),
			Items:      items,
		}
		for i := range saved.Items {
			saved.Items[i].ID = saved.ID*1000 + int64(i) // synthetic, unique enough for the demo
		}
		store.nextID++
		store.orders = append(store.orders, saved)
		store.mu.Unlock()

		if status == "paid" {
			notify, _ := json.Marshal(map[string]int64{
				"order_id":    saved.ID,
				"user_id":     saved.UserID,
				"total_cents": saved.TotalCents,
			})
			if err := rdb.LPush(r.Context(), notifyQueueKey, notify).Err(); err != nil {
				log.Printf("push to %s: %v", notifyQueueKey, err)
			}
		}

		writeJSON(w, http.StatusCreated, saved)
	}
}

// forwardTraceHeaders is the same propagation contract as catalog-svc's
// forwardTraceHeaders and order-svc's TraceHeaders.forward: carry the W3C
// traceparent/baggage headers ensemble's proxy stamped on the inbound
// request onto every outbound call this service makes on its own behalf.
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
	_ = json.NewEncoder(w).Encode(v)
}
