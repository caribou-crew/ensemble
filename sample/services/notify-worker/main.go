// Command notify-worker is the "brew" sample stack's order-notification
// worker: it drains the Redis work queue orders:notify and simulates
// sending a notification for each order by logging it and appending it to
// a local NDJSON file. It knows nothing about ensemble beyond exposing a
// plain health endpoint like any other service in this stack.
package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

const notifyQueueKey = "orders:notify"

// notificationsLogPath is relative to the process's working directory —
// ensemble runs each service from its own service dir, which is how this
// simulates a local append-only log a real notification service might keep.
const notificationsLogPath = "./notifications.log"

// order is the opaque shape popped off the queue. We don't validate it
// strictly — just decode enough to log something human-readable and pass
// the rest through.
type order struct {
	OrderID    int64 `json:"order_id"`
	UserID     int64 `json:"user_id"`
	TotalCents int64 `json:"total_cents"`
}

// healthy tracks whether the last Redis PING succeeded. Read by the
// /healthz handler, written by the periodic pinger.
var healthy atomic.Bool

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8084"
	}
	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		log.Fatal("REDIS_URL is required")
	}

	opt, err := redis.ParseURL(redisURL)
	if err != nil {
		log.Fatalf("parse REDIS_URL: %v", err)
	}
	rdb := redis.NewClient(opt)
	defer rdb.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go runHealthPinger(ctx, rdb)
	go runWorker(ctx, rdb)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handleHealthz)

	log.Printf("notify-worker listening on :%s (redis=%s)", port, redisURL)
	log.Fatal(http.ListenAndServe(":"+port, mux))
}

// runHealthPinger periodically PINGs Redis and records the result so
// /healthz can answer instantly without blocking on Redis per-request.
func runHealthPinger(ctx context.Context, rdb *redis.Client) {
	ping := func() {
		pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		healthy.Store(rdb.Ping(pingCtx).Err() == nil)
	}
	ping()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ping()
		}
	}
}

func handleHealthz(w http.ResponseWriter, r *http.Request) {
	if !healthy.Load() {
		http.Error(w, "redis unreachable", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// runWorker blocking-pops from the Redis list orders:notify and processes
// each message. BRPop's timeout keeps this looping (rather than blocking
// forever) so it notices ctx cancellation and transient Redis errors.
func runWorker(ctx context.Context, rdb *redis.Client) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		res, err := rdb.BRPop(ctx, 5*time.Second, notifyQueueKey).Result()
		if err != nil {
			if err == redis.Nil {
				// Timed out with nothing queued — loop and try again.
				continue
			}
			if ctx.Err() != nil {
				return
			}
			log.Printf("notify-worker: brpop error: %v", err)
			time.Sleep(time.Second)
			continue
		}

		// res is [key, value] on success.
		if len(res) != 2 {
			continue
		}
		processMessage(res[1])
	}
}

// processMessage logs a human-readable line and appends the message as one
// NDJSON line to notificationsLogPath with an added receivedAt timestamp.
// This simulates "sent a notification" for the demo.
func processMessage(raw string) {
	receivedAt := time.Now().UTC().Format(time.RFC3339)

	var o order
	if err := json.Unmarshal([]byte(raw), &o); err != nil {
		log.Printf("notify-worker: failed to parse message: %v (raw=%s)", err, raw)
	} else {
		log.Printf("notified order %d for user %d: $%.2f", o.OrderID, o.UserID, float64(o.TotalCents)/100)
	}

	if err := appendNotification(raw, receivedAt); err != nil {
		log.Printf("notify-worker: failed to write notifications log: %v", err)
	}
}

// appendNotification appends raw (a JSON object) plus a receivedAt field as
// one NDJSON line to notificationsLogPath. It works directly with the raw
// bytes so a message that fails order-schema decoding is still recorded.
func appendNotification(raw, receivedAt string) error {
	var fields map[string]any
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		// Not a JSON object — record it verbatim alongside the timestamp.
		fields = map[string]any{"raw": raw}
	}
	fields["receivedAt"] = receivedAt

	line, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	line = append(line, '\n')

	f, err := os.OpenFile(notificationsLogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = f.Write(line)
	return err
}
