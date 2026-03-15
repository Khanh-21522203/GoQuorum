package cluster

import (
	"testing"

	"GoQuorum/internal/common"
	"GoQuorum/internal/config"
)

func testMembership(t *testing.T, nodeID string, peers []string) *MembershipManager {
	t.Helper()

	members := []config.MemberConfig{
		{ID: common.NodeID(nodeID), Addr: nodeID + ":7070", HTTPAddr: nodeID + ":8080"},
	}
	for _, p := range peers {
		members = append(members, config.MemberConfig{
			ID:       common.NodeID(p),
			Addr:     p + ":7070",
			HTTPAddr: p + ":8080",
		})
	}

	cfg := config.ClusterConfig{
		NodeID:           common.NodeID(nodeID),
		Members:          members,
		FailureThreshold: 3,
	}
	return NewMembershipManager(cfg, "test")
}

// ── Initial status is Joining ────────────────────────────────────────────────

func TestMembershipInitialStatus(t *testing.T) {
	mm := testMembership(t, "n1", []string{"n2"})
	if mm.GetLocalStatus() != NodeStatusJoining {
		t.Errorf("expected Joining, got %s", mm.GetLocalStatus())
	}
}

// ── Peer starts Unknown ──────────────────────────────────────────────────────

func TestMembershipPeerInitialUnknown(t *testing.T) {
	mm := testMembership(t, "n1", []string{"n2"})
	if mm.GetPeerStatus("n2") != NodeStatusUnknown {
		t.Errorf("expected Unknown, got %s", mm.GetPeerStatus("n2"))
	}
}

// ── RecordHeartbeatSuccess transitions Unknown → Active ─────────────────────

func TestMembershipHeartbeatSuccessActivates(t *testing.T) {
	mm := testMembership(t, "n1", []string{"n2"})
	mm.RecordHeartbeatSuccess("n2", 0)
	if mm.GetPeerStatus("n2") != NodeStatusActive {
		t.Errorf("expected Active after heartbeat success, got %s", mm.GetPeerStatus("n2"))
	}
}

// ── Repeated heartbeat failures → Suspect → Failed ──────────────────────────

func TestMembershipHeartbeatFailuresToFailed(t *testing.T) {
	mm := testMembership(t, "n1", []string{"n2"})
	// First activate n2
	mm.RecordHeartbeatSuccess("n2", 0)

	// Then fail 3 times (failure threshold = 3)
	for i := 0; i < 3; i++ {
		mm.RecordHeartbeatFailure("n2")
	}

	if mm.GetPeerStatus("n2") != NodeStatusFailed {
		t.Errorf("expected Failed after 3 missed heartbeats, got %s", mm.GetPeerStatus("n2"))
	}
}

func TestMembershipSuspectAfterSomeFailures(t *testing.T) {
	mm := testMembership(t, "n1", []string{"n2"})
	mm.RecordHeartbeatSuccess("n2", 0)

	// 1 failure → Suspect (below threshold)
	mm.RecordHeartbeatFailure("n2")
	if mm.GetPeerStatus("n2") != NodeStatusSuspect {
		t.Errorf("expected Suspect after 1 failure, got %s", mm.GetPeerStatus("n2"))
	}
}

// ── RecordHeartbeatSuccess recovers a Failed node ───────────────────────────

func TestMembershipRecoveryFromFailed(t *testing.T) {
	mm := testMembership(t, "n1", []string{"n2"})
	mm.RecordHeartbeatSuccess("n2", 0)
	for i := 0; i < 3; i++ {
		mm.RecordHeartbeatFailure("n2")
	}
	// Recover
	mm.RecordHeartbeatSuccess("n2", 0)
	if mm.GetPeerStatus("n2") != NodeStatusActive {
		t.Errorf("expected Active after recovery, got %s", mm.GetPeerStatus("n2"))
	}
}

// ── UpdatePeerStatus ─────────────────────────────────────────────────────────

func TestMembershipUpdatePeerStatus(t *testing.T) {
	mm := testMembership(t, "n1", []string{"n2"})
	mm.UpdatePeerStatus("n2", NodeStatusLeaving)
	if mm.GetPeerStatus("n2") != NodeStatusLeaving {
		t.Errorf("expected Leaving, got %s", mm.GetPeerStatus("n2"))
	}
}

// ── HasQuorum ────────────────────────────────────────────────────────────────

func TestMembershipHasQuorumWhenPeersActive(t *testing.T) {
	// 3-node cluster: quorum = ceil(3/2)+1 = 2
	mm := testMembership(t, "n1", []string{"n2", "n3"})

	// No peers active → no quorum
	if mm.HasQuorum() {
		t.Error("should not have quorum with no active peers")
	}

	// Activate one peer
	mm.RecordHeartbeatSuccess("n2", 0)
	if !mm.HasQuorum() {
		t.Error("should have quorum with self + 1 active peer (2 of 3)")
	}
}

// ── ActivateIfQuorum ─────────────────────────────────────────────────────────

func TestMembershipActivateIfQuorum(t *testing.T) {
	mm := testMembership(t, "n1", []string{"n2", "n3"})

	// Not enough peers → should not activate
	activated := mm.ActivateIfQuorum()
	if activated {
		t.Error("should not activate without quorum")
	}
	if mm.GetLocalStatus() != NodeStatusJoining {
		t.Errorf("expected Joining, got %s", mm.GetLocalStatus())
	}

	// Activate enough peers
	mm.RecordHeartbeatSuccess("n2", 0)
	activated = mm.ActivateIfQuorum()
	if !activated {
		t.Error("should activate with quorum")
	}
	if mm.GetLocalStatus() != NodeStatusActive {
		t.Errorf("expected Active, got %s", mm.GetLocalStatus())
	}
}

// ── GetAllPeers ──────────────────────────────────────────────────────────────

func TestMembershipGetAllPeers(t *testing.T) {
	mm := testMembership(t, "n1", []string{"n2", "n3"})
	peers := mm.GetAllPeers()
	if len(peers) != 2 {
		t.Errorf("expected 2 peers, got %d", len(peers))
	}
}
