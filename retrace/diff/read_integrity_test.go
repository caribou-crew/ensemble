package diff

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caribou-crew/ensemble/core/trace"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

func TestBuildRejectsDamagedRecordedTraffic(t *testing.T) {
	for _, plane := range []string{"wire", "hops"} {
		for _, side := range []string{"a", "b"} {
			for _, damage := range []string{"corrupt line", "wrong schema", "missing", "empty", "shortened"} {
				t.Run(plane+"/"+side+"/"+damage, func(t *testing.T) {
					hops := []trace.Hop{{Seq: 1, Method: "GET", Path: "/item", Status: 200}, {Seq: 2, Method: "GET", Path: "/other", Status: 200}}
					a := RunRef{Kind: "run", RunID: "a", Dir: t.TempDir(), Manifest: manifest("a", nil, nil, okCapture())}
					b := RunRef{Kind: "run", RunID: "b", Dir: t.TempDir(), Manifest: manifest("b", nil, nil, okCapture())}
					for _, ref := range []*RunRef{&a, &b} {
						writeWireFile(t, ref.Dir, hops)
						ref.Manifest.Wire.Calls = len(hops)
						if plane == "hops" {
							writeChainFile(t, ref.Dir, hops)
							ref.Manifest.Hops = &runs.Counts{Recorded: true, Calls: len(hops)}
						}
					}
					dir := a.Dir
					if side == "b" {
						dir = b.Dir
					}
					path := filepath.Join(dir, plane+".jsonl")
					switch damage {
					case "missing":
						if err := os.Remove(path); err != nil {
							t.Fatal(err)
						}
					case "empty":
						if err := os.WriteFile(path, nil, 0o600); err != nil {
							t.Fatal(err)
						}
					case "shortened":
						writeHopFile(t, path, hops[:1])
					default:
						data, err := os.ReadFile(path)
						if err != nil {
							t.Fatal(err)
						}
						bad := "{broken\n"
						if damage == "wrong schema" {
							bad = "{}\n"
						}
						if err := os.WriteFile(path, append(data, bad...), 0o600); err != nil {
							t.Fatal(err)
						}
					}
					// --allow-degraded cannot reconstruct bytes lost after capture.
					for _, allow := range []bool{false, true} {
						s, err := Build(BuildInput{App: "app", Flow: "flow", A: a, B: b, Cfg: baseConfig(t), AllowDegraded: allow})
						if err == nil || !strings.Contains(err.Error(), path) {
							t.Fatalf("damaged recording must fail naming %s (allow-degraded=%v): verdict=%s err=%v", path, allow, s.Verdict, err)
						}
					}
				})
			}
		}
	}
}
