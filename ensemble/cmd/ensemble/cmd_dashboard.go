package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os/exec"
	"runtime"
)

// openBrowserFn launches url in the OS default browser. It's a package var
// (not a direct call to openBrowser) so tests can substitute a no-op stub
// and exercise cmdDashboard's flag/reachability logic without actually
// spawning a browser process.
var openBrowserFn = openBrowser

// cmdDashboard opens the running control plane's dashboard in the default
// browser. It doesn't serve anything itself — server.New already mounts
// the dashboard at "/" whenever `ensemble up` is running (see
// ensemble/server/ui) — this is just a shortcut so users don't have to
// remember/type the address.
func cmdDashboard(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("dashboard", flag.ContinueOnError)
	fs.SetOutput(stderr)
	apiURL := fs.String("api-url", defaultAPIURL(), "ensemble control-plane API base URL")
	noOpen := fs.Bool("no-open", false, "print the dashboard URL instead of opening a browser")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	// --no-open is a pure URL lookup: it must work even when nothing is
	// running (e.g. to see the address before `ensemble up`), so it
	// deliberately skips the reachability check below.
	if *noOpen {
		fmt.Fprintln(stdout, *apiURL)
		return 0
	}

	// Checked before opening so a stopped stack fails with a clear message
	// instead of a browser tab showing a generic connection error.
	if _, err := NewClient(*apiURL).Status(context.Background()); err != nil {
		fmt.Fprintf(stderr, "ensemble: dashboard: %s is not reachable (is `ensemble up` running?): %v\n", *apiURL, err)
		return 1
	}

	fmt.Fprintln(stdout, *apiURL)
	if err := openBrowserFn(*apiURL); err != nil {
		fmt.Fprintf(stderr, "ensemble: dashboard: opening browser: %v\n", err)
		return 1
	}
	return 0
}

// openBrowser launches url in the OS default browser.
func openBrowser(url string) error {
	return browserCommand(runtime.GOOS, url).Start()
}

// browserCommand returns the OS-specific "open a URL" launcher for goos.
// There's no cross-platform stdlib way to do this — every OS has its own.
func browserCommand(goos, url string) *exec.Cmd {
	switch goos {
	case "darwin":
		return exec.Command("open", url)
	case "windows":
		// rundll32, not `start` — `start` is a cmd.exe builtin, not an
		// executable, so exec.Command can't invoke it directly.
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default: // linux and other unix
		return exec.Command("xdg-open", url)
	}
}
