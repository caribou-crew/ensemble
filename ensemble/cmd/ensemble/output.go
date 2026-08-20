package main

import (
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/caribou-crew/ensemble/core/trace"
)

// printJSON writes v as indented JSON to w. Returns a process exit code
// (0, or 1 on an encode failure) so callers can `return printJSON(...)`.
func printJSON(w io.Writer, v any) int {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		fmt.Fprintf(w, "ensemble: encode JSON: %v\n", err)
		return 1
	}
	return 0
}

// newTabwriter returns a tabwriter configured the same way for every
// command's human-readable table output.
func newTabwriter(w io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
}

// printHopTable renders hops as a human-readable table.
func printHopTable(w io.Writer, hops []trace.Hop) {
	tw := newTabwriter(w)
	fmt.Fprintln(tw, "SEQ\tFROM\tTO\tMETHOD\tPATH\tSTATUS\tDONE(ms)")
	for _, h := range hops {
		from := h.From
		if from == "" {
			from = "-"
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%d\t%.1f\n", h.Seq, from, h.To, h.Method, h.Path, h.Status, h.T.DoneMs)
	}
	tw.Flush()
}

// printHopLine renders one hop as a single compact line — used by
// `traffic --follow`, where a repeated table header per hop would be
// noisy.
func printHopLine(w io.Writer, h trace.Hop) {
	from := h.From
	if from == "" {
		from = "-"
	}
	fmt.Fprintf(w, "%d  %s -> %s  %s %s  %d  %.1fms\n", h.Seq, from, h.To, h.Method, h.Path, h.Status, h.T.DoneMs)
}

// printLatencyRules renders a LatencyListResponse as JSON or a table.
func printLatencyRules(w io.Writer, jsonOut bool, res LatencyListResponse) int {
	if jsonOut {
		return printJSON(w, res)
	}
	tw := newTabwriter(w)
	fmt.Fprintln(tw, "TARGET\tPATH\tFIXED(ms)\tP50\tP95\tP99\tENABLED")
	for _, r := range res.Rules {
		fmt.Fprintf(tw, "%s\t%s\t%g\t%g\t%g\t%g\t%t\n", r.Target, r.Path, r.FixedMs, r.P50, r.P95, r.P99, r.Enabled)
	}
	tw.Flush()
	return 0
}
