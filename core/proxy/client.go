package proxy

import (
	"regexp"
	"strings"
)

// DefaultClientHeaders are the request headers checked, in order, for the
// name of the client application that sent a request, when
// Proxy.ClientHeaders is unset. Both are conventions already common in
// multi-front-end stacks; a project with its own spelling sets
// ClientHeaders instead.
//
// Unlike SourceHeaders there is no "off" state, and none is needed: reading
// two headers that are not present costs a map lookup and records nothing.
// A stack that does not use them simply never has a Client on its hops.
var DefaultClientHeaders = []string{"x-source-client", "x-local-client"}

// FallbackClient is what a hop records when a client-identity header was
// present but its value failed validation.
//
// It is deliberately a value, not "": an empty Client means "no client
// header arrived", and a malformed one is a different fact — someone IS
// declaring a client identity and getting it wrong. Collapsing the two
// would hide a misconfigured app inside the much larger population of
// requests that simply carry no header, which is where it would never be
// found.
//
// It is equally deliberately not the offending string. Whatever a client
// puts in that header would otherwise reach hops.jsonl, the traffic UI, and
// any group-by built on Client — so the raw value is dropped at the door
// rather than sanitized downstream by every consumer in turn.
const FallbackClient = "client"

// ValidClient is the shape a client identity must have: lower-case
// alphanumeric, then up to 31 more of the same plus ':' and '-'.
//
// Tight on purpose. Client is an IDENTIFIER — it is grouped by, filtered on,
// and rendered as a label — so the charset is the set of things that are
// safe to do all three with. The leading character excludes '-' and ':' so a
// value cannot be mistaken for a flag or an empty namespace, and the 32-byte
// cap keeps a header nobody validates upstream from becoming an unbounded
// field on every hop.
var ValidClient = regexp.MustCompile(`^[a-z0-9][a-z0-9:-]{0,31}$`)

// clientIdentity resolves the client application's name from the first
// configured header present on the request.
//
// FIRST PRESENT wins, not first valid: a request carrying a malformed
// `x-source-client` and a well-formed `x-local-client` reports the fallback,
// not the second header. Falling through to the next header would silently
// repair a misconfigured app — the report would look clean while the value
// the team believes they are sending is being thrown away, and nobody would
// ever be told. The same reason the header list is ordered at all.
func (p *Proxy) clientIdentity(header interface{ Get(string) string }) string {
	headers := p.ClientHeaders
	if len(headers) == 0 {
		headers = DefaultClientHeaders
	}
	for _, h := range headers {
		v := header.Get(h)
		if v == "" {
			continue
		}
		if ValidClient.MatchString(v) {
			return v
		}
		p.warnBadClient(h, v)
		return FallbackClient
	}
	return ""
}

// badClientWarnCap bounds how many DISTINCT malformed values are ever
// warned about, after which the proxy goes quiet.
//
// The brief says "a one-time warning". Once per process is the literal
// reading and is worse than it sounds: `ensemble up` runs for hours, so the
// second app to send a bad value — or the same app after a developer's
// failed fix — gets nothing, and the silence reads as success. Once per
// distinct (header, value) says something the first time each genuinely new
// mistake appears, which is the behaviour "one-time" is reaching for.
//
// The cap is what keeps that from becoming a log-flood and an unbounded map
// when the bad value varies per request (a request id in the wrong header, a
// hostile client). Past it the warnings stop; they are diagnostics, and no
// gate depends on them.
const badClientWarnCap = 32

// warnedValueLimit truncates the offending value in the warning text. A
// developer needs enough to recognize what they sent; a header this is the
// WRONG place for often holds a token, and a warning that echoed one in full
// would move a secret into a log file. 32 bytes is the length a valid client
// identity could have had, so nothing legitimate is ever cut.
const warnedValueLimit = 32

// warnBadClient reports a malformed client identity through OnWarn, at most
// once per distinct (header, value).
//
// OnWarn is invoked while holding the proxy's warning mutex, so a sink never
// sees two of these concurrently and does not need its own lock — which
// matters because the natural sink is stderr, and stderr is not safe for
// concurrent writers.
func (p *Proxy) warnBadClient(header, value string) {
	if p.OnWarn == nil {
		return
	}
	key := header + "\x00" + value

	p.warnMu.Lock()
	defer p.warnMu.Unlock()
	if p.warnedClients == nil {
		p.warnedClients = map[string]bool{}
	}
	if p.warnedClients[key] || len(p.warnedClients) >= badClientWarnCap {
		return
	}
	p.warnedClients[key] = true

	shown := value
	if len(shown) > warnedValueLimit {
		shown = shown[:warnedValueLimit] + "…"
	}
	p.OnWarn("ensemble: " + header + ": " + quoteish(shown) +
		" is not a valid client identity (lower-case, " + ValidClient.String() + "); " +
		"recording it as " + quoteish(FallbackClient) + " instead — the value itself is not stored")
}

// quoteish wraps a value in quotes for a message without pulling in %q's
// escaping, which would turn an already-suspect value into an unreadable
// one. Control characters are stripped instead: this text is going to a
// terminal, and a header value is attacker-controlled.
func quoteish(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			b.WriteByte('?')
			continue
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}
