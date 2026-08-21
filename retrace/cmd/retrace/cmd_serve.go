package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/caribou-crew/ensemble/core/buildinfo"
	"github.com/caribou-crew/ensemble/retrace/config"
	"github.com/caribou-crew/ensemble/retrace/serve"
)

// defaultServeAddr is loopback, and that is the product's default posture:
// a review server is reachable from the machine it runs on unless somebody
// says otherwise, in words, on the command line.
const defaultServeAddr = "127.0.0.1:4800"

// serveShutdownGrace bounds how long the graceful close waits for in-flight
// requests once Ctrl-C arrives. A diff of a large flow can be mid-flight,
// so this is not as short as replay's.
const serveShutdownGrace = 5 * time.Second

// hostList collects a repeatable --allow-host flag.
type hostList []string

func (h *hostList) String() string { return strings.Join(*h, ",") }
func (h *hostList) Set(v string) error {
	v = strings.TrimSpace(v)
	if v == "" {
		return errors.New("--allow-host needs a hostname")
	}
	*h = append(*h, v)
	return nil
}

// cmdServe runs the review queue's REST surface (and, from Task 15, the UI)
// until Ctrl-C.
//
// The bind policy is DELIBERATELY different from `retrace replay --listen`,
// which refuses a non-loopback address outright (R-I), and the difference
// is not an inconsistency:
//
//   - replay serves recorded traffic verbatim — request and response bodies
//     lifted out of a bundle. Nobody needs that off-box; an SSH tunnel is
//     the answer.
//   - serve is the review queue: a human looking at screenshots and
//     approving diffs. Wanting that reachable from a laptop while the runs
//     happen on a build box is an ordinary, legitimate need, and refusing it
//     outright would be an over-refusal.
//
// So: loopback default, and a non-loopback bind is an explicit opt-in that
// must NAME the hostnames it answers as. R-I's actual invariant — a flag
// must not describe a guarantee that is not made — is kept, because the
// wide path exists, is reachable only by naming a second flag, and the help
// says so.
func cmdServe(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var allow hostList
	addr := fs.String("addr", defaultServeAddr, "address the review server binds; a non-loopback address requires --allow-host")
	openUI := fs.Bool("open", false, "open the review UI in a browser once the server is listening")
	fs.Var(&allow, "allow-host", "hostname this server may be reached as (repeatable). Required for a non-loopback --addr; on a loopback bind it is optional, and \"*\" is only accepted there")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	// Every refusal below happens BEFORE anything is bound. A flag that
	// binds and THEN rejects every request hands the operator a running
	// server, a live port and a stream of errors that have nothing to do
	// with what they typed (R-I).
	loopback, err := loopbackAddr(*addr)
	if err != nil {
		return fail(stderr, "serve: --addr %v", err)
	}
	if !loopback {
		if len(allow) == 0 {
			return fail(stderr, "serve: --addr %s is not a loopback address, and a wide bind must name the hostnames it answers as: pass --allow-host HOST (repeatable) for each name this server will be reached by.\n"+
				"Without it every request would be refused by the DNS-rebinding guard, which is a running server that answers nothing — reachable, useless, and confusing.", *addr)
		}
		// R-K. "*" turns host and origin matching OFF entirely
		// (core/httpguard), so a non-loopback bind carrying it is a fully
		// open, unauthenticated control plane serving captured traffic and
		// screenshots — reached by an operator who typed the wildcard
		// because enumerating hostnames was annoying. It is the plausible
		// value: it sails through every seam, reads as "I configured the
		// allow-list", and is indistinguishable in a shell history from a
		// careful one.
		//
		// Every star-shaped spelling is refused here, not just the bare
		// "*": the guard honours only the exact wildcard and drops the
		// rest, so "*.internal" would bind wide and then answer nothing,
		// which is the same "a flag describing a guarantee it does not
		// make" defect wearing a different costume.
		//
		// The refusal is on the PAIR. "*" on a loopback bind stays legal —
		// the listener already decides reachability there.
		for _, h := range allow {
			if strings.Contains(h, "*") {
				return fail(stderr, "serve: --allow-host %q cannot be combined with the non-loopback --addr %s: a wide bind must name the hostnames it answers as, one --allow-host per name.\n"+
					"\"*\" turns the Host and Origin checks off entirely, so this pair would publish an unauthenticated review server — captured traffic, screenshots and the accept verb — to everything that can route to %s.\n"+
					"Either name the hosts (--allow-host build.internal), or keep the wildcard and bind loopback.", h, *addr, *addr)
			}
		}
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fail(stderr, "serve: cannot determine the working directory: %v", err)
	}
	cfg, err := config.Discover(cwd)
	if err != nil {
		return fail(stderr, "%v", err)
	}

	// Bound explicitly rather than through ListenAndServe, so the address
	// actually bound (which may differ from what was asked for: port 0) is
	// what gets printed and opened.
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return fail(stderr, "serve: cannot bind %s: %v", *addr, err)
	}

	// A deliberate, explicitly-flagged, correctly-spelled wide bind is not
	// a defect — it is an operator doing a legitimate thing that carries
	// risk — so it gets a warning rather than a gate. (The wildcard PAIR
	// above is the case with no innocent reading, which is why that one is
	// refused.) It goes to STDERR: this command's stdout may be consumed,
	// and stderr is the channel a human and a CI log both keep.
	if !loopback {
		fmt.Fprintf(stderr, "retrace: warning: serving the review queue on %s, which is NOT loopback — anything that can route here can read every recorded request, response and screenshot, and can accept or reject a reference. It answers as %s and nothing else; an SSH tunnel is the safer shape if you did not mean this.\n",
			ln.Addr(), strings.Join(allow, ", "))
	}

	url := "http://" + displayAddr(ln.Addr())
	fmt.Fprintf(stderr, "retrace serve: listening on %s\n", url)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *openUI {
		openBrowser(stderr, url)
	}

	// AllowedHosts is passed straight through: nil (no --allow-host) is the
	// SAFE zero value in core/httpguard — loopback only, never "no
	// allow-list, so allow anything".
	h := serve.New(serve.Deps{
		Cwd: cwd, Cfg: cfg, AllowedHosts: allow, Version: buildinfo.Resolve(version),
	})
	srv := &http.Server{
		Handler: h,
		// BaseContext ties every accepted connection — and therefore every
		// request — to ctx, so a handler blocked on r.Context().Done()
		// unblocks the instant Ctrl-C arrives rather than only when the
		// client disconnects.
		BaseContext: func(net.Listener) context.Context { return ctx },
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fail(stderr, "serve: %v", err)
		}
		return exitOK
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), serveShutdownGrace)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fail(stderr, "serve: shutting down: %v", err)
		}
		<-errCh
		fmt.Fprintln(stderr, "retrace serve: stopped")
		return exitOK
	}
}

// loopbackAddr reports whether addr binds loopback and nothing else. It is
// the ONE determination of that fact in this command — `replay
// --listen` (which refuses a non-loopback address) and `serve --addr`
// (which allows one behind --allow-host) reach different DECISIONS off the
// same reading, and two copies would be two places for the 0.0.0.0 case, a
// name that resolves both ways, or an IPv6 literal to be handled
// differently.
//
// An error means "could not be shown to be loopback", which every caller
// must treat as a refusal rather than as a false: an address that does not
// resolve is not evidence of anything.
func loopbackAddr(addr string) (bool, error) {
	host, _, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		return false, fmt.Errorf("%q is not a host:port address: %v", addr, err)
	}
	// An empty host is 0.0.0.0/[::] — every interface. It is the widest
	// bind there is, so it reads as NOT loopback rather than as
	// "unspecified, probably fine": the zero value here must be the
	// refusing one.
	if strings.TrimSpace(host) == "" {
		return false, nil
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback(), nil
	}
	// A NAME (localhost, or something that merely looks like it). Every
	// address it resolves to must be loopback — one non-loopback answer is
	// a bind on a real interface, whatever the name suggested.
	ips, err := net.LookupIP(host)
	if err != nil || len(ips) == 0 {
		return false, fmt.Errorf("%s: cannot resolve %q, and an address that does not resolve cannot be shown to be loopback: %v", addr, host, err)
	}
	for _, ip := range ips {
		if !ip.IsLoopback() {
			return false, nil
		}
	}
	return true, nil
}

// displayAddr turns a listener address into something a browser can open:
// the unspecified address (0.0.0.0 / [::]) is not a destination, so it is
// shown as loopback, which is where the operator's own browser will reach
// it.
func displayAddr(a net.Addr) string {
	host, port, err := net.SplitHostPort(a.String())
	if err != nil {
		return a.String()
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		return net.JoinHostPort("127.0.0.1", port)
	}
	return net.JoinHostPort(host, port)
}

// openBrowser is best effort: a browser that will not open is a nuisance,
// not a reason to fail a server that is already listening. The failure is
// reported so the operator knows to open the URL themselves.
func openBrowser(stderr io.Writer, url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(stderr, "retrace: warning: could not open a browser (%v) — open %s yourself\n", err, url)
		return
	}
	go func() { _ = cmd.Wait() }()
}
