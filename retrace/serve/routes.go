package serve

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/caribou-crew/ensemble/retrace/config"
	"github.com/caribou-crew/ensemble/retrace/diff"
	"github.com/caribou-crew/ensemble/retrace/diff/pixel"
	"github.com/caribou-crew/ensemble/retrace/refs"
	"github.com/caribou-crew/ensemble/retrace/rules"
	"github.com/caribou-crew/ensemble/retrace/runs"
	"github.com/caribou-crew/ensemble/retrace/serve/ui"
)

// routes registers the REST surface, and it is the INVENTORY the route test
// walks (routes_test.go parses this function rather than listing patterns by
// hand, so a route added later is covered the day it appears).
//
// Every pattern names its METHOD. That is not style: a method-less pattern
// PANICS at registration the moment it conflicts with another, and it also
// makes "registered for POST" and "registered for anything" the same code.
//
// The UI is deliberately NOT registered here as "GET /". A catch-all
// pattern in this mux matches every API path no other pattern matched,
// which turns a wrong-method call — GET on a POST-only verb — into a 200
// carrying the app shell, instead of ServeMux's own 405. That is worse than
// it sounds for an API-first surface: an agent doing GET .../accept would
// read HTML as success. New dispatches /api/* to this mux and everything
// else to the UI, so ServeMux keeps answering 405/404 for the API while the
// UI still owns every other path. Task 15 fills in handleUI's body; it must
// not move it back into this mux.
func (s *server) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/queue", s.handleQueue)
	mux.HandleFunc("GET /api/queue/{app}/{flow}", s.handleItem)
	mux.HandleFunc("POST /api/queue/{app}/{flow}/accept", s.handleAccept)
	mux.HandleFunc("POST /api/queue/{app}/{flow}/reject", s.handleReject)
	mux.HandleFunc("POST /api/queue/{app}/{flow}/rule", s.handleRule)
	mux.HandleFunc("POST /api/queue/{app}/{flow}/redact", s.handleRedact)
	mux.HandleFunc("GET /api/shots/{app}/{flow}/{side}/{name}", s.handleShot)
	mux.HandleFunc("GET /api/evidence/{app}/{flow}", s.handleEvidence)
	mux.HandleFunc("GET /api/videos/{app}/{flow}/{name}", s.handleVideo)
	mux.HandleFunc("GET /api/report/{app}/{flow}", s.handleReport)
	mux.HandleFunc("GET /api/report/{app}/{flow}/{path...}", s.handleReport)
	mux.HandleFunc("GET /api/sync/candidates", s.handleSyncCandidates)
	mux.HandleFunc("POST /api/sync", s.handleSync)
}

// --- health -------------------------------------------------------------

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	d := s.deps()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"version": d.Version,
		"time":    d.now().UTC().Format("2006-01-02T15:04:05Z07:00"),
	})
}

// --- queue --------------------------------------------------------------

func (s *server) handleQueue(w http.ResponseWriter, r *http.Request) {
	WriteQueue(w, s.deps())
}

// WriteQueue writes the review queue as this handler's response would,
// exported so a second HTTP surface (ensemble/server's retrace routes) can
// serve the identical response without a second implementation of "what a
// queue response looks like".
func WriteQueue(w http.ResponseWriter, d Deps) {
	items, err := BuildQueue(d)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// items is never nil (BuildQueue starts from an empty slice), so this
	// encodes as [] and never as null: a client that renders null as "no
	// data yet" and [] as "nothing to review" must not have to guess which
	// one an empty queue is.
	//
	// "empty" is the other half of the same question, and it is R-O's: a
	// queue with nothing to act on says WHICH of its two causes it is.
	// "no-runs" is a setup step nobody has done; "all-clear" is the
	// reassuring one, and "" — the zero value — promises nothing. See
	// EmptyReasonFor.
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "empty": EmptyReasonFor(items)})
}

func (s *server) handleItem(w http.ResponseWriter, r *http.Request) {
	d, app, flow, ok := s.flowFrom(w, r)
	if !ok {
		return
	}
	WriteItem(w, d, app, flow)
}

// WriteItem writes one flow's diff summary, exported for the same reason
// WriteQueue is: ensemble/server's GET /api/retrace/queue/{app}/{flow}
// calls this directly rather than re-deriving what a 200/409 body looks
// like.
func WriteItem(w http.ResponseWriter, d Deps, app, flow string) {
	sum, err := SummaryFor(d, app, flow)
	if err != nil {
		writeErr(w, statusForSummaryErr(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"summary": sum})
}

// flowFrom resolves and validates the {app}/{flow} pair every non-health
// route carries, writing the refusal itself when it cannot. It is a thin
// HTTP wrapper over ResolveFlow — see that function for the three-way
// status split.
func (s *server) flowFrom(w http.ResponseWriter, r *http.Request) (Deps, string, string, bool) {
	d := s.deps()
	app, flow := r.PathValue("app"), r.PathValue("flow")
	status, msg, ok := ResolveFlow(d, app, flow)
	if !ok {
		writeErr(w, status, msg)
		return d, "", "", false
	}
	return d, app, flow, true
}

// ResolveFlow validates an {app}/{flow} pair and reports whether it names
// something that exists at all, independent of any http.Request — exported
// so a second HTTP surface (ensemble/server's retrace routes) reaches the
// exact same three-way verdict this package's own routes do, rather than a
// second guess at what counts as a malformed component vs. an unknown flow.
//
// The three answers are deliberately different codes. A component that
// could escape the runs root is a 400 — the client sent something
// malformed. A flow nothing has ever recorded (and that has no committed
// bundle) is a 404. Anything else is fine to proceed with; the caller (a
// SummaryFor call, typically) answers from there.
func ResolveFlow(d Deps, app, flow string) (status int, msg string, ok bool) {
	if err := runs.ValidateComponents(app, flow); err != nil {
		return http.StatusBadRequest, err.Error(), false
	}
	if err := d.check(); err != nil {
		return http.StatusInternalServerError, err.Error(), false
	}
	if !flowKnown(d, app, flow) {
		return http.StatusNotFound, fmt.Sprintf("no flow %s/%s: nothing recorded under %s and no reference bundle", app, flow, runs.RunsRoot(d.Cwd)), false
	}
	return 0, "", true
}

// flowKnown reports whether this app/flow names something that exists at
// all — a run directory or a committed bundle. It is what separates "you
// asked for a flow that does not exist" (404) from "this flow exists and
// cannot currently be evaluated" (409); collapsing the two would let a
// typo'd flow name read as a flow with a problem, and vice versa.
func flowKnown(d Deps, app, flow string) bool {
	if _, err := os.Stat(filepath.Join(runs.RunsRoot(d.Cwd), app, flow)); err == nil {
		return true
	}
	dir, err := refs.BundleDir(d.Cwd, app, flow)
	if err != nil {
		return false
	}
	_, err = os.Stat(dir)
	return err == nil
}

// statusForSummaryErr maps a summaryFor failure onto a status. It is 409,
// not 500 and not 404: the flow exists, the server is fine, and the
// comparison cannot be made in the state the project is in right now —
// typically because no reference has been accepted yet. A 404 would read as
// "no such flow" and a 200 with an empty Summary would read as "nothing
// differed", which is the one answer this surface must never give by
// accident.
func statusForSummaryErr(err error) int {
	if err == nil {
		return http.StatusOK
	}
	return http.StatusConflict
}

// --- accept / reject / rule --------------------------------------------

// acceptRequest is the optional body of POST .../accept. Both fields
// mirror `retrace ref accept` exactly (--run, --force), because
// global-constraints.md's API-first parity means the UI's button and the
// CLI's verb must be the same operation and not two.
type acceptRequest struct {
	Run   string `json:"run"`
	Force bool   `json:"force"`
}

func (s *server) handleAccept(w http.ResponseWriter, r *http.Request) {
	d, app, flow, ok := s.flowFrom(w, r)
	if !ok {
		return
	}
	var req acceptRequest
	if !decodeBody(w, r, &req) {
		return
	}
	sel := req.Run
	if sel == "" {
		sel = "latest"
	}
	runID := runs.FindRun(runs.RunsRoot(d.Cwd), app, flow, sel)
	if runID == "" {
		writeErr(w, http.StatusNotFound, fmt.Sprintf("no run matches %q for %s/%s", sel, app, flow))
		return
	}

	// refs.Accept, with the SAME three mask inputs `retrace ref accept`
	// passes. Dropping MaskedCheckpoints here would make the REST verb the
	// permissive one: a mask entry naming a checkpoint that does not exist
	// refuses at the CLI and would promote unredacted pixels through the
	// UI, which is the same operation reaching two different answers.
	res, err := refs.Accept(refs.AcceptOptions{
		Cwd: d.Cwd, RunsRoot: runs.RunsRoot(d.Cwd), App: app, Flow: flow, RunID: runID,
		MasksFor: masksFor(d.Cfg, flow), Force: req.Force,
		MaskedCheckpoints:        d.Cfg.FlowMaskEntryCheckpoints(flow),
		ProjectMaskedCheckpoints: d.Cfg.ProjectMaskEntryCheckpoints(),
	})
	if err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	// captureStatus and unmatchedMasks travel as VALUES, not as the stderr
	// sentences the CLI prints: an agent accepting through REST must be
	// able to see that it just promoted a non-ok capture, or that a
	// project-wide mask entry matched nothing, without parsing prose.
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true,
		"bundle": map[string]any{
			"dir":            res.Dir,
			"files":          res.Files,
			"bytes":          res.Bytes,
			"runId":          res.RunID,
			"captureStatus":  res.CaptureStatus,
			"unmatchedMasks": res.UnmatchedMasks,
		},
	})
}

// rejectRequest is the optional body of POST .../reject.
//
// Out exists to be REFUSED, exactly as ruleRequest's Scope and Flow do, and
// for a sharper reason (R-P). refs.Reject joins OutDir into a path and then
// os.RemoveAll's it before writing (refs.go:732-742); App, Flow and RunID
// all go through runs.ValidateComponents on the way there and OutDir does
// not — and it needs no traversal to escape, because filepath.Join honours
// an absolute path. Honouring a request-supplied Out would therefore make
// this verb an arbitrary-directory delete-and-write primitive on an
// UNAUTHENTICATED control plane that `--allow-host` can bind wide (R-K).
//
// The fix is to decline the input, not to sanitise it: rejecting absolute
// paths, rejecting "..", confining under a root — that is a class of fix
// notorious for being almost right, and here there is the option of not
// accepting the value at all. Do not validate what you can decline to
// accept. The server chooses the directory itself, deterministically,
// under .retrace/ in the project it was started in.
//
// `retrace ref reject --out DIR` keeps the flag: the operator typing it is
// standing in the project and is already trusted with rm. This is not the
// two-faces-of-one-verb divergence R-N rules against — the CLI and the REST
// call write the same bundle by the same code, and the flag that differs is
// about who chose the path, not about what the operation can express.
type rejectRequest struct {
	Run string `json:"run"`
	Out string `json:"out"`
}

func (s *server) handleReject(w http.ResponseWriter, r *http.Request) {
	d, app, flow, ok := s.flowFrom(w, r)
	if !ok {
		return
	}
	var req rejectRequest
	if !decodeBody(w, r, &req) {
		return
	}
	// BEFORE anything touches the filesystem — before FindRun, before
	// summaryFor writes a diff image, and long before refs.Reject removes
	// and recreates a directory. A refusal that merely changed the response
	// after the RemoveAll had already run would be no refusal at all.
	if strings.TrimSpace(req.Out) != "" {
		writeErr(w, http.StatusBadRequest,
			"\"out\" is not a dimension this endpoint accepts, and that is deliberate: it names a directory this server would then DELETE and write into, chosen by whoever sent the request rather than by the operator who started the server. "+
				"This control plane is unauthenticated and `retrace serve --addr` can be bound beyond loopback, so honouring it would be an arbitrary-directory delete-and-write. "+
				"The repro bundle is written under .retrace/repro/ in the project this server was started in; re-send without \"out\", or use `retrace ref reject --out DIR` at the CLI, where the operator typing the path is standing in the project.")
		return
	}
	sel := req.Run
	if sel == "" {
		sel = "latest"
	}
	runID := runs.FindRun(runs.RunsRoot(d.Cwd), app, flow, sel)
	if runID == "" {
		writeErr(w, http.StatusNotFound, fmt.Sprintf("no run matches %q for %s/%s", sel, app, flow))
		return
	}

	// Best-effort, exactly as `retrace ref reject` is: a repro bundle is
	// worth having even when the diff that would explain it cannot be
	// computed. What is NOT acceptable is a summary.json asserting a
	// comparison that never ran — refs.Reject omits the file for a nil
	// Summary, and the warning below says so in the response rather than
	// leaving the caller to notice the missing file.
	var summary *diff.Summary
	warning := ""
	if sum, err := summaryFor(d, app, flow, runID); err == nil {
		summary = &sum
	} else {
		warning = "no summary.json in this repro bundle — " + err.Error()
	}

	// No OutDir: refs.Reject defaults to <Cwd>/.retrace/repro, which is the
	// point of R-P — the directory is the server's decision, derived from
	// the project it was started in and from nothing the caller sent.
	res, err := refs.Reject(refs.RejectOptions{
		Cwd: d.Cwd, RunsRoot: runs.RunsRoot(d.Cwd), App: app, Flow: flow, RunID: runID,
		Summary: summary,
	})
	if err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	out := map[string]any{
		"ok":    true,
		"repro": map[string]any{"dir": res.Dir, "files": res.Files, "runId": runID},
	}
	if warning != "" {
		out["warning"] = warning
	}
	writeJSON(w, http.StatusOK, out)
}

// ruleRequest is the body of POST .../rule.
//
// Scope and Flow exist to be REFUSED, and their presence in this struct is
// the point: the wire-rule dialect can express NEITHER dimension —
// rules.Raw carries method/path/headers/body, config.WireRules is a
// top-level list with no per-flow nesting, and rules.Resolve keys on method
// plus normalized path alone, so the request and the response body consult
// the same globs. A rule minted "for the checkout flow" from a response
// field silences that field in every flow and in both bodies.
//
// Accepting the field and ignoring it is the plausible-value trap this
// phase keeps ruling against: `"scope":"resp"` SAILS, the agent that sent
// it believes it scoped, and the rule goes on silencing that field
// project-wide. `retrace ref rule` refuses --scope/--flow for exactly this
// reason (see cmd_ref.go), and the two faces of one verb must not disagree
// about what they can express. Silently accepting it here would be worse
// than at the CLI, not better: nobody reads a REST call in a pull request.
//
// The dialect gap is follow-up F.7; when it lands, these fields start
// working instead of being refused.
type ruleRequest struct {
	Scope   string `json:"scope"`
	Flow    string `json:"flow"`
	Field   string `json:"field"`
	Matcher string `json:"matcher"`
	Method  string `json:"method"`
	Path    string `json:"path"`
	// Why rides onto the rule so the overlay a reviewer reads in a pull
	// request explains itself. Not required here even under
	// `require_why: true` — the ratchet catches the omission at the next
	// Discover, naming the entry, and two checks in two places with two
	// messages is how a check comes to disagree with itself.
	Why string `json:"why"`
}

func (s *server) handleRule(w http.ResponseWriter, r *http.Request) {
	d, app, flow, ok := s.flowFrom(w, r)
	if !ok {
		return
	}
	_, _ = app, flow // the rule is project-wide; see ruleRequest.
	var req ruleRequest
	if !decodeBody(w, r, &req) {
		return
	}
	for _, f := range []struct{ name, val string }{{"scope", req.Scope}, {"flow", req.Flow}} {
		if strings.TrimSpace(f.val) != "" {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf(
				"%q is not a dimension a wire rule has, and that is deliberate: a wire rule is scoped by neither flow nor request/response. "+
					"The rule you are about to write would apply to EVERY flow in this project and to BOTH the request and the response body. "+
					"Narrow it with \"path\" and \"method\" instead — those are the only dimensions the rule dialect has, and re-send without %q.",
				f.name, f.name))
			return
		}
	}
	for _, f := range []struct{ name, val string }{{"field", req.Field}, {"matcher", req.Matcher}} {
		if strings.TrimSpace(f.val) == "" {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("%q is required", f.name))
			return
		}
	}

	raw := rules.Raw{Method: req.Method, Path: req.Path, Body: map[string]any{req.Field: req.Matcher}, Why: req.Why}
	// config.AppendWireRule is the SAME writer `retrace ref rule` uses: it
	// validates the matcher, is idempotent, and holds the cross-process
	// lock. Nothing here re-implements any of that.
	if err := config.AppendWireRule(d.Cwd, raw); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// Reloaded before responding, so the very next GET /api/queue is
	// evaluated WITH the rule. A server that kept its startup config would
	// tell the reviewer the rule had no effect.
	if err := s.reloadConfig(); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":   true,
		"rule": raw,
		// Every wire rule now in effect — the file's own plus the overlay's,
		// in the order they are applied — not just the overlay, because
		// "the rules that decide this project's diffs" is the question a
		// reviewer is actually asking.
		"rules": s.deps().Cfg.WireRules,
	})
}

// redactRequest is the "add redaction rule" form's body: a field name, a
// mode (destroy/encrypt/display — see core/trace.Mode), and a why. Flow is
// accepted but deliberately unused, same reasoning as ruleRequest.Scope: a
// redact entry is project-wide, matching config.RedactEntry's own shape,
// which carries no flow selector.
type redactRequest struct {
	Flow  string `json:"flow"`
	Field string `json:"field"`
	Mode  string `json:"mode"`
	Why   string `json:"why"`
}

func (s *server) handleRedact(w http.ResponseWriter, r *http.Request) {
	d, app, flow, ok := s.flowFrom(w, r)
	if !ok {
		return
	}
	_, _ = app, flow // the rule is project-wide; see redactRequest.
	var req redactRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Field) == "" {
		writeErr(w, http.StatusBadRequest, `"field" is required`)
		return
	}

	entry := config.RedactEntry{Field: req.Field, Mode: req.Mode, Why: req.Why}
	// config.AppendRedactEntry is the SAME writer `retrace rekey`'s sibling
	// commands and the config package's own tests exercise: it validates
	// the mode, is idempotent, and holds the cross-process lock. Nothing
	// here re-implements any of that.
	if err := config.AppendRedactEntry(d.Cwd, entry); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// Reloaded before responding, same reason handleRule reloads: the very
	// next GET /api/queue/{app}/{flow} must see the new rule, or a reviewer
	// who just added it would watch it appear to have no effect.
	if err := s.reloadConfig(); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"redact": entry,
		// Every redact entry now in effect — the file's own plus the
		// overlay's — matching handleRule's own "rules" field.
		"rules": s.deps().Cfg.Redact,
	})
}

// --- shots --------------------------------------------------------------

// shotSides is the four comparison panes, and it is also what the "unknown
// side" refusal enumerates, so the two cannot drift.
var shotSides = []string{"a", "b", "diff", "overlay"}

func (s *server) handleShot(w http.ResponseWriter, r *http.Request) {
	d, app, flow, ok := s.flowFrom(w, r)
	if !ok {
		return
	}
	WriteShot(w, d, app, flow, r.PathValue("side"), r.PathValue("name"))
}

// WriteShot writes one comparison-pane image, exported for the same reason
// WriteQueue/WriteItem are: ensemble/server's GET
// /api/retrace/shots/{app}/{flow}/{side}/{name} calls this directly, after
// its own ResolveFlow check, rather than re-deriving checkpoint/side
// resolution a second time.
func WriteShot(w http.ResponseWriter, d Deps, app, flow, side, name string) {
	// The name is validated BEFORE anything is resolved or read, so a
	// traversal attempt is refused as malformed rather than answered with
	// whatever the flow's state happens to be.
	if _, err := safeBase(name); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if !slices.Contains(shotSides, side) {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("unknown side %q — one of %s", side, strings.Join(shotSides, ", ")))
		return
	}

	sum, err := SummaryFor(d, app, flow)
	if err != nil {
		writeErr(w, statusForSummaryErr(err), err.Error())
		return
	}
	cp, found := checkpointNamed(sum, name)
	if !found {
		writeErr(w, http.StatusNotFound, fmt.Sprintf("no checkpoint %q in %s/%s", name, app, flow))
		return
	}
	// A generated side that was never written is a 404 with a reason, never
	// an empty 200: an empty body renders as a blank comparison pane, and a
	// blank pane reads as "identical".
	switch side {
	case "diff":
		if cp.Images.Diff == "" {
			writeErr(w, http.StatusNotFound, "no diff image: this checkpoint did not change")
			return
		}
	case "overlay":
		if cp.Images.Overlay == "" {
			writeErr(w, http.StatusNotFound, "no diff image: this checkpoint did not change")
			return
		}
	}

	dir, err := shotDirFor(&sum, diffDir(d.Cwd, app, flow), side)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	file, err := safeShotPath(dir, name)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	b, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		writeErr(w, http.StatusNotFound, fmt.Sprintf("no %s-side image for checkpoint %q", side, name))
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "image/png")
	_, _ = w.Write(b)
}

func (s *server) handleEvidence(w http.ResponseWriter, r *http.Request) {
	d, app, flow, ok := s.flowFrom(w, r)
	if !ok {
		return
	}
	WriteEvidence(w, d, app, flow)
}

func (s *server) handleVideo(w http.ResponseWriter, r *http.Request) {
	d, app, flow, ok := s.flowFrom(w, r)
	if !ok {
		return
	}
	WriteVideo(w, r, d, app, flow, r.PathValue("name"))
}

func (s *server) handleReport(w http.ResponseWriter, r *http.Request) {
	d, app, flow, ok := s.flowFrom(w, r)
	if !ok {
		return
	}
	WriteReport(w, r, d, app, flow)
}

// shotDirFor resolves one of the four comparison sides to the directory
// safeShotPath joins onto. Each must therefore be a directory with a
// shots/ child — the diff layout contract (diff.writeCheckpointImages)
// exists to make that true for the two generated sides, and a run/bundle
// directory is already that shape for the two captured ones.
func shotDirFor(sum *diff.Summary, outDir, side string) (string, error) {
	switch side {
	case "a":
		return sum.A.Dir, nil
	case "b":
		return sum.B.Dir, nil
	case "diff", "overlay":
		return filepath.Join(outDir, side), nil // outDir/diff/shots/<name>.png
	}
	return "", fmt.Errorf("unknown side %q", side)
}

// safeBase is the ONE guard body for a caller-supplied checkpoint name.
// ServeMux's path cleaning operates on the still-escaped path, so
// "%2e%2e%2f" reaches a handler as literal "../" — rooting at "/" before
// Clean, then rejecting any remaining separator, is what makes that
// harmless.
func safeBase(name string) (string, error) {
	clean := path.Clean("/" + name)
	base := strings.TrimPrefix(clean, "/")
	if base == "" || strings.ContainsAny(base, `/\`) {
		return "", fmt.Errorf("invalid checkpoint name %q", name)
	}
	return base, nil
}

// safeShotPath resolves a checkpoint name to a file inside the run
// directory and nowhere else.
//
// It re-validates through safeBase even though handleShot has already done
// so, and deliberately: this function JOINS a caller-supplied component
// into a filesystem path, so the guard belongs at the join and not at a
// statement order somewhere else (global-constraints.md). The guard body
// itself exists once, in safeBase.
func safeShotPath(runDir, name string) (string, error) {
	base, err := safeBase(name)
	if err != nil {
		return "", err
	}
	return filepath.Join(runDir, "shots", base+".png"), nil
}

func checkpointNamed(s diff.Summary, name string) (diff.CheckpointVerdict, bool) {
	for _, cp := range s.Checkpoints {
		if cp.Name == name {
			return cp, true
		}
	}
	return diff.CheckpointVerdict{}, false
}

// --- ui -----------------------------------------------------------------

// uiHandler is the embedded review UI, built once: ui.Handler walks the
// embedded FS to build its file server, and doing that per request would be
// the same work on every page load.
var uiHandler = ui.Handler()

// handleUI serves the review UI at every non-/api path — the retrace-ui
// bundle embedded by retrace/serve/ui, as an SPA fallback so that
// /?app=web&flow=checkout survives a hard refresh.
//
// It stays HERE, in the dispatch New makes, and is NOT registered as a
// "GET /" pattern in the API mux: a catch-all in that mux would match every
// API path no route matched, so GET on a POST-only verb would answer 200
// carrying the app shell instead of ServeMux's 405, and an agent — the other
// half of this API-first surface — would read HTML as success. See routes'
// doc comment.
//
// GET and HEAD only: a POST to the app shell is not a page load, and
// answering it 200 would make "the verb you used is not this route's verb"
// invisible on the UI half of the surface too.
func (s *server) handleUI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeErr(w, http.StatusMethodNotAllowed, "the review UI answers GET and HEAD only")
		return
	}
	uiHandler.ServeHTTP(w, r)
}

// --- shared -------------------------------------------------------------

// decodeBody reads an optional JSON body. An absent or empty body leaves v
// at its zero value — every verb here has a working default — while a
// malformed one, or one carrying a key the struct does not have, is
// refused. DisallowUnknownFields is what keeps a misspelt or invented field
// from being silently dropped: a caller that sends {"forse":true} must be
// told, not quietly given the safe default it did not ask for.
func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.Body == nil {
		return true
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "reading the request body: "+err.Error())
		return false
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return true
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "the request body is not the expected JSON document: "+err.Error())
		return false
	}
	return true
}

// masksFor is the one place config.Rect becomes pixel.Rect on this surface,
// mirroring cmd_ref.go's project.masksFor — refs never imports
// retrace/config, so the conversion happens in the caller's closure through
// pixel.RectsFrom, exactly once.
func masksFor(cfg *config.Config, flow string) func(string) []pixel.Rect {
	return func(checkpoint string) []pixel.Rect {
		return pixel.RectsFrom(cfg.MasksFor(flow, checkpoint))
	}
}
