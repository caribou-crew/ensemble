package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/caribou-crew/ensemble/core/proxy"
)

func cmdLatency(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: ensemble latency <list|set|reset|arm-all|from-datadog|apply> [flags]")
		return 2
	}
	switch args[0] {
	case "list":
		return cmdLatencyList(args[1:], stdout, stderr)
	case "set":
		return cmdLatencySet(args[1:], stdout, stderr)
	case "reset":
		return cmdLatencyReset(args[1:], stdout, stderr)
	case "arm-all":
		return cmdLatencyArmAll(args[1:], stdout, stderr)
	case "from-datadog":
		return cmdLatencyFromDatadog(args[1:], stdout, stderr)
	case "apply":
		return cmdLatencyApply(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "ensemble: latency: unknown subcommand %q\n", args[0])
		return 2
	}
}

func cmdLatencyList(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("latency list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	apiURL := fs.String("api-url", defaultAPIURL(), "ensemble control-plane API base URL")
	jsonOut := fs.Bool("json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	c := NewClient(*apiURL)
	res, err := c.LatencyList(context.Background())
	if err != nil {
		fmt.Fprintf(stderr, "ensemble: latency list: %v\n", err)
		return 1
	}
	return printLatencyRules(stdout, *jsonOut, res)
}

func cmdLatencySet(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("latency set", flag.ContinueOnError)
	fs.SetOutput(stderr)
	apiURL := fs.String("api-url", defaultAPIURL(), "ensemble control-plane API base URL")
	jsonOut := fs.Bool("json", false, "output JSON")
	target := fs.String("target", "", "target service name, or * for any")
	path := fs.String("path", "/", "path prefix")
	fixed := fs.Float64("fixed", 0, "fixed delay in ms")
	p50 := fs.Float64("p50", 0, "p50 delay in ms (used when fixed is 0)")
	p95 := fs.Float64("p95", 0, "p95 delay in ms")
	p99 := fs.Float64("p99", 0, "p99 delay in ms")
	enabled := fs.Bool("enabled", false, "arm this rule")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *target == "" {
		fmt.Fprintln(stderr, "ensemble: latency set: --target is required")
		return 2
	}

	c := NewClient(*apiURL)
	rule := proxy.LatencyRule{
		Target: *target, Path: *path,
		FixedMs: *fixed, P50: *p50, P95: *p95, P99: *p99,
		Enabled: *enabled,
	}
	res, err := c.LatencySet(context.Background(), rule)
	if err != nil {
		fmt.Fprintf(stderr, "ensemble: latency set: %v\n", err)
		return 1
	}
	return printLatencyRules(stdout, *jsonOut, res)
}

func cmdLatencyReset(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("latency reset", flag.ContinueOnError)
	fs.SetOutput(stderr)
	apiURL := fs.String("api-url", defaultAPIURL(), "ensemble control-plane API base URL")
	jsonOut := fs.Bool("json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	c := NewClient(*apiURL)
	res, err := c.LatencyReset(context.Background())
	if err != nil {
		fmt.Fprintf(stderr, "ensemble: latency reset: %v\n", err)
		return 1
	}
	return printLatencyRules(stdout, *jsonOut, res)
}

func cmdLatencyArmAll(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("latency arm-all", flag.ContinueOnError)
	fs.SetOutput(stderr)
	apiURL := fs.String("api-url", defaultAPIURL(), "ensemble control-plane API base URL")
	jsonOut := fs.Bool("json", false, "output JSON")
	enabled := fs.Bool("enabled", false, "enable (true) or disable (false) every rule")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	c := NewClient(*apiURL)
	res, err := c.LatencyArmAll(context.Background(), *enabled)
	if err != nil {
		fmt.Fprintf(stderr, "ensemble: latency arm-all: %v\n", err)
		return 1
	}
	return printLatencyRules(stdout, *jsonOut, res)
}

func cmdLatencyFromDatadog(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("latency from-datadog", flag.ContinueOnError)
	fs.SetOutput(stderr)
	apiURL := fs.String("api-url", defaultAPIURL(), "ensemble control-plane API base URL")
	jsonOut := fs.Bool("json", false, "output JSON")
	target := fs.String("target", "", "target service name, or * for any")
	query := fs.String("query", "", `Datadog percentile query template, containing the literal "{P}" (e.g. "p{P}:trace.http.server.request.duration{service:billing}")`)
	window := fs.Int("window", 0, "query window in minutes (default: server's datadog.default_window_minutes, or 60)")
	path := fs.String("path", "/", "path prefix")
	enabled := fs.Bool("enabled", false, "arm this rule immediately (default: pulled disarmed)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *target == "" {
		fmt.Fprintln(stderr, "ensemble: latency from-datadog: --target is required")
		return 2
	}
	if *query == "" {
		fmt.Fprintln(stderr, "ensemble: latency from-datadog: --query is required")
		return 2
	}

	c := NewClient(*apiURL)
	res, err := c.LatencyFromDatadog(context.Background(), LatencyFromDatadogRequest{
		Target: *target, Query: *query, WindowMinutes: *window, Path: *path, Enabled: enabled,
	})
	if err != nil {
		fmt.Fprintf(stderr, "ensemble: latency from-datadog: %v\n", err)
		return 1
	}
	if *jsonOut {
		return printJSON(stdout, res)
	}

	var rule *proxy.LatencyRule
	for i := range res.Rules {
		if res.Rules[i].Target == *target && res.Rules[i].Path == *path {
			rule = &res.Rules[i]
			break
		}
	}
	if rule == nil {
		fmt.Fprintln(stderr, "ensemble: latency from-datadog: applied rule not found in response")
		return 1
	}
	fmt.Fprintf(stdout, "%s %s: p50=%s p95=%s p99=%s (source: datadog, last %sm)\n",
		rule.Target, rule.Path, formatLatencyMs(rule.P50), formatLatencyMs(rule.P95), formatLatencyMs(rule.P99), datadogWindowFromSource(rule.Source))
	return 0
}

// datadogWindowFromSource pulls the "last <N>m" window out of a rule's
// Source string (e.g. "datadog:p50:foo{bar} (last 60m)"), which always
// carries the window the server actually used, including its own default —
// cheaper than the CLI guessing that default itself.
func datadogWindowFromSource(source string) string {
	_, rest, ok := strings.Cut(source, "(last ")
	if !ok {
		return "?"
	}
	window, _, ok := strings.Cut(rest, "m)")
	if !ok {
		return "?"
	}
	return window
}

func cmdLatencyApply(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("latency apply", flag.ContinueOnError)
	fs.SetOutput(stderr)
	apiURL := fs.String("api-url", defaultAPIURL(), "ensemble control-plane API base URL")
	jsonOut := fs.Bool("json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: ensemble latency apply <profile> [flags]")
		return 2
	}
	profile := fs.Arg(0)

	c := NewClient(*apiURL)
	res, err := c.LatencyApply(context.Background(), profile)
	if err != nil {
		fmt.Fprintf(stderr, "ensemble: latency apply: %v\n", err)
		return 1
	}
	if *jsonOut {
		return printJSON(stdout, res)
	}

	failed := 0
	for _, r := range res.Results {
		if r.OK {
			if r.Source != "" {
				fmt.Fprintf(stdout, "%s %s: p50=%s p95=%s p99=%s (source: datadog)\n", r.Target, r.Path, formatLatencyMs(r.P50), formatLatencyMs(r.P95), formatLatencyMs(r.P99))
			} else {
				fmt.Fprintf(stdout, "%s %s: fixed=%s\n", r.Target, r.Path, formatLatencyMs(r.FixedMs))
			}
		} else {
			failed++
			fmt.Fprintf(stdout, "%s %s: ERROR: %s\n", r.Target, r.Path, r.Error)
		}
	}
	fmt.Fprintf(stdout, "%d applied, %d failed\n", len(res.Results)-failed, failed)
	if failed > 0 {
		return 1
	}
	return 0
}
