package serve

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/config"
)

func TestPostRedactAppendsAnEntryAndTheNextQueueReadSeesIt(t *testing.T) {
	cwd := t.TempDir()
	recordRun(t, cwd, "web", "checkout", runA, nil,
		[]trace.Hop{hop(1, "GET", "/checkout", 200, `{"account_number":"1111111111111111"}`)})

	ts := newServer(t, cwd)
	doc := mustOK(t, post(t, ts, "/api/queue/web/checkout/redact",
		`{"field":"account_number","mode":"encrypt","why":"assert against the real value in CI"}`), "POST redact")
	if doc["ok"] != true {
		t.Fatalf("POST redact did not report ok: %v", doc)
	}
	echoed, ok := doc["redact"].(map[string]any)
	if !ok || echoed["field"] != "account_number" || echoed["mode"] != "encrypt" {
		t.Fatalf("POST redact did not echo the entry it wrote: %v", doc)
	}

	// Written to the OVERLAY, not retrace.yaml — same reason AppendWireRule
	// writes there: re-emitting YAML would delete a human's comments.
	raw, err := os.ReadFile(filepath.Join(cwd, config.RedactOverlayPath))
	if err != nil {
		t.Fatalf("reading the redact overlay: %v", err)
	}
	var overlay []config.RedactEntry
	if err := json.Unmarshal(raw, &overlay); err != nil {
		t.Fatalf("unmarshaling the redact overlay: %v", err)
	}
	if len(overlay) != 1 || overlay[0].Field != "account_number" {
		t.Fatalf("the overlay does not carry the entry: %+v", overlay)
	}

	// Reloaded before responding, so the very next GET sees it — same
	// contract handleRule's own test pins.
	rules, ok := doc["rules"].([]any)
	if !ok || len(rules) != 1 {
		t.Fatalf("POST redact did not return the redact rules now in effect: %v", doc["rules"])
	}

	// Config.Discover on the next request must also pick it up, proving the
	// merge (not just the reload this one process happened to keep in
	// memory).
	cfg, err := config.Discover(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Redact) != 1 || cfg.Redact[0].Field != "account_number" || cfg.Redact[0].Mode != "encrypt" {
		t.Fatalf("Discover after POST redact = %+v, want the new entry merged in", cfg.Redact)
	}
}

func TestPostRedactRejectsAnEmptyField(t *testing.T) {
	cwd := t.TempDir()
	recordRun(t, cwd, "web", "checkout", runA, nil,
		[]trace.Hop{hop(1, "GET", "/checkout", 200, `{}`)})

	ts := newServer(t, cwd)
	resp := post(t, ts, "/api/queue/web/checkout/redact", `{"field":""}`)
	if resp.status != 400 {
		t.Fatalf("status = %d, want 400 for an empty field name: %s", resp.status, resp.body)
	}
}

func TestPostRedactRejectsAnUnknownMode(t *testing.T) {
	cwd := t.TempDir()
	recordRun(t, cwd, "web", "checkout", runA, nil,
		[]trace.Hop{hop(1, "GET", "/checkout", 200, `{}`)})

	ts := newServer(t, cwd)
	resp := post(t, ts, "/api/queue/web/checkout/redact", `{"field":"card","mode":"hide"}`)
	if resp.status != 400 {
		t.Fatalf("status = %d, want 400 for an unknown mode: %s", resp.status, resp.body)
	}
}
