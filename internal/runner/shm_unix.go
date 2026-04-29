//go:build !windows

package runner

import "os"

// unlinkShm removes the OS-level POSIX shared memory entry for `name`.
// On Linux/macOS, multiprocessing.shared_memory.SharedMemory is backed by a
// file at /dev/shm/<name>; removing it is equivalent to shm_unlink. Existing
// open handles continue to work until they close (POSIX semantics), but the
// name is freed so a future SharedMemory(create=True, name=...) can reuse it
// and the block is reclaimed once the last handle closes.
//
// Errors are ignored: ENOENT means the block was already cleaned up (atexit
// in the producer Python ran successfully), which is the happy path.
func unlinkShm(name string) {
	if name == "" {
		return
	}
	_ = os.Remove("/dev/shm/" + name)
}
