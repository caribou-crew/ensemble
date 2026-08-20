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
