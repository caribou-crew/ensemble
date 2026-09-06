package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/caribou-crew/ensemble/core/trace"
)

// openHopHistory resumes this NDJSON log before the rotating writer counts
// its initial size. The generic rotating writer remains a raw byte sink.
func openHopHistory(path string, maxBytes int64, keep int) (*trace.RotatingFile, uint64, error) {
	seq, err := retainedHopSequence(path, keep)
	if err != nil {
		return nil, 0, err
	}
	if err := terminateHopHistoryLine(path); err != nil {
		return nil, 0, err
	}
	f, err := trace.OpenRotatingFile(path, maxBytes, keep)
	if err != nil {
		return nil, 0, fmt.Errorf("open hops log: %w", err)
	}
	return f, seq, nil
}

// terminateHopHistoryLine preserves every old byte while separating the
// next record from a crash-truncated tail or a complete final record that
// lacks only its newline. Do this before opening the rotating writer so
// its size accounting includes the added delimiter.
func terminateHopHistoryLine(path string) (err error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("prepare hop history %s: %w", path, err)
	}
	defer func() {
		if closeErr := f.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("close prepared hop history %s: %w", path, closeErr)
		}
	}()
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat hop history %s: %w", path, err)
	}
	if info.Size() == 0 {
		return nil
	}
	var last [1]byte
	if _, err := f.ReadAt(last[:], info.Size()-1); err != nil {
		return fmt.Errorf("read hop history tail %s: %w", path, err)
	}
	if last[0] != '\n' {
		if _, err := f.WriteString("\n"); err != nil {
			return fmt.Errorf("terminate hop history line %s: %w", path, err)
		}
	}
	return nil
}

// retainedHopSequence resumes the sequence namespace shared by the live ring
// and retained files. A stream may finalize after later seqs, including after
// a rotation, so neither the last line nor the current file alone is enough.
// Only retained generations are scanned; memory holds one decoded hop at a
// time. Historic duplicate seqs are left intact, but new runs never reuse them.
func retainedHopSequence(path string, keep int) (uint64, error) {
	var highest uint64
	for generation := 0; generation <= max(0, keep); generation++ {
		file := path
		if generation > 0 {
			file = fmt.Sprintf("%s.%d", path, generation)
		}
		seq, err := highestHopSequence(file)
		if err != nil {
			return 0, err
		}
		highest = max(highest, seq)
	}
	if highest == ^uint64(0) {
		return 0, fmt.Errorf("hop history %s exhausted the sequence range", path)
	}
	return highest, nil
}

func highestHopSequence(path string) (uint64, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read hop history %s: %w", path, err)
	}
	defer f.Close()
	reader := trace.NewReader(f)
	var highest uint64
	var previousError error
	for {
		hop, err := reader.Next()
		if errors.Is(err, trace.ErrEOF) {
			return highest, nil
		}
		if err != nil {
			// As in traffic history, malformed lines (including a crashed
			// writer's tail) can be skipped. Scanner/I/O failures repeat the
			// same stored error: they prevent finding any later maximum, so
			// refuse to start with a sequence we cannot safely resume.
			if err == previousError {
				return 0, fmt.Errorf("read hop history %s: %w", path, err)
			}
			previousError = err
			continue
		}
		previousError = nil
		highest = max(highest, hop.Seq)
	}
}
