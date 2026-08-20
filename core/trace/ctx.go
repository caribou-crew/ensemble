package trace

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// Well-known baggage keys. correlationId is the human-facing join key
// (pairs with the W3C traceId); encore-run partitions hops into recording
// sessions.
const (
	BaggageCorrelationID = "correlationId"
	BaggageSession       = "encore-run"
)

var traceparentPattern = regexp.MustCompile(`^00-([0-9a-f]{32})-([0-9a-f]{16})-([0-9a-f]{2})$`)

// Ctx is parsed W3C trace context plus baggage for one request.
type Ctx struct {
	TraceID      string
	SpanID       string
	ParentSpanID string
	Flags        string
	Baggage      map[string]string
}

// ParseCtx parses traceparent and baggage headers. Invalid or absent
// traceparent yields a fresh root context (new trace id and span id) —
// the proxy stamps context at the first hop that lacks it.
func ParseCtx(traceparent, baggage string) Ctx {
	ctx := Ctx{Flags: "01", Baggage: parseBaggage(baggage)}
	if m := traceparentPattern.FindStringSubmatch(traceparent); m != nil &&
		m[1] != strings.Repeat("0", 32) && m[2] != strings.Repeat("0", 16) {
		ctx.TraceID, ctx.SpanID, ctx.Flags = m[1], m[2], m[3]
		return ctx
	}
	ctx.TraceID = randHex(16)
	ctx.SpanID = randHex(8)
	return ctx
}

// NewCtx returns a fresh root context.
func NewCtx() Ctx { return ParseCtx("", "") }

// Child returns a context for the next hop: same trace, new span,
// parent linkage to this span. Baggage is copied, not shared.
func (c Ctx) Child() Ctx {
	bag := make(map[string]string, len(c.Baggage))
	for k, v := range c.Baggage {
		bag[k] = v
	}
	return Ctx{
		TraceID:      c.TraceID,
		SpanID:       randHex(8),
		ParentSpanID: c.SpanID,
		Flags:        c.Flags,
		Baggage:      bag,
	}
}

// Traceparent renders the W3C traceparent header value.
func (c Ctx) Traceparent() string {
	return fmt.Sprintf("00-%s-%s-%s", c.TraceID, c.SpanID, c.Flags)
}

// BaggageHeader renders the W3C baggage header value (sorted for stability).
func (c Ctx) BaggageHeader() string {
	if len(c.Baggage) == 0 {
		return ""
	}
	keys := make([]string, 0, len(c.Baggage))
	for k := range c.Baggage {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+url.QueryEscape(c.Baggage[k]))
	}
	return strings.Join(parts, ",")
}

// CorrelationID returns the correlation join key, if present.
func (c Ctx) CorrelationID() string { return c.Baggage[BaggageCorrelationID] }

// Session returns the encore-run session id; "" means ambient traffic.
func (c Ctx) Session() string { return c.Baggage[BaggageSession] }

// EnsureCorrelationID returns the existing correlation id or mints and
// stores one.
func (c Ctx) EnsureCorrelationID() string {
	if id := c.Baggage[BaggageCorrelationID]; id != "" {
		return id
	}
	id := randHex(8)
	c.Baggage[BaggageCorrelationID] = id
	return id
}

func parseBaggage(s string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.Split(s, ",") {
		// Baggage members may carry ;-separated properties; we keep only
		// the key=value head.
		head, _, _ := strings.Cut(strings.TrimSpace(part), ";")
		k, v, ok := strings.Cut(head, "=")
		if !ok || k == "" {
			continue
		}
		if dv, err := url.QueryUnescape(strings.TrimSpace(v)); err == nil {
			v = dv
		}
		out[strings.TrimSpace(k)] = v
	}
	return out
}

func randHex(nBytes int) string {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand failure is unrecoverable
	}
	return hex.EncodeToString(b)
}
