package runner

import (
	"sync"
)

type CacheBlock struct {
	Key      string
	ShmName  string
	Size     int64
	OwnerRun string
	Refs     []string
}

// CacheManager tracks shared-memory blocks used by Python scripts. Go owns
// the registry and lifecycle; Python opens the blocks directly.
type CacheManager struct {
	mu     sync.RWMutex
	blocks map[string]*CacheBlock
}

func NewCacheManager() *CacheManager {
	return &CacheManager{
		blocks: make(map[string]*CacheBlock),
	}
}

// Register adds a new block to the registry. Returns false if the key is
// already registered — overwriting would orphan the prior block (its
// ShmName is lost from the map, so CleanupRun can no longer unlink it) and
// silently drop every other run's ref. The caller (handleCacheCreate) is
// expected to surface the rejection back to the producing script so it can
// release its just-created shm and stop assuming the data is shared.
func (cm *CacheManager) Register(key, shmName string, size int64, ownerRunID string) bool {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	if _, exists := cm.blocks[key]; exists {
		return false
	}
	cm.blocks[key] = &CacheBlock{
		Key:      key,
		ShmName:  shmName,
		Size:     size,
		OwnerRun: ownerRunID,
		Refs:     []string{ownerRunID},
	}
	return true
}

func (cm *CacheManager) LookupAndRef(key, runID string) (shmName string, size int64, found bool) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	block, ok := cm.blocks[key]
	if !ok {
		return "", 0, false
	}
	for _, ref := range block.Refs {
		if ref == runID {
			return block.ShmName, block.Size, true
		}
	}
	block.Refs = append(block.Refs, runID)
	return block.ShmName, block.Size, true
}

// Release drops runID from the block's refs. When refs hit zero the block
// is removed from the registry and unlinkShm runs (no-op on Windows; the OS
// reclaims pagefile-backed shm when the last handle closes). Returns true
// if runID was actually referencing the block.
func (cm *CacheManager) Release(key, runID string) bool {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	block, ok := cm.blocks[key]
	if !ok {
		return false
	}
	found := false
	for i, ref := range block.Refs {
		if ref == runID {
			block.Refs = append(block.Refs[:i], block.Refs[i+1:]...)
			found = true
			break
		}
	}
	if len(block.Refs) == 0 {
		unlinkShm(block.ShmName)
		delete(cm.blocks, key)
	}
	return found
}

// CleanupRun is the authoritative cache-lifecycle hook. Manager.waitForExit
// calls it for every terminal status — graceful exit, crash, SIGKILL, cancel
// — so blocks are reclaimed even when Python's atexit handler couldn't run
// (os._exit / SIGKILL). Idempotent.
func (cm *CacheManager) CleanupRun(runID string) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	for key, block := range cm.blocks {
		for i, ref := range block.Refs {
			if ref == runID {
				block.Refs = append(block.Refs[:i], block.Refs[i+1:]...)
				break
			}
		}
		if len(block.Refs) == 0 {
			unlinkShm(block.ShmName)
			delete(cm.blocks, key)
		}
	}
}

