package cluster

import (
	"context"
	"fmt"
	"sync"
	"time"

	"GoQuorum/internal/storage"
	"GoQuorum/internal/vclock"
)

const ttlSweepMaxPerRound = 1000 // rate-limit: at most 1000 deletes per sweep

// TTLSweeper scans storage for expired siblings and issues tombstone Deletes.
type TTLSweeper struct {
	storage     *storage.Storage
	coordinator *Coordinator
	interval    time.Duration

	stopCh chan struct{}
	wg     sync.WaitGroup
}

// NewTTLSweeper creates a TTLSweeper. interval is how often to run (e.g. 1 minute).
func NewTTLSweeper(store *storage.Storage, coord *Coordinator, interval time.Duration) *TTLSweeper {
	return &TTLSweeper{
		storage:     store,
		coordinator: coord,
		interval:    interval,
		stopCh:      make(chan struct{}),
	}
}

// Start begins background sweeping.
func (ts *TTLSweeper) Start() {
	ts.wg.Add(1)
	go func() {
		defer ts.wg.Done()
		ticker := time.NewTicker(ts.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ts.stopCh:
				return
			case <-ticker.C:
				ts.sweep()
			}
		}
	}()
}

// Stop stops the sweeper and waits for the current sweep to finish.
func (ts *TTLSweeper) Stop() {
	close(ts.stopCh)
	ts.wg.Wait()
}

func (ts *TTLSweeper) sweep() {
	now := time.Now().Unix()
	deleted := 0

	err := ts.storage.Scan(nil, nil, func(key []byte, ss *storage.SiblingSet) bool {
		if deleted >= ttlSweepMaxPerRound {
			return false // stop after limit
		}
		if ss == nil {
			return true
		}
		for _, sib := range ss.Siblings {
			if sib.ExpiresAt != 0 && now >= sib.ExpiresAt {
				// Build a causally-dominated vector clock to write the tombstone.
				vc := vclock.NewVectorClock()
				for _, s := range ss.Siblings {
					vc.Merge(s.VClock)
				}
				vc.Tick(ts.coordinator.nodeID)

				keyCopy := append([]byte(nil), key...)
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				if err := ts.coordinator.Delete(ctx, string(keyCopy), vc); err != nil {
					fmt.Printf("TTLSweeper: failed to delete expired key %x: %v\n", keyCopy, err)
				} else {
					deleted++
				}
				cancel()
				break // one delete per key per sweep
			}
		}
		return true
	})

	if err != nil {
		fmt.Printf("TTLSweeper: scan error: %v\n", err)
	}
}
