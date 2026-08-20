package trace

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// hopLogPerm is the mode a rotating hop log and its rotated generations are
// created with: owner-only. The file is a verbatim capture of every request
// and response through the stack — whatever the Redactor didn't know to
// scrub is sitting in it in cleartext.
const hopLogPerm os.FileMode = 0o600

// RotatingFile is an io.Writer over a file that is rolled over once it
// passes a size limit, keeping a bounded number of previous generations
// (path.1, path.2, ... path.N, newest first) and deleting the rest.
//
// Without this the hop log is an unbounded append: a stack under test
// overnight, or one service in a retry loop, fills the developer's disk.
// Rotation is by size rather than time because the thing being bounded is
// bytes on disk, and hop volume has no relationship to wall-clock.
type RotatingFile struct {
	path     string
	maxBytes int64
	keep     int

	mu   sync.Mutex
	f    *os.File
	size int64
}

// OpenRotatingFile opens (creating or appending to) path as a rotating
// writer. maxBytes <= 0 disables rotation, making this a plain append-only
// file. keep is how many rotated generations to retain; keep <= 0 discards
// the old content on each rollover instead of renaming it aside.
//
// An existing file is appended to and its current size counted, so a
// restart doesn't reset the budget.
func OpenRotatingFile(path string, maxBytes int64, keep int) (*RotatingFile, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("trace: rotating log: create dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, hopLogPerm)
	if err != nil {
		return nil, fmt.Errorf("trace: rotating log: open %s: %w", path, err)
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("trace: rotating log: stat %s: %w", path, err)
	}
	return &RotatingFile{path: path, maxBytes: maxBytes, keep: keep, f: f, size: fi.Size()}, nil
}

// Write appends b, rotating first if b would push the file past maxBytes.
// A single write larger than maxBytes is never split — the limit bounds
// the file at "one record over", not mid-record, since half an NDJSON line
// is unparseable.
func (r *RotatingFile) Write(b []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.f == nil {
		return 0, os.ErrClosed
	}
	if r.maxBytes > 0 && r.size > 0 && r.size+int64(len(b)) > r.maxBytes {
		if err := r.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := r.f.Write(b)
	r.size += int64(n)
	return n, err
}

// rotate closes the current file, shifts the retained generations down one
// (path.1 -> path.2, ...), moves the current file to path.1, and opens a
// fresh one. Callers hold r.mu.
func (r *RotatingFile) rotate() error {
	if err := r.f.Close(); err != nil {
		return fmt.Errorf("trace: rotating log: close %s: %w", r.path, err)
	}
	r.f = nil

	if r.keep <= 0 {
		// No generations kept: the next open truncates rather than appends.
		if err := os.Remove(r.path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("trace: rotating log: remove %s: %w", r.path, err)
		}
	} else {
		// Oldest first, so each rename lands on a free (or about-to-be
		// overwritten) name.
		oldest := r.generation(r.keep)
		if err := os.Remove(oldest); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("trace: rotating log: remove %s: %w", oldest, err)
		}
		for i := r.keep - 1; i >= 1; i-- {
			from, to := r.generation(i), r.generation(i+1)
			if err := os.Rename(from, to); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("trace: rotating log: rename %s: %w", from, err)
			}
		}
		if err := os.Rename(r.path, r.generation(1)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("trace: rotating log: rename %s: %w", r.path, err)
		}
	}

	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, hopLogPerm)
	if err != nil {
		return fmt.Errorf("trace: rotating log: reopen %s: %w", r.path, err)
	}
	r.f = f
	r.size = 0
	return nil
}

// generation names the nth rotated file ("hops.jsonl.1" for n=1).
func (r *RotatingFile) generation(n int) string {
	return fmt.Sprintf("%s.%d", r.path, n)
}

// Close closes the underlying file. Subsequent writes return
// os.ErrClosed rather than panicking; Close is idempotent.
func (r *RotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}
