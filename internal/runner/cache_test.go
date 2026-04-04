package runner

import (
	"fmt"
	"sync"
	"testing"
)

func TestCacheManager_RegisterAndLookup(t *testing.T) {
	cm := NewCacheManager()
	cm.Register("features", "shm_001", 1024, "run-1")

	name, size, found := cm.Lookup("features")
	if !found {
		t.Fatal("expected to find 'features' block")
	}
	if name != "shm_001" {
		t.Errorf("expected shm name 'shm_001', got %q", name)
	}
	if size != 1024 {
		t.Errorf("expected size 1024, got %d", size)
	}
}

func TestCacheManager_LookupMissing(t *testing.T) {
	cm := NewCacheManager()
	_, _, found := cm.Lookup("nonexistent")
	if found {
		t.Error("expected not found for nonexistent key")
	}
}

func TestCacheManager_RefCounting(t *testing.T) {
	cm := NewCacheManager()
	cm.Register("data", "shm_002", 2048, "run-1")
	cm.AddRef("data", "run-2")
	cm.AddRef("data", "run-3")

	// Release run-1, block should still exist
	cm.Release("data", "run-1")
	_, _, found := cm.Lookup("data")
	if !found {
		t.Fatal("block should still exist after releasing one ref")
	}

	// Release run-2, block should still exist
	cm.Release("data", "run-2")
	_, _, found = cm.Lookup("data")
	if !found {
		t.Fatal("block should still exist with one ref remaining")
	}

	// Release run-3, block should be removed
	cm.Release("data", "run-3")
	_, _, found = cm.Lookup("data")
	if found {
		t.Error("block should be removed after all refs released")
	}
}

func TestCacheManager_DuplicateRef(t *testing.T) {
	cm := NewCacheManager()
	cm.Register("data", "shm_003", 100, "run-1")
	cm.AddRef("data", "run-1") // duplicate, should not add

	blocks := cm.Blocks()
	if len(blocks["data"].Refs) != 1 {
		t.Errorf("expected 1 ref (no duplicate), got %d", len(blocks["data"].Refs))
	}
}

func TestCacheManager_CleanupRun(t *testing.T) {
	cm := NewCacheManager()
	cm.Register("block1", "shm_a", 100, "run-1")
	cm.Register("block2", "shm_b", 200, "run-1")
	cm.Register("block3", "shm_c", 300, "run-2")
	cm.AddRef("block3", "run-1") // run-1 also references block3

	cm.CleanupRun("run-1")

	// Zero-ref blocks (block1, block2) should be deleted.
	// On Windows, shared memory is reclaimed when the last handle closes,
	// so keeping zero-ref blocks would leave stale registry entries.
	_, _, found1 := cm.Lookup("block1")
	_, _, found2 := cm.Lookup("block2")
	_, _, found3 := cm.Lookup("block3")
	if found1 || found2 {
		t.Error("expected zero-ref blocks to be deleted after CleanupRun")
	}
	if !found3 {
		t.Error("expected block3 to persist (still has run-2 ref)")
	}

	// block3 should have run-2's ref remaining
	blocks := cm.Blocks()
	if len(blocks["block3"].Refs) != 1 || blocks["block3"].Refs[0] != "run-2" {
		t.Errorf("expected block3 to have only run-2 ref, got %v", blocks["block3"].Refs)
	}
}

func TestCacheManager_ExplicitRelease(t *testing.T) {
	cm := NewCacheManager()
	cm.Register("data", "shm_x", 100, "run-1")
	cm.AddRef("data", "run-2")

	// CleanupRun removes run-1's ref; block persists because run-2 still holds a ref
	cm.CleanupRun("run-1")
	_, _, found := cm.Lookup("data")
	if !found {
		t.Error("expected block to persist after CleanupRun (run-2 still has ref)")
	}

	// Explicit Release of run-2 deletes the block (zero refs)
	cm.Release("data", "run-2")
	_, _, found = cm.Lookup("data")
	if found {
		t.Error("expected block to be deleted after explicit Release with zero refs")
	}
}

func TestCacheManager_ConcurrentAccess(t *testing.T) {
	cm := NewCacheManager()
	const goroutines = 20
	const ops = 100

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			key := fmt.Sprintf("key-%d", id%5) // 5 shared keys
			runID := fmt.Sprintf("run-%d", id)
			for i := 0; i < ops; i++ {
				cm.Register(key, fmt.Sprintf("shm-%d-%d", id, i), int64(i), runID)
				cm.Lookup(key)
				cm.AddRef(key, runID)
				cm.Release(key, runID)
			}
		}(g)
	}
	wg.Wait()

	// Should not panic — that's the primary assertion.
	// Also verify cleanup works after concurrent operations.
	for i := 0; i < 5; i++ {
		key := fmt.Sprintf("key-%d", i)
		cm.Release(key, "any") // clean up remaining
	}
}
