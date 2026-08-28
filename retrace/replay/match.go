package replay

import (
	"encoding/json"
	"net/url"
	"reflect"
	"sort"
	"strings"

	"github.com/caribou-crew/ensemble/retrace/rules"
)

// MissField is one reason a request did not match: the field that
// differed, what the recording expected there, and what actually arrived.
// Both sides are rendered strings rather than `any` because this crosses
// the wire in the 501 body and in misses.jsonl, where a reader wants to
// see the values, not re-decode them.
type MissField struct {
	Field    string `json:"field"`
	Expected string `json:"expected"`
	Actual   string `json:"actual"`
}

// Result is what Match decides. Miss and Hit are mutually exclusive by
// construction, and the ZERO Result is a miss with no explanation rather
// than a hit — a Result that some future path forgot to fill must never
// read as "the recording covers this call".
//
// Miss is a separate bool rather than "Hit == nil": a caller that reads
// only one of the two fields gets the refusing answer either way.
type Result struct {
	Hit     *Exchange   `json:"hit,omitempty"`
	Miss    bool        `json:"miss,omitempty"`
	Nearest *Exchange   `json:"nearest,omitempty"`
	Diff    []MissField `json:"diff,omitempty"`
}

// Request is one incoming call, already split into the parts that
// identify it. Body is the DECODED request body (nil when there was none
// or it was not JSON); Raw is the same body's bytes VERBATIM, which is
// what a recorded body this package could not parse is compared against.
//
// Raw's zero value is the refusing one: a caller that forgets to set it
// cannot match a recording whose request body was opaque, it can only
// miss.
type Request struct {
	Method, Path, Query string
	Body                any
	Raw                 string
}

// Options is everything that can loosen matching, and every field's zero
// value is the STRICT one: no rules, no normalization, no ignored query
// params. A caller that forgets to configure this gets the strictest
// possible mock, never the most permissive.
type Options struct {
	Rules       []rules.Rule
	Normalize   func(string) string
	QueryIgnore []string
	// TargetFilter restricts matching to exchanges recorded through one
	// named listener (Exchange.Target). Empty means no filter — every
	// exchange is eligible regardless of Target, which is today's exact
	// behavior for every bundle recorded before multi-listener existed
	// (and for a single-listener bundle recorded after). It exists so a
	// multi-listener replay can bind one Server per listener without
	// either ever answering from the other's recorded traffic: a request
	// that matches by method+path+query+body but was recorded through a
	// DIFFERENT target is not this server's to serve, so it is reported as
	// an ordinary miss rather than a cross-listener hit.
	TargetFilter string
	// No MissPath here. The misses file has ONE name and ONE owner:
	// runs.Paths.MissesPath. A second field naming the same file is a
	// second thing to keep in sync, and the loser of that race writes
	// misses nobody reads. NewServer takes the path explicitly.
}

// NormalizePath strips one trailing slash so "/cart/" and "/cart" are the
// same route, and maps the empty path to "/".
func NormalizePath(p string) string {
	if p == "" {
		return "/"
	}
	if len(p) > 1 && strings.HasSuffix(p, "/") {
		return p[:len(p)-1]
	}
	return p
}

// SignificantQuery canonicalises a query string: fixture-declared
// insignificant keys dropped, the rest sorted, so a query that only
// differs in key order — or only adds an ignored param — is the same
// request.
//
// A query that will not parse is returned VERBATIM rather than silently
// canonicalised into something else: two identical unparseable queries
// still match each other, and two different ones still differ, which is
// the fail-closed reading.
func SignificantQuery(query string, ignore []string) string {
	if query == "" {
		return ""
	}
	vals, err := url.ParseQuery(query)
	if err != nil {
		return query
	}
	for _, k := range ignore {
		vals.Del(k)
	}
	// url.Values.Encode sorts by key; values within one key keep their
	// order, which is real information (?tag=a&tag=b is not ?tag=b&tag=a).
	return vals.Encode()
}

// Match answers a request from the bundle's exchange table, in the prototype's
// order — method+path, then query, then request body — with two additions
// the Go side needs: body equivalence is decided by the SAME wire rules
// the diff uses (so a fresh uuid under a uuid rule is not a deviation),
// and the `used` counter gives repeated identical calls their recorded
// order.
//
// It NEVER returns a "close enough" hit. Every path out of here is either
// an exchange that genuinely matched or a miss carrying the nearest
// candidate and the fields that differed, for a human to read off a 501.
//
// Match MUTATES the bundle (the `used` counter) and is therefore not safe
// for concurrent use; Server serialises it behind a mutex.
func (b *Bundle) Match(r Request, o Options) Result {
	method := strings.ToUpper(strings.TrimSpace(r.Method))
	path := normalizeWith(o, r.Path)
	wantQuery := SignificantQuery(r.Query, o.QueryIgnore)

	var byMethodAndPath []int
	for i := range b.Exchanges {
		e := &b.Exchanges[i]
		if o.TargetFilter != "" && e.Target != o.TargetFilter {
			continue
		}
		if strings.ToUpper(e.Key.Method) == method && normalizeWith(o, e.Key.Path) == path {
			byMethodAndPath = append(byMethodAndPath, i)
		}
	}
	if len(byMethodAndPath) == 0 {
		near := b.nearest(method, path, o)
		res := Result{Miss: true, Nearest: near}
		if near != nil {
			// The METHOD row only when the method is actually part of the
			// difference. `GET /admin/purge` against a nearest `GET /cart`
			// printing "method: expected GET, got GET" is a reported
			// difference that is not one, in the body a human reads to act
			// on.
			if !strings.EqualFold(near.Key.Method, method) {
				res.Diff = append(res.Diff, MissField{Field: "method", Expected: near.Key.Method, Actual: r.Method})
			}
			res.Diff = append(res.Diff, MissField{Field: "path", Expected: near.Key.Path, Actual: r.Path})
		}
		return res
	}

	var byQuery []int
	for _, i := range byMethodAndPath {
		if SignificantQuery(b.Exchanges[i].Key.Query, o.QueryIgnore) == wantQuery {
			byQuery = append(byQuery, i)
		}
	}
	if len(byQuery) == 0 {
		near := &b.Exchanges[byMethodAndPath[0]]
		return Result{Miss: true, Nearest: near, Diff: []MissField{{
			Field:    "query",
			Expected: SignificantQuery(near.Key.Query, o.QueryIgnore),
			Actual:   wantQuery,
		}}}
	}

	res := rules.Resolve(o.Rules, method, path)
	var candidates []int
	for _, i := range byQuery {
		if len(requestBodyDiff(&b.Exchanges[i], r, res)) == 0 {
			candidates = append(candidates, i)
		}
	}
	if len(candidates) == 0 {
		near := &b.Exchanges[byQuery[0]]
		return Result{Miss: true, Nearest: near, Diff: requestBodyDiff(near, r, res)}
	}

	// Recorded order for repeats: the first candidate that has not been
	// served yet, else the LAST one. Serving the first one again would
	// hang a poll-until-ready flow forever; missing instead would report a
	// retry loop's extra attempt as a client deviation, which it is not.
	chosen := candidates[len(candidates)-1]
	for _, i := range candidates {
		if b.Exchanges[i].used == 0 {
			chosen = i
			break
		}
	}
	b.Exchanges[chosen].used++
	return Result{Hit: &b.Exchanges[chosen]}
}

// requestBodyDiff decides whether one recorded request body admits this
// request's body.
//
// A recorded body the loader could not parse as JSON — a form post, XML,
// a protobuf, plain text — is compared BYTE FOR BYTE against the bytes
// that arrived. It is emphatically not "no constraint": that reading made
// a recorded `POST /login` with a form-encoded body match ANY body on the
// same method+path+query, which is the wildcard this package exists to
// refuse (F3). Byte-exact is the useful of the two honest answers,
// because a non-JSON body genuinely has no structure this package can
// subset-match — and truncated bodies, the one case where byte-exact
// would reject correct clients, never reach here: LoadBundle refuses the
// bundle.
//
// A recorded literal `null` also lands here (it decodes to a nil `any`
// while its bytes are "null"), and is therefore held to those four bytes.
// That is stricter than the old reading and deliberately so: fail-closed
// is the tie-breaker whenever a value could mean either "nothing was
// recorded" or "nothing is required".
func requestBodyDiff(e *Exchange, r Request, res rules.Resolved) []MissField {
	if e.ReqBody == nil && e.ReqRaw != "" {
		if r.Raw == e.ReqRaw {
			return nil
		}
		actual := r.Raw
		if actual == "" {
			actual = absent
		}
		return []MissField{{Field: "body", Expected: e.ReqRaw, Actual: actual}}
	}
	return bodyDiff(e.ReqBody, r.Body, "", res)
}

func normalizeWith(o Options, p string) string {
	if o.Normalize != nil {
		p = o.Normalize(p)
	}
	return NormalizePath(p)
}

// nearest is the single closest exchange by path (then method), for the
// miss body's "nearest candidate" — a human debugging a 501 needs
// something concrete to compare against, not just "no match". Ties keep
// the earliest recorded exchange, so the answer is deterministic.
func (b *Bundle) nearest(method, path string, o Options) *Exchange {
	best, bestScore := -1, 0
	for i := range b.Exchanges {
		if o.TargetFilter != "" && b.Exchanges[i].Target != o.TargetFilter {
			continue
		}
		score := levenshtein(normalizeWith(o, b.Exchanges[i].Key.Path), path)
		if strings.ToUpper(b.Exchanges[i].Key.Method) != method {
			score += 100
		}
		if best == -1 || score < bestScore {
			best, bestScore = i, score
		}
	}
	if best == -1 {
		return nil
	}
	return &b.Exchanges[best]
}

func levenshtein(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			if a[i-1] == b[j-1] {
				cur[j] = prev[j-1]
				continue
			}
			cur[j] = 1 + min3(prev[j], cur[j-1], prev[j-1])
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

func min3(a, b, c int) int {
	if b < a {
		a = b
	}
	if c < a {
		a = c
	}
	return a
}

// bodyDiff reports every key the RECORDING declared that the incoming
// body does not satisfy. The incoming body may carry additional keys the
// recording never saw — a client that sends more is not deviating from
// what was recorded — but a key present on both sides with a different
// value is a real conflict.
//
// Rules decide equivalence, not byte equality: the matcher resolved for
// this call is consulted per field, so an `ignore` or a satisfied value
// matcher (uuid, iso-8601, …) silences a difference exactly as it does in
// the wire diff. rules.Classify's zero Matcher — "no rule applies" —
// classifies as Changed, so an unruled field is always compared.
//
// A nil recorded body reaches here only when the recording carried NO
// request body at all — requestBodyDiff routes a body that was present
// but unparseable to a verbatim comparison before this is called — so
// there is genuinely no declared shape to hold the client to.
func bodyDiff(fixture, request any, path string, res rules.Resolved) []MissField {
	if fixture == nil {
		return nil
	}
	fixObj, isObj := fixture.(map[string]any)
	if !isObj {
		if equalJSON(fixture, request) {
			return nil
		}
		if silenced(res, path, fixture, request, request != nil) {
			return nil
		}
		return []MissField{{Field: fieldName(path), Expected: render(fixture), Actual: render(request)}}
	}
	reqObj, reqIsObj := request.(map[string]any)
	var out []MissField
	for _, k := range sortedKeys(fixObj) {
		field := joinField(path, k)
		expected := fixObj[k]
		if !reqIsObj {
			if !silenced(res, field, expected, nil, false) {
				out = append(out, MissField{Field: fieldName(field), Expected: render(expected), Actual: absent})
			}
			continue
		}
		actual, present := reqObj[k]
		if !present {
			if !silenced(res, field, expected, nil, false) {
				out = append(out, MissField{Field: fieldName(field), Expected: render(expected), Actual: absent})
			}
			continue
		}
		out = append(out, bodyDiff(expected, actual, field, res)...)
	}
	return out
}

// absent is how a key the request never sent is rendered. It is
// deliberately not "" or "null", both of which are values a request could
// legitimately carry — "the field was empty" and "the field was not there"
// are different facts and must not print the same.
const absent = "(absent)"

// silenced asks the wire rules whether a difference at this field path is
// one the operator already declared irrelevant. Only Ignored and Tolerated
// silence; Changed and Violation do not — a broken matcher (Violation)
// must never excuse a difference.
func silenced(res rules.Resolved, path string, a, b any, bothPresent bool) bool {
	switch rules.Classify(res.ForField(path), a, b, bothPresent) {
	case rules.Ignored, rules.Tolerated:
		return true
	default:
		return false
	}
}

// fieldName is what a MissField calls a body field. The rule lookup uses
// the BARE field path ("items[0].sku") — the same dialect wire_rules globs
// are written in, so one glob means one thing in both the diff and the
// replay matcher — and only the report substitutes "body" for the root, so
// a whole-body mismatch has a name to print.
func fieldName(path string) string {
	if path == "" {
		return "body"
	}
	return path
}

func joinField(parent, key string) string {
	if parent == "" {
		return key
	}
	return parent + "." + key
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func equalJSON(a, b any) bool { return reflect.DeepEqual(a, b) }

func render(v any) string {
	if v == nil {
		return "null"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return absent
	}
	return string(b)
}
