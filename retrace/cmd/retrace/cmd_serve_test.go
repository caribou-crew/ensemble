package main

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- harness ------------------------------------------------------------

// syncBuffer is an io.Writer a test can read while the child is still
// writing to it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type serveProc struct {
	cmd    *exec.Cmd
	stdout *syncBuffer
	stderr *syncBuffer
	url    string     // http://host:port, as the server itself reported it
	done   chan error // the ONE Wait for this process; a second one errors
}

// startServe launches `retrace serve` and waits for it to say what it bound.
// It returns nil (with the process already reaped) when the command exited
// instead of listening, which is what the refusal arms assert.
func startServe(t *testing.T, bin, cwd string, args ...string) (*serveProc, int) {
	t.Helper()
	cmd := exec.Command(bin, append([]string{"serve"}, args...)...)
	cmd.Dir = cwd
	stdout, stderr := &syncBuffer{}, &syncBuffer{}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting retrace serve: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if line := listeningURL(stderr.String()); line != "" {
			return &serveProc{cmd: cmd, stdout: stdout, stderr: stderr, url: line, done: done}, 0
		}
		select {
		case err := <-done:
			code := 0
			if exitErr, ok := err.(*exec.ExitError); ok {
				code = exitErr.ExitCode()
			} else if err != nil {
				t.Fatalf("retrace serve: %v\nstderr: %s", err, stderr.String())
			}
			return nil, code
		default:
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("retrace serve never reported a listening address\nstderr: %s", stderr.String())
	return nil, 0
}

func listeningURL(stderr string) string {
	const marker = "listening on "
	i := strings.Index(stderr, marker)
	if i < 0 {
		return ""
	}
	rest := stderr[i+len(marker):]
	j := strings.IndexAny(rest, "\r\n")
	if j < 0 {
		return ""
	}
	return strings.TrimSpace(rest[:j])
}

// stop sends the interrupt a human's Ctrl-C sends and waits for the process
// to leave, which also pins the graceful-shutdown wiring.
func (p *serveProc) stop(t *testing.T) {
	t.Helper()
	if err := p.cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("interrupting retrace serve: %v", err)
	}
	select {
	case err := <-p.done:
		if err != nil {
			t.Fatalf("retrace serve did not exit cleanly on interrupt: %v\nstderr: %s", err, p.stderr.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("retrace serve did not exit within the shutdown grace after an interrupt\nstderr: %s", p.stderr.String())
	}
}

// healthWithHost fetches /api/health from the running server, presenting
// the given Host header — which is the value the DNS-rebinding guard
// judges.
func healthWithHost(t *testing.T, url, host string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url+"/api/health", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if host != "" {
		req.Host = host
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("GET %s/api/health (Host %q): %v", url, host, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// p2Stderr runs the same refusal against a port SOMETHING ELSE IS ALREADY
// HOLDING, and returns what the command said.
//
// This is what pins the ORDERING that "nothing is accepting afterwards"
// cannot: a command that bound first and refused second would exit with
// "cannot bind: address already in use" here, and would leave the port
// closed by the time a dial could notice, because a refusing process is a
// process that has exited. The refusal must be about the flags — decided
// before anything is bound (R-I's bar, and the reason it exists: a flag
// that binds and THEN refuses hands the operator a running server and an
// error about something they did not do).
func p2Stderr(t *testing.T, bin, ip, host string) string {
	t.Helper()
	ln, err := net.Listen("tcp", ip+":0")
	if err != nil {
		t.Fatalf("holding a port: %v", err)
	}
	defer ln.Close()
	p, code := startServe(t, bin, t.TempDir(), "--addr", ln.Addr().String(), "--allow-host", host)
	if p != nil {
		p.stop(t)
		t.Fatalf("--allow-host %q was accepted on a held port", host)
	}
	if code == 0 {
		t.Fatalf("--allow-host %q exited 0 on a held port", host)
	}
	// Read the stderr of that run: startServe returns nil for it, so it is
	// re-run here rather than plumbed back — the process is cheap and the
	// alternative is a second return value every other caller ignores.
	cmd := exec.Command(bin, "serve", "--addr", ln.Addr().String(), "--allow-host", host)
	cmd.Dir = t.TempDir()
	out, _ := cmd.CombinedOutput()
	return string(out)
}

// --- R-K: the wildcard pair ---------------------------------------------

// R-K, all three arms plus the two mirrors it makes non-optional.
//
// `--allow-host "*"` turns Host and Origin matching OFF entirely
// (core/httpguard), so paired with a non-loopback --addr it yields a fully
// open, unauthenticated control plane serving captured traffic — reached by
// an operator who typed the wildcard because enumerating hostnames was
// annoying. Either flag ALONE is fine. The refusal is on the pair.
func TestServeRefusesAWildcardAllowHostOnANonLoopbackBind(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping go-build subprocess test in -short mode")
	}
	bin := buildRetrace(t)
	ip := hostIP(t)

	// Arm 1: refused, and NO LISTENER OPENED — asserted by dialling the
	// port, not merely by reading the error, which is R-I's bar for the
	// same reason: a server that binds and then refuses everything is a
	// running server.
	//
	// "*:8080" and "[*]" are in the same list deliberately: core/httpguard
	// strips a port before matching, so those spellings used to reduce to
	// the wildcard and turn matching off. The library no longer honours
	// them, and this command refuses every star-shaped spelling on a wide
	// bind rather than binding wide and then answering nothing.
	for _, host := range []string{"*", "*:8080", "[*]", "*.internal"} {
		t.Run("refuses "+host, func(t *testing.T) {
			port := freePort(t)
			p, code := startServe(t, bin, t.TempDir(), "--addr", ip+":"+port, "--allow-host", host)
			if p != nil {
				p.stop(t)
				t.Fatalf("--addr %s:%s --allow-host %q was accepted and bound", ip, port, host)
			}
			if code == 0 {
				t.Fatalf("--allow-host %q on a non-loopback bind exited 0", host)
			}
			conn, err := net.DialTimeout("tcp", ip+":"+port, time.Second)
			if err == nil {
				conn.Close()
				t.Fatalf("something is accepting on %s:%s after the refusal", ip, port)
			}
			conn, err = net.DialTimeout("tcp", "127.0.0.1:"+port, time.Second)
			if err == nil {
				conn.Close()
				t.Fatalf("something is accepting on 127.0.0.1:%s after the refusal", port)
			}
			if !strings.Contains(p2Stderr(t, bin, ip, host), "--allow-host") {
				t.Fatalf("--allow-host %q: the refusal did not come from the flag pair", host)
			}
		})
	}

	// A non-loopback bind with NO --allow-host at all is refused for the
	// same reason from the other direction: it would bind wide and then
	// answer nothing, which is a flag describing a guarantee it does not
	// make.
	t.Run("refuses a wide bind that names no host", func(t *testing.T) {
		port := freePort(t)
		p, code := startServe(t, bin, t.TempDir(), "--addr", ip+":"+port)
		if p != nil {
			p.stop(t)
			t.Fatalf("--addr %s:%s with no --allow-host was accepted and bound", ip, port)
		}
		if code == 0 {
			t.Fatalf("a wide bind with no --allow-host exited 0")
		}
	})

	// Arm 2, the over-refusal mirror: a non-loopback bind that NAMES its
	// host binds and serves. Five times this phase a widened refusal has
	// needed one of these.
	t.Run("a named host still binds and serves", func(t *testing.T) {
		p, _ := startServe(t, bin, t.TempDir(), "--addr", ip+":0", "--allow-host", "build.internal")
		if p == nil {
			t.Fatalf("--addr %s:0 --allow-host build.internal was refused", ip)
		}
		defer p.stop(t)
		if code, body := healthWithHost(t, p.url, "build.internal"); code != http.StatusOK {
			t.Fatalf("Host build.internal: status = %d, want 200\n%s", code, body)
		}
		// And it answers as THAT name and nothing else — the allow-list is
		// an allow-list, not decoration.
		if code, _ := healthWithHost(t, p.url, "attacker.example"); code != http.StatusForbidden {
			t.Fatalf("Host attacker.example on a named wide bind: status = %d, want 403", code)
		}
		// The warning reaches STDERR, through the built binary. serve's
		// stdout may be consumed, so a warning printed there is a warning
		// nobody sees.
		if !strings.Contains(p.stderr.String(), "warning") || !strings.Contains(p.stderr.String(), "NOT loopback") {
			t.Fatalf("a wide bind printed no warning to stderr:\n%s", p.stderr.String())
		}
		if strings.Contains(p.stdout.String(), "warning") {
			t.Fatalf("the warning went to stdout, which may be consumed:\n%s", p.stdout.String())
		}
	})

	// Arm 3, the second mirror and the one most likely to be broken by an
	// over-eager fix: the wildcard on a LOOPBACK bind is harmless and stays
	// legal — the listener already decides reachability there.
	t.Run("the wildcard on a loopback bind still binds and serves", func(t *testing.T) {
		p, _ := startServe(t, bin, t.TempDir(), "--addr", "127.0.0.1:0", "--allow-host", "*")
		if p == nil {
			t.Fatalf("--addr 127.0.0.1:0 --allow-host '*' was refused")
		}
		defer p.stop(t)
		if code, body := healthWithHost(t, p.url, "anything.example"); code != http.StatusOK {
			t.Fatalf("wildcard on loopback: status = %d, want 200 (matching is off)\n%s", code, body)
		}
		if !strings.Contains(p.stderr.String(), "NOT loopback") {
			return // no warning is expected on a loopback bind
		}
		t.Fatalf("a loopback bind warned about being wide:\n%s", p.stderr.String())
	})
}

// The default is loopback, with no flags at all — the posture a reader of
// the help text is entitled to assume, and the one every other listener in
// this repo takes.
func TestServeBindsLoopbackByDefaultAndServesTheQueue(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping go-build subprocess test in -short mode")
	}
	bin := buildRetrace(t)
	p, _ := startServe(t, bin, t.TempDir(), "--addr", "127.0.0.1:0")
	if p == nil {
		t.Fatalf("the default bind was refused")
	}
	defer p.stop(t)

	if !strings.HasPrefix(p.url, "http://127.0.0.1:") {
		t.Fatalf("listening URL = %q, want a loopback address", p.url)
	}
	if code, body := healthWithHost(t, p.url, ""); code != http.StatusOK || !strings.Contains(body, `"ok":true`) {
		t.Fatalf("GET /api/health: status = %d\n%s", code, body)
	}
	// No --allow-host means nil AllowedHosts, which core/httpguard treats
	// as loopback-only — never as "no allow-list, so allow anything".
	if code, _ := healthWithHost(t, p.url, "attacker.example"); code != http.StatusForbidden {
		t.Fatalf("Host attacker.example on the default bind: status = %d, want 403", code)
	}
	// An empty project has an empty queue, and it is an array rather than
	// null so a client cannot read "not loaded yet" as "nothing to review".
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Get(p.url + "/api/queue")
	if err != nil {
		t.Fatalf("GET /api/queue: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(b), `"items":[]`) {
		t.Fatalf("GET /api/queue: status = %d\n%s", resp.StatusCode, b)
	}
	// The default written into the help text is the one the code uses.
	if defaultServeAddr != "127.0.0.1:4800" {
		t.Fatalf("defaultServeAddr = %q, but the usage text promises 127.0.0.1:4800", defaultServeAddr)
	}
}

// A malformed --addr is refused before anything binds, rather than being
// handed to net.Listen to produce a message about a value the operator
// never typed.
func TestServeRefusesAnAddrThatIsNotHostPort(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping go-build subprocess test in -short mode")
	}
	bin := buildRetrace(t)
	p, code := startServe(t, bin, t.TempDir(), "--addr", "127.0.0.1")
	if p != nil {
		p.stop(t)
		t.Fatalf("--addr 127.0.0.1 (no port) was accepted")
	}
	if code != exitUsage {
		t.Fatalf("exit = %d, want %d", code, exitUsage)
	}
}

// loopbackAddr is the ONE reading of "is this loopback" that `replay
// --listen` and `serve --addr` both consult, so it is pinned directly as
// well as through both commands.
func TestLoopbackAddrReadsEveryShapeOfAddressTheSameWayForBothCommands(t *testing.T) {
	cases := []struct {
		addr string
		want bool
		err  bool
	}{
		{"127.0.0.1:4800", true, false},
		{"127.0.0.2:4800", true, false},
		{"[::1]:4800", true, false},
		{"localhost:4800", true, false},
		{"0.0.0.0:4800", false, false},
		{":4800", false, false},
		{"[::]:4800", false, false},
		{"8.8.8.8:4800", false, false},
		{"127.0.0.1", false, true},
		{"", false, true},
	}
	for _, c := range cases {
		t.Run(c.addr, func(t *testing.T) {
			got, err := loopbackAddr(c.addr)
			if (err != nil) != c.err {
				t.Fatalf("loopbackAddr(%q) error = %v, want an error: %v", c.addr, err, c.err)
			}
			if got != c.want {
				t.Fatalf("loopbackAddr(%q) = %v, want %v", c.addr, got, c.want)
			}
		})
	}
}
