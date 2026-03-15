package cluster

import (
	"GoQuorum/internal/common"
	"GoQuorum/internal/config"
	"context"
	"fmt"
	"time"
)

// Bootstrap performs cluster bootstrap sequence (Section 5.1)
func Bootstrap(
	cfg config.ClusterConfig,
	membership *MembershipManager,
	failureDetector *FailureDetector) error {

	fmt.Println("=== Starting Cluster Bootstrap ===")

	// Step 1: Configuration already loaded and validated
	fmt.Printf("Node ID: %s\n", cfg.NodeID)
	fmt.Printf("Listen Address: %s\n", cfg.ListenAddr)
	fmt.Printf("Cluster Members: %d\n", len(cfg.Members))

	// Step 2: Local state already initialized in NewMembershipManager
	fmt.Println("Local state initialized: JOINING")

	// Step 3: gRPC server started externally (before bootstrap)

	// Step 4: Connect to peers (handled by failure detector start)
	fmt.Println("Connecting to peers...")
	peers := cfg.GetPeers()
	failureDetector.Start(peerIDsFromMembers(peers))

	// Step 5: Wait for quorum (Section 5.1)
	fmt.Printf("Waiting for quorum (%d/%d nodes)...\n",
		cfg.QuorumSize(), len(cfg.Members))

	ctx, cancel := context.WithTimeout(context.Background(), cfg.BootstrapTimeout)
	defer cancel()

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if membership.ActivateIfQuorum() {
				// Step 6: Transition to ACTIVE (Section 5.1) - done atomically inside ActivateIfQuorum
				fmt.Println("✓ Quorum reached - Bootstrap complete")
				fmt.Println("Node is now ACTIVE")
				return nil
			}

			// Show progress
			activeCount := len(membership.GetActivePeers()) + 1 // +1 for self
			fmt.Printf("  Active nodes: %d/%d\n", activeCount, cfg.QuorumSize())

		case <-ctx.Done():
			// Bootstrap timeout (Section 5.2)
			return fmt.Errorf("bootstrap timeout: cannot reach quorum after %s",
				cfg.BootstrapTimeout)
		}
	}
}

func peerIDsFromMembers(members []config.MemberConfig) []common.NodeID {
	ids := make([]common.NodeID, len(members))
	for i, m := range members {
		ids[i] = m.ID
	}
	return ids
}
