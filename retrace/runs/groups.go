package runs

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// GroupRecord is one flow-part marker as an adapter writes it. The writer is
// deliberately stateless — an `end` record carries no name, because a CLI or
// HTTP marker door is a fresh caller that cannot know what is open. Every
// sequencing rule lives in DeriveGroups instead.
type GroupRecord struct {
	Phase string    `json:"phase"` // "start" | "end"
	Name  string    `json:"name,omitempty"`
	TS    time.Time `json:"ts"`
	Quiet bool      `json:"quiet,omitempty"` // declared silence: suppresses gap suspicion
}

// Group is a derived half-open interval [StartedAt, EndedAt).
type Group struct {
	Name      string    `json:"name"`
	StartedAt time.Time `json:"startedAt"`
	EndedAt   time.Time `json:"endedAt"`
	Quiet     bool      `json:"quiet,omitempty"`
}

func AppendGroupRecord(runDir string, r GroupRecord) error {
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(runDir, "groups.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(b, '\n'))
	return err
}

// ReadGroupRecords tolerates corrupt lines: a half-written marker from a
// killed test process must not make the whole run unreadable.
func ReadGroupRecords(runDir string) ([]GroupRecord, error) {
	f, err := os.Open(filepath.Join(runDir, "groups.jsonl"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []GroupRecord
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for s.Scan() {
		line := s.Bytes()
		if len(line) == 0 {
			continue
		}
		var r GroupRecord
		if err := json.Unmarshal(line, &r); err != nil {
			continue
		}
		out = append(out, r)
	}
	return out, s.Err()
}

// DeriveGroups folds markers into intervals in start order. A name may
// repeat. An unclosed group closes when the next one opens, or at
// finishedAt — a marker placed after the traffic it meant to bracket then
// shows as an empty part, which is exactly the symptom worth seeing.
func DeriveGroups(records []GroupRecord, finishedAt time.Time) []Group {
	sorted := append([]GroupRecord(nil), records...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].TS.Before(sorted[j].TS) })

	var out []Group
	var open *Group
	closeAt := func(ts time.Time) {
		if open != nil {
			open.EndedAt = ts
			out = append(out, *open)
			open = nil
		}
	}
	for _, r := range sorted {
		switch r.Phase {
		case "start":
			closeAt(r.TS)
			open = &Group{Name: r.Name, StartedAt: r.TS, Quiet: r.Quiet}
		case "end":
			closeAt(r.TS)
		}
	}
	closeAt(finishedAt)
	return out
}

// GroupAt returns the part a timestamp falls in, "" for none. Half-open, so
// a call made at the instant a part opens belongs to that part.
func GroupAt(groups []Group, ts time.Time) string {
	for _, g := range groups {
		if !ts.Before(g.StartedAt) && ts.Before(g.EndedAt) {
			return g.Name
		}
	}
	return ""
}

// GroupNames lists distinct part names in first-seen order.
func GroupNames(groups []Group) []string {
	seen := map[string]bool{}
	var out []string
	for _, g := range groups {
		if !seen[g.Name] {
			seen[g.Name] = true
			out = append(out, g.Name)
		}
	}
	return out
}
