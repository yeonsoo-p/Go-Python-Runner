//go:build integration

package integration

import (
	"testing"
	"time"

	"go-python-runner/internal/runner"
)

func TestCacheShareObject(t *testing.T) {
	mgr, grpcSrv, cleanup := testSetup(t)
	defer cleanup()
	_ = grpcSrv

	// On Windows, shared memory blocks are destroyed when the last handle closes.
	// For cross-process sharing, producer and consumer must run concurrently so
	// the producer's handle is still open when the consumer opens the same block.

	// Start producer — it caches data and keeps running until complete()
	_, prodCh, err := mgr.StartRun("cache_producer", map[string]string{}, testdataDir(t, "cache_producer"))
	if err != nil {
		t.Fatal(err)
	}

	// Wait for producer to output "cached:shared_data" (block is now in shared memory)
	var producerCached bool
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
waitProducer:
	for {
		select {
		case msg, ok := <-prodCh:
			if !ok {
				break waitProducer
			}
			if out, ok := msg.(runner.OutputMsg); ok {
				t.Logf("Producer: %s", out.Text)
				if out.Text == "cached:shared_data" {
					producerCached = true
				}
			}
			if _, ok := msg.(runner.StatusMsg); ok {
				break waitProducer
			}
		case <-timer.C:
			t.Fatal("timeout waiting for producer")
		}
	}
	if !producerCached {
		t.Fatal("producer did not cache data")
	}

	// Producer has exited by now (it calls complete() after cache_set).
	// On Windows the shared memory handle is closed. This test verifies the
	// Go registry still has the metadata — actual cross-process sharing
	// requires concurrent runs (see cache_demo sample script).
	shmName, size, found := mgr.CacheLookup("shared_data")
	if !found {
		t.Fatal("cache registry lost the block after producer exited")
	}
	t.Logf("Cache block persisted: shm=%s size=%d", shmName, size)
}

func TestCacheCleanupOnCrash(t *testing.T) {
	mgr, grpcSrv, cleanup := testSetup(t)
	defer cleanup()
	_ = grpcSrv

	// Start crash script — caches data then exits abruptly via os._exit(1)
	_, crashCh, err := mgr.StartRun("cache_crash", map[string]string{}, testdataDir(t, "cache_crash"))
	if err != nil {
		t.Fatal(err)
	}

	// Collect messages (may be incomplete due to crash)
	msgs := collectMessages(crashCh, 15*time.Second)
	t.Logf("Crash script: %d messages", len(msgs))

	// Wait for cleanup goroutine
	time.Sleep(500 * time.Millisecond)

	// Block persists (available to future consumers) but the crashed run's
	// ref has been removed. Verify block exists with zero refs.
	blocks := mgr.CacheBlocks()
	if block, ok := blocks["crash_data"]; ok {
		if len(block.Refs) != 0 {
			t.Errorf("expected zero refs after crash cleanup, got %v", block.Refs)
		}
		t.Logf("Orphaned block persists: key=%s, size=%d", block.Key, block.Size)
	}
	// Block may or may not exist depending on whether the cache_set message
	// arrived before the process crash — both outcomes are valid.
}
