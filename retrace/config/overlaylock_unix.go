//go:build unix

package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// overlayLockWait bounds how long an append waits for another process to
// finish its read-modify-write. It is a BOUNDED wait on purpose: a command
// a developer typed must never hang forever behind a stuck peer, and an
// expiry that names the file is a repairable message where an unbounded
// block is not.
const overlayLockWait = 10 * time.Second

// overlayLockPoll is how often a waiter retries. flock(2) has a blocking
// mode, but using it would give up the deadline — LOCK_NB plus a poll is
// what makes the wait bounded.
const overlayLockPoll = 2 * time.Millisecond

// lockOverlay takes an exclusive CROSS-PROCESS lock on a sidecar file
// beside the overlay, held across the read, the merge and the rename.
//
// flock(2), not an O_EXCL lockfile, and the reason is crash-release, not
// convenience: the kernel drops a flock when the holding process exits for
// ANY reason, killed or not. An O_EXCL lockfile is not released when its
// holder dies — a `retrace ref rule` interrupted between create and unlink
// leaves a file that wedges every later append in every process,
// permanently, and the natural repair (a human deleting the lockfile by
// hand) puts two writers back. A crash that converts a transient race into
// a permanent denial is worse than the race it prevents: the race loses one
// rule, the wedge loses all of them.
//
// The sidecar is a separate file from the overlay because the overlay is
// REPLACED by rename on every append — a lock held on the overlay's own
// inode would be a lock on a file that is no longer the overlay by the time
// the next writer opens it.
func lockOverlay(path string) (unlock func(), err error) {
	lockPath := path + ".lock"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("opening the overlay lock %s: %w", lockPath, err)
	}
	deadline := time.Now().Add(overlayLockWait)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				// Closing the descriptor would release the lock on its own;
				// unlocking first makes the release explicit rather than a
				// side effect a later edit could remove.
				_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				_ = f.Close()
			}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			f.Close()
			return nil, fmt.Errorf("locking the overlay %s: %w", lockPath, err)
		}
		if time.Now().After(deadline) {
			f.Close()
			return nil, fmt.Errorf("timed out after %s waiting for another process to finish writing %s (lock: %s) — a `retrace ref rule` or a review server is holding it; retry, and if nothing else is running, report it rather than deleting the lock file",
				overlayLockWait, path, lockPath)
		}
		time.Sleep(overlayLockPoll)
	}
}
