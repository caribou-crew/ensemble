#!/bin/sh
# Demo headers_from script for tools/readiness.yaml's ops-bff-healthy check
# — stands in for a real script that would mint or read a token from
# wherever the target service's own auth config lives. Each non-blank
# stdout line becomes one request header ("Header-Name: value").
echo "X-Ensemble-Readiness-Check: brew-sample"
