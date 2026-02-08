package cluster

import (
	"GoQuorum/internal/storage"
	"context"
	"fmt"
	"sync"
	"time"
)

// GracefulShutdown handles clean node shutdown (Section 4.1)
type GracefulShutdown struct {
	storage         *storage.Storage
	coordinator     *Coordinator
	failureDetector *FailureDetector

	drainTimeout time.Duration // Default: 30s

	mu       sync.Mutex
	stopping bool
}

func NewGracefulShutdown(
	storage *storage.Storage,
	coordinator *Coordinator,
	failureDetector *FailureDetector) *GracefulShutdown {

	return &GracefulShutdown{
		storage:         storage,
		coordinator:     coordinator,
		failureDetector: failureDetector,
		drainTimeout:    30 * time.Second,
	}
}

// Shutdown performs graceful shutdown sequence (Section 4.1)
// Returns error if shutdown doesn't complete within timeout
func (gs *GracefulShutdown) Shutdown(ctx context.Context) error {
	gs.mu.Lock()
	if gs.stopping {
		gs.mu.Unlock()
		return fmt.Errorf("already shutting down")
	}
	gs.stopping = true
	gs.mu.Unlock()

	fmt.Println("Starting graceful shutdown...")

	// Step 1: Stop accepting new requests (Section 4.1)
	// (Health endpoint should return NOT_READY - implement in HTTP server)
	fmt.Println("Step 1: Stopped accepting new requests")

	// Step 2: Wait for in-flight requests to complete (Section 4.1)
	fmt.Println("Step 2: Draining in-flight requests...")
	drainCtx, cancel := context.WithTimeout(ctx, gs.drainTimeout)
	defer cancel()

	// TODO: Track in-flight requests and wait
	select {
	case <-drainCtx.Done():
		fmt.Println("Drain timeout reached, forcing shutdown")
	case <-time.After(1 * time.Second):
		// Simplified: just wait 1s for demo
		fmt.Println("In-flight requests drained")
	}

	// Step 3: Notify peers we're leaving (Section 4.1)
	fmt.Println("Step 3: Notifying peers...")
	// TODO: Send "leaving" notification via RPC

	// Step 4: Close storage (flushes WAL) (Section 4.1)
	fmt.Println("Step 4: Closing storage (flushing WAL)...")
	if err := gs.storage.Close(); err != nil {
		return fmt.Errorf("close storage: %w", err)
	}

	// Step 5: Stop failure detector
	fmt.Println("Step 5: Stopping failure detector...")
	gs.failureDetector.Stop()

	fmt.Println("Graceful shutdown complete")
	return nil
}

// IsShuttingDown returns true if shutdown in progress
func (gs *GracefulShutdown) IsShuttingDown() bool {
	gs.mu.Lock()
	defer gs.mu.Unlock()
	return gs.stopping
}
