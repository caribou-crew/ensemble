package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/caribou-crew/ensemble/ensemble/mcp"
)

// cmdMCP serves newline-delimited MCP JSON-RPC on stdin/stdout. Diagnostics
// go to stderr so stdout remains a valid stdio protocol stream.
func cmdMCP(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	fs.SetOutput(stderr)
	apiURL := fs.String("api-url", defaultAPIURL(), "ensemble control-plane API base URL")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "ensemble: mcp: unexpected arguments: %s\n", fs.Arg(0))
		return 2
	}
	if err := mcp.Serve(context.Background(), os.Stdin, stdout, *apiURL); err != nil {
		fmt.Fprintf(stderr, "ensemble: mcp: %v\n", err)
		return 1
	}
	return 0
}
