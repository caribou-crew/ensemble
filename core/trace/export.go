package trace

// Export formats: HAR 1.2, curl, raw request/response. Ported from the
// local-stack prototype (web/src/trace/export.ts). Enough of HAR 1.2 to
// import cleanly into Charles / Chrome devtools: fields the spec marks
// required are always present; unmeasured numbers are -1 ("not applicable"),
// never 0 (a lie).

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
)

type HarNameValue struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type HarPostData struct {
	MimeType string `json:"mimeType"`
	Text     string `json:"text"`
}

type HarContent struct {
	Size     int    `json:"size"`
	MimeType string `json:"mimeType"`
	Text     string `json:"text,omitempty"`
}

type HarTimings struct {
	Send    float64 `json:"send"`
	Wait    float64 `json:"wait"`
	Receive float64 `json:"receive"`
}

type HarRequest struct {
	Method      string         `json:"method"`
	URL         string         `json:"url"`
	HTTPVersion string         `json:"httpVersion"`
	Headers     []HarNameValue `json:"headers"`
	QueryString []HarNameValue `json:"queryString"`
	Cookies     []HarNameValue `json:"cookies"`
	HeadersSize int            `json:"headersSize"`
	BodySize    int            `json:"bodySize"`
	PostData    *HarPostData   `json:"postData,omitempty"`
}

type HarResponse struct {
	Status      int            `json:"status"`
	StatusText  string         `json:"statusText"`
	HTTPVersion string         `json:"httpVersion"`
	Headers     []HarNameValue `json:"headers"`
	Cookies     []HarNameValue `json:"cookies"`
	Content     HarContent     `json:"content"`
	RedirectURL string         `json:"redirectURL"`
	HeadersSize int            `json:"headersSize"`
	BodySize    int            `json:"bodySize"`
}

type HarEntry struct {
	StartedDateTime string      `json:"startedDateTime"`
	Time            float64     `json:"time"`
	Request         HarRequest  `json:"request"`
	Response        HarResponse `json:"response"`
	Cache           struct{}    `json:"cache"`
	Timings         HarTimings  `json:"timings"`
}

type HarCreator struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type HarLog struct {
	Version string     `json:"version"`
	Creator HarCreator `json:"creator"`
	Entries []HarEntry `json:"entries"`
}

type Har struct {
	Log HarLog `json:"log"`
}

const httpVersion = "HTTP/1.1"

var reasonPhrase = map[int]string{
	200: "OK", 201: "Created", 202: "Accepted", 204: "No Content",
	301: "Moved Permanently", 302: "Found", 304: "Not Modified",
	400: "Bad Request", 401: "Unauthorized", 403: "Forbidden",
	404: "Not Found", 409: "Conflict", 422: "Unprocessable Entity",
	429: "Too Many Requests", 500: "Internal Server Error",
	502: "Bad Gateway", 503: "Service Unavailable", 504: "Gateway Timeout",
}

func headerLookup(headers map[string]string, name string) string {
	for k, v := range headers {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}

// HopURL builds a replayable URL. `To` is a logical service name and is not
// resolvable, so the captured Host header is the authority when present.
func HopURL(h Hop) string {
	authority := headerLookup(h.Req.Headers, "host")
	if authority == "" {
		authority = h.To
	}
	return "http://" + authority + h.Path
}

// shellQuote is POSIX single-quoting: end the quote, escape a literal
// quote, reopen.
func shellQuote(v string) string {
	return "'" + strings.ReplaceAll(v, "'", `'\''`) + "'"
}

// sortedHeaders returns name/value pairs sorted by name — Go maps are
// unordered, and deterministic output keeps exports diffable.
func sortedHeaders(headers map[string]string) []HarNameValue {
	out := make([]HarNameValue, 0, len(headers))
	for k, v := range headers {
		out = append(out, HarNameValue{Name: k, Value: v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func ToCurl(h Hop) string {
	parts := []string{"curl"}
	method := strings.ToUpper(h.Method)
	if method != "GET" {
		parts = append(parts, "-X", method)
	}
	parts = append(parts, shellQuote(HopURL(h)))
	for _, nv := range sortedHeaders(h.Req.Headers) {
		parts = append(parts, "-H", shellQuote(nv.Name+": "+nv.Value))
	}
	if h.Req.Body != "" {
		parts = append(parts, "--data-raw", shellQuote(h.Req.Body))
	}
	return strings.Join(parts, " ")
}

// rawMessage is newline-separated rather than CRLF: this is written to a
// clipboard for a human to read and paste, and stray ^M helps nobody.
func rawMessage(startLine string, headers map[string]string, body string) string {
	lines := []string{startLine}
	for _, nv := range sortedHeaders(headers) {
		lines = append(lines, nv.Name+": "+nv.Value)
	}
	if body != "" {
		lines = append(lines, "", body)
	}
	return strings.Join(lines, "\n")
}

func ToRawRequest(h Hop) string {
	return rawMessage(strings.ToUpper(h.Method)+" "+h.Path+" "+httpVersion, h.Req.Headers, h.Req.Body)
}

func ToRawResponse(h Hop) string {
	statusLine := fmt.Sprintf("%s %d", httpVersion, h.Status)
	if reason := reasonPhrase[h.Status]; reason != "" {
		statusLine += " " + reason
	}
	return rawMessage(statusLine, h.Resp.Headers, h.Resp.Body)
}

func queryPairs(path string) []HarNameValue {
	_, raw, _ := strings.Cut(path, "?")
	if raw == "" {
		return []HarNameValue{}
	}
	out := []HarNameValue{}
	for _, part := range strings.Split(raw, "&") {
		if part == "" {
			continue
		}
		name, value, _ := strings.Cut(part, "=")
		if dn, err := url.QueryUnescape(name); err == nil {
			name = dn
		}
		if dv, err := url.QueryUnescape(value); err == nil {
			value = dv
		}
		out = append(out, HarNameValue{Name: name, Value: value})
	}
	return out
}

func mimeType(headers map[string]string) string {
	if ct := headerLookup(headers, "content-type"); ct != "" {
		return ct
	}
	return "application/json"
}

func toEntry(h Hop) HarEntry {
	entry := HarEntry{
		StartedDateTime: h.T.Start.UTC().Format("2006-01-02T15:04:05.000Z07:00"),
		Time:            h.T.DoneMs,
		Request: HarRequest{
			Method:      strings.ToUpper(h.Method),
			URL:         HopURL(h),
			HTTPVersion: httpVersion,
			Headers:     sortedHeaders(h.Req.Headers),
			QueryString: queryPairs(h.Path),
			Cookies:     []HarNameValue{},
			HeadersSize: -1,
			BodySize:    -1,
		},
		Response: HarResponse{
			Status:      h.Status,
			StatusText:  reasonPhrase[h.Status],
			HTTPVersion: httpVersion,
			Headers:     sortedHeaders(h.Resp.Headers),
			Cookies:     []HarNameValue{},
			Content:     HarContent{Size: len(h.Resp.Body), MimeType: mimeType(h.Resp.Headers), Text: h.Resp.Body},
			HeadersSize: -1,
			BodySize:    -1,
		},
		// Unlike the prototype, the proxy measures first-byte: when known,
		// wait is time-to-first-byte and receive is the remainder. Otherwise
		// the total goes to wait and unmeasured legs are the spec's -1.
		Timings: HarTimings{Send: -1, Wait: h.T.DoneMs, Receive: -1},
	}
	if h.T.FirstByteMs > 0 {
		entry.Timings.Wait = h.T.FirstByteMs
		entry.Timings.Receive = h.T.DoneMs - h.T.FirstByteMs
	}
	if h.Req.Body != "" {
		entry.Request.BodySize = len(h.Req.Body)
		entry.Request.PostData = &HarPostData{MimeType: mimeType(h.Req.Headers), Text: h.Req.Body}
	}
	if h.Resp.Body != "" {
		entry.Response.BodySize = len(h.Resp.Body)
	}
	return entry
}

func ToHar(hops []Hop) Har {
	entries := make([]HarEntry, 0, len(hops))
	for _, h := range hops {
		entries = append(entries, toEntry(h))
	}
	return Har{Log: HarLog{
		Version: "1.2",
		Creator: HarCreator{Name: "ensemble", Version: "1"},
		Entries: entries,
	}}
}
