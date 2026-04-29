//go:build stress

package stress

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go-python-runner/internal/runner"
)

// TestCacheRefRaceCleanup runs LookupAndRef/Release loops alongside CleanupRun
// for the same runID, under -race. The race detector and the registry's own
// invariants (ref count never negative) are the assertion.
func TestCacheRefRaceCleanup(t *testing.T) {
	cm := runner.NewCacheManager()

	const ownerRun = "owner-run"
	const consumerRun = "consumer-run"
	const key = "shared-key"
	cm.Register(key, "shm-name", 4096, ownerRun)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	var lookupRefs atomic.Int64
	var releases atomic.Int64
	var cleanups atomic.Int64

	// Goroutine A: consumer does LookupAndRef + Release in a tight loop.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, _, found := cm.LookupAndRef(key, consumerRun); found {
				lookupRefs.Add(1)
				cm.Release(key, consumerRun)
				releases.Add(1)
			}
		}
	}()

	// Goroutine B: cleanup of the consumer's runID — idempotent, should not race.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			cm.CleanupRun(consumerRun)
			cleanups.Add(1)
		}
	}()

	time.Sleep(1 * time.Second)
	close(stop)
	wg.Wait()

	// The owner's reference is still held; the block must still exist.
	// Reuse Release to verify (Release returns true iff runID was a ref).
	if !cm.Release(key, ownerRun) {
		t.Errorf("owner ref unexpectedly removed during the race")
	}

	t.Logf("lookups+ref=%d releases=%d cleanups=%d",
		lookupRefs.Load(), releases.Load(), cleanups.Load())
}

// TestConcurrentReleaseSameKey: many goroutines call Release for the same
// (key, runID) simultaneously. The block should be deleted at most once;
// Release should never produce a panic or negative ref count.
func TestConcurrentReleaseSameKey(t *testing.T) {
	cm := runner.NewCacheManager()
	const owner = "owner"
	const key = "k"
	cm.Register(key, "shm-x", 1024, owner)

	const N = 64
	var foundCount atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if cm.Release(key, owner) {
				foundCount.Add(1)
			}
		}()
	}
	wg.Wait()

	if got := foundCount.Load(); got != 1 {
		t.Errorf("expected exactly 1 successful release, got %d", got)
	}
	// Block should be gone — Release should now return false for any runID.
	if cm.Release(key, owner) {
		t.Errorf("Release returned true for already-released block")
	}
}

// TestRegisterReleaseChurn drives Register + Release at high frequency across
// many keys and many goroutines under -race.
func TestRegisterReleaseChurn(t *testing.T) {
	cm := runner.NewCacheManager()

	const goroutines = 32
	const keysPerGoroutine = 100

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			runID := fmt.Sprintf("run-%d", gid)
			for k := 0; k < keysPerGoroutine; k++ {
				key := fmt.Sprintf("key-%d-%d", gid, k)
				cm.Register(key, fmt.Sprintf("shm-%d-%d", gid, k), 64, runID)
				if _, _, found := cm.LookupAndRef(key, runID); !found {
					t.Errorf("just-registered key %s not found", key)
				}
				cm.Release(key, runID) // drop the second ref we just added
				cm.Release(key, runID) // drop the owner ref → block should be deleted
			}
		}(g)
	}
	wg.Wait()
}
