//go:build !unix

package config

// lockOverlay is a no-op on platforms without flock(2).
//
// The Go standard library exposes no portable file lock: syscall.Flock does
// not exist on Windows, and the Win32 equivalent (LockFileEx) is reachable
// only through golang.org/x/sys/windows, which this module does not depend
// on and which global-constraints.md will not let a task add without a
// justification of its own. The alternative — an O_EXCL lockfile, which IS
// portable — is the mechanism this task deliberately rejected everywhere
// else: it is not released when its holder dies, so one Ctrl-C wedges every
// later append permanently.
//
// So on non-unix platforms AppendWireRule keeps exactly the guarantee it
// had before this task: overlayMu serializes appends within one process,
// the temp-file/rename keeps readers safe everywhere, and two WRITER
// processes can still silently lose an append. That is a real gap, stated
// here and in AppendWireRule's doc rather than papered over — the retrace
// binary does ship a windows build. It is not a regression; it is the
// unfixed half, and the fix needs either a dependency or a mechanism this
// task ruled out.
func lockOverlay(path string) (unlock func(), err error) {
	return func() {}, nil
}
