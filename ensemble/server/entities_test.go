package server_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/caribou-crew/ensemble/ensemble/config"
	"github.com/caribou-crew/ensemble/ensemble/server"
)

// echoUpstream is a test entity backend that reports exactly what it
// received (method, path, raw query, body) so passthrough tests can assert
// on all of it.
func echoUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Upstream", "yes")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"method": r.Method,
			"path":   r.URL.Path,
			"query":  r.URL.RawQuery,
			"body":   string(body),
		})
	}))
	t.Cleanup(ts.Close)
	return ts
}

func newEntitiesTestEnv(t *testing.T, entities map[string]config.Entity) *httptest.Server {
	t.Helper()
	cfg := &config.Config{Dir: t.TempDir(), Entities: entities}
	handler := server.New(server.Deps{Cfg: cfg, Version: "test"})
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts
}

func TestEntitiesDiscoveryList(t *testing.T) {
	ts := newEntitiesTestEnv(t, map[string]config.Entity{
		"users":  {Base: "http://127.0.0.1:1", ID: "id"},
		"orders": {Base: "http://127.0.0.1:2", ID: "_id"},
	})

	resp, err := http.Get(ts.URL + "/api/entities")
	if err != nil {
		t.Fatalf("GET /api/entities: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	var got struct {
		Entities []struct {
			Name string `json:"name"`
			ID   string `json:"id"`
		} `json:"entities"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, body)
	}
	if len(got.Entities) != 2 {
		t.Fatalf("entities = %+v, want 2", got.Entities)
	}
	// sortedKeys orders alphabetically: orders, users.
	if got.Entities[0].Name != "orders" || got.Entities[0].ID != "_id" {
		t.Errorf("entities[0] = %+v", got.Entities[0])
	}
	if got.Entities[1].Name != "users" || got.Entities[1].ID != "id" {
		t.Errorf("entities[1] = %+v", got.Entities[1])
	}
}

// TestEntitiesDiscoveryListIncludesLinks: an entity's configured links (label +
// raw template) ride along in the discovery list so the dashboard can render
// per-row "open in host app" buttons without a second round-trip — the
// template itself is resolved client-side against each row's own fields.
func TestEntitiesDiscoveryListIncludesLinks(t *testing.T) {
	ts := newEntitiesTestEnv(t, map[string]config.Entity{
		"gadgets": {
			Base: "http://127.0.0.1:1", ID: "gadget_id",
			Links: []config.EntityLink{
				{Label: "Open in admin-console", Template: "http://localhost:3000/modules?gadgetId={{gadget_id}}"},
				{Label: "Open in Acme Wallet (mobile)", Template: "acmewallet://card?token={{gadget_id}}"},
			},
		},
		"users": {Base: "http://127.0.0.1:2", ID: "id"}, // no links configured
	})

	resp, err := http.Get(ts.URL + "/api/entities")
	if err != nil {
		t.Fatalf("GET /api/entities: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var got struct {
		Entities []struct {
			Name  string `json:"name"`
			Links []struct {
				Label    string `json:"label"`
				Template string `json:"template"`
			} `json:"links"`
		} `json:"entities"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, body)
	}
	if len(got.Entities) != 2 {
		t.Fatalf("entities = %+v, want 2", got.Entities)
	}
	// sortedKeys orders alphabetically: gadgets, users.
	entity0 := got.Entities[0]
	if entity0.Name != "gadgets" || len(entity0.Links) != 2 {
		t.Fatalf("gadgets = %+v", entity0)
	}
	if entity0.Links[0].Label != "Open in admin-console" || entity0.Links[0].Template != "http://localhost:3000/modules?gadgetId={{gadget_id}}" {
		t.Errorf("links[0] = %+v", entity0.Links[0])
	}
	if entity0.Links[1].Label != "Open in Acme Wallet (mobile)" || entity0.Links[1].Template != "acmewallet://card?token={{gadget_id}}" {
		t.Errorf("links[1] = %+v", entity0.Links[1])
	}
	users := got.Entities[1]
	if users.Name != "users" || len(users.Links) != 0 {
		t.Errorf("users (no links configured) = %+v", users)
	}
}

// TestEntitiesDiscoveryListUrlLinkSerializesUnchanged guards that adding
// kind/argv to entityLink didn't change the wire shape of a "url" (or
// default) link — kind and argv must be entirely absent, not present with
// zero values, so the existing dashboard client's assumptions still hold.
func TestEntitiesDiscoveryListUrlLinkSerializesUnchanged(t *testing.T) {
	ts := newEntitiesTestEnv(t, map[string]config.Entity{
		"gadgets": {
			Base: "http://127.0.0.1:1", ID: "gadget_id",
			Links: []config.EntityLink{
				{Label: "Open in admin-console", Template: "http://localhost:3000/modules?gadgetId={{gadget_id}}"},
			},
		},
	})

	resp, err := http.Get(ts.URL + "/api/entities")
	if err != nil {
		t.Fatalf("GET /api/entities: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if strings.Contains(string(body), `"kind"`) || strings.Contains(string(body), `"argv"`) {
		t.Fatalf("url link response should omit kind/argv entirely, got: %s", body)
	}
}

// TestEntitiesDiscoveryListIncludesExecLinkArgv: a "kind: exec" link
// carries its expanded argv (from the closed command table) and kind, so
// the dashboard can build the copy-to-clipboard command without a second
// lookup or its own copy of the command table.
func TestEntitiesDiscoveryListIncludesExecLinkArgv(t *testing.T) {
	ts := newEntitiesTestEnv(t, map[string]config.Entity{
		"gadgets": {
			Base: "http://127.0.0.1:1", ID: "gadget_id",
			Links: []config.EntityLink{
				{Label: "Open on Android", Template: "myapp://widget/{{gadget_id}}", Kind: "exec", Exec: "adb-view"},
			},
		},
	})

	resp, err := http.Get(ts.URL + "/api/entities")
	if err != nil {
		t.Fatalf("GET /api/entities: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var got struct {
		Entities []struct {
			Links []struct {
				Label    string   `json:"label"`
				Template string   `json:"template"`
				Kind     string   `json:"kind"`
				Argv     []string `json:"argv"`
			} `json:"links"`
		} `json:"entities"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, body)
	}
	if len(got.Entities) != 1 || len(got.Entities[0].Links) != 1 {
		t.Fatalf("got = %+v", got)
	}
	link := got.Entities[0].Links[0]
	if link.Kind != "exec" {
		t.Errorf("kind = %q, want \"exec\"", link.Kind)
	}
	wantArgv := []string{"adb", "shell", "am", "start", "-a", "android.intent.action.VIEW", "-d", "{{url}}"}
	if len(link.Argv) != len(wantArgv) {
		t.Fatalf("argv = %v, want %v", link.Argv, wantArgv)
	}
	for i := range wantArgv {
		if link.Argv[i] != wantArgv[i] {
			t.Errorf("argv[%d] = %q, want %q", i, link.Argv[i], wantArgv[i])
		}
	}
	// The exec: config key itself is a server-side lookup key only — it
	// must never appear as a JSON key (the "exec" kind VALUE is expected
	// and checked above, so this only looks for the key form).
	if strings.Contains(string(body), `"exec":`) {
		t.Errorf("response must not expose the exec: config key, got: %s", body)
	}
}

// TestEntitiesDiscoveryDefaultsEmptyIDToId guards final-review-phase-3.md's I4:
// config.Validate requires entity.base but says nothing about entity.id, so
// `entities: { users: { base: "..." } }` is a valid config — forwarding its
// empty ID verbatim made EntityView's idField "", which made detail/edit/delete
// unreachable for every row and blamed the user's (valid) config. The server
// side of the fix defaults it here so every consumer of this endpoint (not
// just the dashboard) sees a usable id field name.
func TestEntitiesDiscoveryDefaultsEmptyIDToId(t *testing.T) {
	ts := newEntitiesTestEnv(t, map[string]config.Entity{
		"users": {Base: "http://127.0.0.1:1"}, // no ID configured
	})

	resp, err := http.Get(ts.URL + "/api/entities")
	if err != nil {
		t.Fatalf("GET /api/entities: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	var got struct {
		Entities []struct {
			Name string `json:"name"`
			ID   string `json:"id"`
		} `json:"entities"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, body)
	}
	if len(got.Entities) != 1 || got.Entities[0].ID != "id" {
		t.Fatalf("entities = %+v, want one entity with id defaulted to \"id\"", got.Entities)
	}
}

func TestEntityProxyGETPassesQueryThrough(t *testing.T) {
	upstream := echoUpstream(t)
	ts := newEntitiesTestEnv(t, map[string]config.Entity{
		"users": {Base: upstream.URL, ID: "id"},
	})

	resp, err := http.Get(ts.URL + "/api/entities/users/123?active=true")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	if resp.Header.Get("X-Upstream") != "yes" {
		t.Errorf("upstream response headers were not forwarded: %v", resp.Header)
	}
	var echoed struct{ Method, Path, Query, Body string }
	if err := json.Unmarshal(body, &echoed); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, body)
	}
	if echoed.Method != http.MethodGet {
		t.Errorf("method = %q, want GET", echoed.Method)
	}
	if echoed.Path != "/123" {
		t.Errorf("path = %q, want /123", echoed.Path)
	}
	if echoed.Query != "active=true" {
		t.Errorf("query = %q, want active=true", echoed.Query)
	}
}

func TestEntityProxyPOSTPassesBodyThrough(t *testing.T) {
	upstream := echoUpstream(t)
	ts := newEntitiesTestEnv(t, map[string]config.Entity{
		"users": {Base: upstream.URL, ID: "id"},
	})

	resp, err := http.Post(ts.URL+"/api/entities/users", "application/json", jsonReader(`{"name":"alice"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	var echoed struct{ Method, Path, Query, Body string }
	if err := json.Unmarshal(body, &echoed); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, body)
	}
	if echoed.Method != http.MethodPost {
		t.Errorf("method = %q, want POST", echoed.Method)
	}
	if echoed.Path != "/" {
		t.Errorf("path = %q, want /", echoed.Path)
	}
	if echoed.Body != `{"name":"alice"}` {
		t.Errorf("body = %q, want the posted JSON verbatim", echoed.Body)
	}
}

func TestEntityProxyUnknownEntityIs404(t *testing.T) {
	ts := newEntitiesTestEnv(t, map[string]config.Entity{
		"users": {Base: "http://127.0.0.1:1", ID: "id"},
	})

	resp, err := http.Get(ts.URL + "/api/entities/nope/1")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	var got map[string]string
	if err := json.Unmarshal(body, &got); err != nil || got["error"] == "" {
		t.Fatalf("expected {error:...} body, got %s", body)
	}
}

// jsonReader is a small helper so POST bodies read as clean literals above.
func jsonReader(s string) io.Reader { return strings.NewReader(s) }
