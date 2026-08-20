package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/caribou-crew/ensemble/core/proxy"
)

func cmdLatency(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: ensemble latency <list|set|reset|arm-all> [flags]")
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
