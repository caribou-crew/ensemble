package proxy

import (
	"net"
	"strings"
	"testing"
)

// TestListenRejectsNonLoopbackHostname pins the enforcement at the Listen
// seam itself — the exported function core/stub binds through — not just
// at ServeStoppable. A stub reachable off-loopback records forgeable hops
// into the same Recorder the proxy trusts.
func TestListenRejectsNonLoopbackHostname(t *testing.T) {
	saved := lookupIP
	lookupIP = func(string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("93.184.216.34")}, nil
	}
	defer func() { lookupIP = saved }()

	lns, _, err := Listen("not-actually-local:0")
	for _, ln := range lns {
		ln.Close()
	}
	if err == nil {
		t.Fatal("Listen with a non-loopback-resolving host returned nil error, want a refusal")
	}
	if !strings.Contains(err.Error(), "non-loopback") {
		t.Errorf("error = %v, want it to name the non-loopback reason", err)
	}
}
