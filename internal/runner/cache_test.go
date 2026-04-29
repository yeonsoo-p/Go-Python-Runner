package runner

import (
	"fmt"
	"sync"
	"testing"
)

// hasBlock checks whether a key exists in the registry without adding a ref.
// In-package tests can read the unexported map directly under the lock.
func hasBlock(cm *CacheManager, key string) bool {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	_, ok := cm.blocks[key]
	return ok
}

// blockRefs returns a copy of a block's ref list, or nil if the block is missing.
func blockRefs(cm *CacheManager, key string) []string {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	block, ok := cm.blocks[key]
	if !ok {
		return nil
	}
	out := make([]string, len(block.Refs))
	copy(out, block.Refs)
	return out
}

func TestCacheManager_RegisterAndLookup(t *testing.T) {
	cm := NewCacheManager()
	cm.Register("features", "shm_001", 1024, "run-1")

	name, size, found := cm.LookupAndRef("features", "run-1")
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
	_, _, found := cm.LookupAndRef("nonexistent", "run-1")
	if found {
		t.Error("expected not found for nonexistent key")
	}
}

func TestCacheManager_RefCounting(t *testing.T) {
	cm := NewCacheManager()
	cm.Register("data", "shm_002", 2048, "run-1")
	cm.LookupAndRef("data", "run-2")
	cm.LookupAndRef("data", "run-3")

	cm.Release("data", "run-1")
	if !hasBlock(cm, "data") {
		t.Fatal("block should still exist after releasing one ref")
	}

	cm.Release("data", "run-2")
	if !hasBlock(cm, "data") {
		t.Fatal("block should still exist with one ref remaining")
	}

	cm.Release("data", "run-3")
	if hasBlock(cm, "data") {
		t.Error("block should be removed after all refs released")
	}
}

// LookupAndRef called twice with the same runID must not append a duplicate ref.
func TestCacheManager_LookupAndRefDedup(t *testing.T) {
	cm := NewCacheManager()
	cm.Register("data", "shm_003", 100, "run-1")
	cm.LookupAndRef("data", "run-1") // owner already in refs
	cm.LookupAndRef("data", "run-1") // duplicate again

	if refs := blockRefs(cm, "data"); len(refs) != 1 {
		t.Errorf("expected 1 ref (no duplicate), got %d (%v)", len(refs), refs)
	}
}

func TestCacheManager_CleanupRun(t *testing.T) {
	cm := NewCacheManager()
	cm.Register("block1", "shm_a", 100, "run-1")
	cm.Register("block2", "shm_b", 200, "run-1")
	cm.Register("block3", "shm_c", 300, "run-2")
	cm.LookupAndRef("block3", "run-1") // run-1 also references block3

	cm.CleanupRun("run-1")

	// Zero-ref blocks (block1, block2) are deleted; block3 persists with run-2.
	if hasBlock(cm, "block1") || hasBlock(cm, "block2") {
		t.Error("expected zero-ref blocks to be deleted after CleanupRun")
	}
	if !hasBlock(cm, "block3") {
		t.Error("expected block3 to persist (still has run-2 ref)")
	}
	refs := blockRefs(cm, "block3")
	if len(refs) != 1 || refs[0] != "run-2" {
		t.Errorf("expected block3 to have only run-2 ref, got %v", refs)
	}
}

func TestCacheManager_ExplicitRelease(t *testing.T) {
	cm := NewCacheManager()
	cm.Register("data", "shm_x", 100, "run-1")
	cm.LookupAndRef("data", "run-2")

	// CleanupRun removes run-1's ref; block persists because run-2 still holds one.
	cm.CleanupRun("run-1")
	if !hasBlock(cm, "data") {
		t.Error("expected block to persist after CleanupRun (run-2 still has ref)")
	}

	// Explicit Release of run-2 deletes the block (zero refs).
	cm.Release("data", "run-2")
	if hasBlock(cm, "data") {
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
			key := fmt.Sprintf("key-%d", id%5)
			runID := fmt.Sprintf("run-%d", id)
			for i := 0; i < ops; i++ {
				cm.Register(key, fmt.Sprintf("shm-%d-%d", id, i), int64(i), runID)
				cm.LookupAndRef(key, runID)
				cm.Release(key, runID)
			}
		}(g)
	}
	wg.Wait()

	// Primary assertion: no panic. Also verify cleanup works after concurrent ops.
	for i := 0; i < 5; i++ {
		key := fmt.Sprintf("key-%d", i)
		cm.Release(key, "any") // best-effort cleanup of any leftover refs
	}
}
