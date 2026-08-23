package inspector

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// HTTPDriver is the Driver implementation for a service that exposes its
// own state (an in-memory store, a SQLite file, a wrapped third-party API —
// anything with no real database socket to point postgres/mysql/dynamodb
// at) over the three-route HTTP contract documented in the
// inspector-http-driver design doc.
type HTTPDriver struct {
	baseURL string
	headers map[string]string
	client  *http.Client
}

// NewHTTPDriver returns a driver against baseURL (e.g.
// "http://127.0.0.1:4281/ensemble-inspect"), sending headers on every
// request. Like the dynamo driver, this is a plain http.Client wrapper —
// nothing is dialed up front, so it's safe to construct before the backing
// service is healthy.
func NewHTTPDriver(baseURL string, headers map[string]string) *HTTPDriver {
	return &HTTPDriver{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		headers: headers,
		client:  &http.Client{},
	}
}

// errNotFound reports table as unrecognized by the backing service (its
// route returned 404), mirroring the "not found" shape Inspector.Rows and
// Inspector.Schema already return for an unregistered database.
type errNotFound struct{ table string }

func (e errNotFound) Error() string {
	return fmt.Sprintf("inspector: http: table %q not found", e.table)
}

// get issues one GET request against d.baseURL+path (query already encoded
// into path) and decodes the JSON response body into out. A 404 response
// becomes errNotFound{table}; any other non-2xx becomes a plain error.
func (d *HTTPDriver) get(ctx context.Context, path, table string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("inspector: http: build request: %w", err)
	}
	for k, v := range d.headers {
		req.Header.Set(k, v)
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("inspector: http: %s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return errNotFound{table: table}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("inspector: http: %s: status %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("inspector: http: %s: decode response: %w", path, err)
	}
	return nil
}

// Tables lists every table/collection the backing service reports via
// GET {baseURL}/tables.
func (d *HTTPDriver) Tables(ctx context.Context) ([]Table, error) {
	var out struct {
		Tables []Table `json:"tables"`
	}
	if err := d.get(ctx, "/tables", "", &out); err != nil {
		return nil, err
	}
	return out.Tables, nil
}

// Rows returns up to limit rows of table, skipping offset, via
// GET {baseURL}/rows?table=&limit=&offset=.
func (d *HTTPDriver) Rows(ctx context.Context, table string, limit, offset int) ([]map[string]any, error) {
	q := url.Values{
		"table":  {table},
		"limit":  {strconv.Itoa(limit)},
		"offset": {strconv.Itoa(offset)},
	}
	var out struct {
		Rows []map[string]any `json:"rows"`
	}
	if err := d.get(ctx, "/rows?"+q.Encode(), table, &out); err != nil {
		return nil, err
	}
	return out.Rows, nil
}

// Fingerprint returns table's opaque change token via
// GET {baseURL}/fingerprint?table=.
func (d *HTTPDriver) Fingerprint(ctx context.Context, table string) (string, error) {
	q := url.Values{"table": {table}}
	var out struct {
		Fingerprint string `json:"fingerprint"`
	}
	if err := d.get(ctx, "/fingerprint?"+q.Encode(), table, &out); err != nil {
		return "", err
	}
	return out.Fingerprint, nil
}
