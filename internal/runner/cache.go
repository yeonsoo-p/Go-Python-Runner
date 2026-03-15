package runner

import (
	"sync"
)

// CacheBlock represents a shared memory block tracked by the cache manager.
type CacheBlock struct {
	Key      string
	ShmName  string
	Size     int64
	OwnerRun string   // runID that created it
	Refs     []string // runIDs currently referencing it
}

// CacheManager tracks shared memory blocks used by Python scripts.
// Go manages the registry and lifecycle; Python scripts access the data directly.
type CacheManager struct {
	mu     sync.RWMutex
	blocks map[string]*CacheBlock
}

// NewCacheManager creates a new cache manager.
func NewCacheManager() *CacheManager {
	return &CacheManager{
		blocks: make(map[string]*CacheBlock),
	}
}

// Register adds or updates a cache block in the registry.
func (cm *CacheManager) Register(key, shmName string, size int64, ownerRunID string) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.blocks[key] = &CacheBlock{
		Key:      key,
		ShmName:  shmName,
		Size:     size,
		OwnerRun: ownerRunID,
		Refs:     []string{ownerRunID},
	}
}

// Lookup returns the shared memory name and size for a given key.
func (cm *CacheManager) Lookup(key string) (shmName string, size int64, found bool) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	block, ok := cm.blocks[key]
	if !ok {
		return "", 0, false
	}
	return block.ShmName, block.Size, true
}

// AddRef adds a run ID reference to a cache block.
func (cm *CacheManager) AddRef(key, runID string) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	block, ok := cm.blocks[key]
	if !ok {
		return
	}
	// Don't add duplicate refs
	for _, ref := range block.Refs {
		if ref == runID {
			return
		}
	}
	block.Refs = append(block.Refs, runID)
}

// Release removes a run ID reference from a cache block.
// If no references remain, the block is removed from the registry.
func (cm *CacheManager) Release(key, runID string) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	block, ok := cm.blocks[key]
	if !ok {
		return
	}
	// Remove the runID from refs
	for i, ref := range block.Refs {
		if ref == runID {
			block.Refs = append(block.Refs[:i], block.Refs[i+1:]...)
			break
		}
	}
	// If no refs remain, remove the block
	// On Windows, shared memory is auto-cleaned when all handles close.
	if len(block.Refs) == 0 {
		delete(cm.blocks, key)
	}
}

// CleanupRun removes a terminated run's references from all cache blocks.
// Unlike Release(), blocks are NOT auto-deleted when refs reach zero here —
// they persist for future consumers. This distinction is intentional:
// Release() is an explicit user action ("I'm done with this"), while
// CleanupRun() is automatic cleanup ("this run ended, but others may still need the data").
// Blocks are only deleted via explicit Release or app shutdown.
func (cm *CacheManager) CleanupRun(runID string) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	for _, block := range cm.blocks {
		for i, ref := range block.Refs {
			if ref == runID {
				block.Refs = append(block.Refs[:i], block.Refs[i+1:]...)
				break
			}
		}
	}
}

// Blocks returns a snapshot of all cache blocks (for diagnostics).
func (cm *CacheManager) Blocks() map[string]CacheBlock {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	result := make(map[string]CacheBlock, len(cm.blocks))
	for k, v := range cm.blocks {
		result[k] = *v
	}
	return result
}
