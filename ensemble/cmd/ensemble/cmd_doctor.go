package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	serverapi "github.com/caribou-crew/ensemble/ensemble/server"
)

const doctorCLIMaxTimeout = 30 * time.Second

const (
	doctorCLITargetLimit = 128
	doctorCLIPathLimit   = 2048
	doctorCLIExpectLimit = 64
)

func (c *Client) Doctor(ctx context.Context, req serverapi.DoctorRequest) (serverapi.DoctorResponse, error) {
	var out serverapi.DoctorResponse
	err := c.do(ctx, http.MethodPost, "/api/doctor", req, &out)
	return out, err
}

func cmdDoctor(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	apiURL := fs.String("api-url", defaultAPIURL(), "ensemble control-plane API base URL")
	target := fs.String("target", "", "configured service or gateway to probe")
	path := fs.String("path", "", "origin-form GET path to probe")
	expect := fs.String("expect", "", "comma-separated configured targets expected on this path")
	timeout := fs.Duration("timeout", 5*time.Second, "maximum probe duration")
	jsonOut := fs.Bool("json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "ensemble: doctor: unexpected positional arguments")
		return 2
	}
	if strings.TrimSpace(*target) == "" {
		fmt.Fprintln(stderr, "ensemble: doctor: --target is required")
		return 2
	}
	if len(*target) > doctorCLITargetLimit {
		fmt.Fprintf(stderr, "ensemble: doctor: --target must not exceed %d bytes\n", doctorCLITargetLimit)
		return 2
	}
	if len(*path) > doctorCLIPathLimit {
		fmt.Fprintf(stderr, "ensemble: doctor: --path must not exceed %d bytes\n", doctorCLIPathLimit)
		return 2
	}
	if err := validateDoctorCLIPath(*path); err != nil {
		fmt.Fprintf(stderr, "ensemble: doctor: %v\n", err)
		return 2
	}
	if *timeout <= 0 || *timeout > doctorCLIMaxTimeout || *timeout < time.Millisecond {
		fmt.Fprintln(stderr, "ensemble: doctor: --timeout must be between 1ms and 30s")
		return 2
	}
	expected, err := parseDoctorExpect(*expect)
	if err != nil {
		fmt.Fprintf(stderr, "ensemble: doctor: %v\n", err)
		return 2
	}

	req := serverapi.DoctorRequest{
		Target: *target, Path: *path, Expect: expected, TimeoutMs: int(timeout.Milliseconds()),
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout+time.Second)
	defer cancel()
	result, err := NewClient(*apiURL).Doctor(ctx, req)
	if err != nil {
		fmt.Fprintf(stderr, "ensemble: doctor: %v\n", err)
		return 2
	}

	if *jsonOut {
		if code := printJSON(stdout, result); code != 0 {
			return 2
		}
	} else {
		printDoctor(stdout, result)
	}
	switch result.Verdict {
	case serverapi.DoctorPass:
		return 0
	case serverapi.DoctorFail, serverapi.DoctorInconclusive:
		return 1
	default:
		fmt.Fprintf(stderr, "ensemble: doctor: API returned unknown verdict %q\n", result.Verdict)
		return 2
	}
}

func validateDoctorCLIPath(path string) error {
	if path == "" {
		return errors.New("--path is required")
	}
	if strings.Contains(path, "#") || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return errors.New("--path must be an origin-form request target beginning with /")
	}
	u, err := url.ParseRequestURI(path)
	if err != nil || u.IsAbs() || u.Host != "" || u.Fragment != "" {
		return errors.New("--path must be a valid origin-form request target")
	}
	return nil
}

func parseDoctorExpect(raw string) ([]string, error) {
	if raw == "" {
		return nil, nil
	}
	seen := map[string]bool{}
	out := []string{}
	for _, part := range strings.Split(raw, ",") {
		name := strings.TrimSpace(part)
		if name == "" {
			return nil, errors.New("--expect must be a comma-separated list without empty names")
		}
		if len(name) > doctorCLITargetLimit {
			return nil, fmt.Errorf("--expect names must not exceed %d bytes", doctorCLITargetLimit)
		}
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	if len(out) > doctorCLIExpectLimit {
		return nil, fmt.Errorf("--expect must not exceed %d target names", doctorCLIExpectLimit)
	}
	return out, nil
}

func printDoctor(w io.Writer, result serverapi.DoctorResponse) {
	fmt.Fprintf(w, "DOCTOR\t%s\n", strings.ToUpper(string(result.Verdict)))
	tw := newTabwriter(w)
	fmt.Fprintln(tw, "TARGET\tPATH\tHTTP\tDURATION(ms)\tTRACE\tSESSION")
	fmt.Fprintf(tw, "%s\t%s\t%d\t%.1f\t%s\t%s\n", result.Target, result.Path, result.HTTPStatus, result.DurationMs, result.TraceID, result.SessionID)
	tw.Flush()
	if len(result.Hops) > 0 {
		hops := newTabwriter(w)
		fmt.Fprintln(hops, "SEQ\tFROM\tTO\tMETHOD\tPATH\tSTATUS\tDURATION(ms)")
		for _, h := range result.Hops {
			from := h.From
			if from == "" {
				from = "-"
			}
			fmt.Fprintf(hops, "%d\t%s\t%s\t%s\t%s\t%d\t%.1f\n", h.Seq, from, h.To, h.Method, h.Path, h.Status, h.DurationMs)
		}
		hops.Flush()
	}
	for _, reason := range result.Reasons {
		fmt.Fprintf(w, "reason: %s\n", reason)
	}
	for _, limitation := range result.Limitations {
		fmt.Fprintf(w, "limit: %s\n", limitation)
	}
}
