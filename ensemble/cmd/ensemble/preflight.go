package main

import (
	"fmt"
	"net"
	"os/exec"
	"sort"
	"strings"

	"github.com/caribou-crew/ensemble/ensemble/config"
)

// checkPortsFree verifies every port the active stack (cfg filtered
// through activeProfiles, see Config.ActivePorts) would bind is actually
// free on this host, before ensemble starts anything. A conflict here is
// almost always either a stale ensemble run that didn't get torn down
// cleanly or an unrelated process squatting the port — either way, better
// to fail fast with a clear message than have some service silently miss
// its health gate later with no explanation. A port belonging to a
// profile-gated service that isn't active right now is never checked, so
// it being in use by something else is not a conflict.
func checkPortsFree(cfg *config.Config, activeProfiles []string) error {
	ports := cfg.ActivePorts(activeProfiles)
	nums := make([]int, 0, len(ports))
	for p := range ports {
		nums = append(nums, p)
	}
	sort.Ints(nums)

	var conflicts []string
	for _, port := range nums {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			msg := fmt.Sprintf("port %d (%s) is already in use", port, ports[port])
			if who := identifyPort(port); who != "" {
				msg += " by " + who
			}
			conflicts = append(conflicts, msg)
			continue
		}
		ln.Close()
	}
	if len(conflicts) == 0 {
		return nil
	}
	return fmt.Errorf("port conflict, refusing to start:\n  %s", strings.Join(conflicts, "\n  "))
}

// identifyPort best-effort names the process listening on port, via lsof
// (present on macOS and most Linux dev boxes). Empty string on any
// failure — not installed, nothing found, unexpected output — this is a
// diagnostic nicety only, never load-bearing: checkPortsFree still reports
// the conflict either way.
func identifyPort(port int) string {
	out, err := exec.Command("lsof", "-nP", fmt.Sprintf("-iTCP:%d", port), "-sTCP:LISTEN").Output()
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return "" // header only, or empty: lsof found no listener (race with our own probe)
	}
	fields := strings.Fields(lines[1])
	if len(fields) < 2 {
		return ""
	}
	return fmt.Sprintf("%s (pid %s)", fields[0], fields[1])
}
