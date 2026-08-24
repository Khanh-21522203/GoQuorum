package membership

import (
	"testing"
	"time"

	"goquorum.io/v2/contracts/node"
)

// threeNodeConfig returns a config for a 3-member static cluster (self plus
// two peers), which gives a quorum size of (3/2)+1 = 2.
func threeNodeConfig() Config {
	return Config{
		NodeID:     "self",
		ListenAddr: "local:9000",
		Members: []MemberConfig{
			{ID: "self", Addr: "local:9000"},
			{ID: "peer1", Addr: "peer1:9000"},
			{ID: "peer2", Addr: "peer2:9000"},
		},
		FailureThreshold: 3,
	}
}

func TestNewMembershipManager_SeedsLocalMetadata(t *testing.T) {
	mm := NewMembershipManager(threeNodeConfig(), "v1.2.3")

	if got := mm.LocalNodeID(); got != "self" {
		t.Fatalf("LocalNodeID() = %v, want %v", got, "self")
	}
	if got := mm.GetLocalStatus(); got != NodeStatusJoining {
		t.Fatalf("GetLocalStatus() = %v, want %v", got, NodeStatusJoining)
	}
	if got := mm.TotalPeerCount(); got != 0 {
		t.Fatalf("TotalPeerCount() = %d, want 0 (peers are discovered dynamically)", got)
	}
}

func TestGetSetLocalStatus_RoundTrip(t *testing.T) {
	tests := []struct {
		name   string
		status NodeStatus
	}{
		{"active", NodeStatusActive},
		{"suspect", NodeStatusSuspect},
		{"failed", NodeStatusFailed},
		{"leaving", NodeStatusLeaving},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mm := NewMembershipManager(threeNodeConfig(), "v1")
			mm.SetLocalStatus(tt.status)
			if got := mm.GetLocalStatus(); got != tt.status {
				t.Fatalf("GetLocalStatus() = %v, want %v", got, tt.status)
			}
		})
	}
}

func TestUpdatePeerStatus_CreatesAbsentPeer(t *testing.T) {
	mm := NewMembershipManager(threeNodeConfig(), "v1")

	if got := mm.GetPeerStatus("peer1"); got != NodeStatusUnknown {
		t.Fatalf("GetPeerStatus() before update = %v, want %v", got, NodeStatusUnknown)
	}

	mm.UpdatePeerStatus("peer1", NodeStatusActive)

	if got := mm.GetPeerStatus("peer1"); got != NodeStatusActive {
		t.Fatalf("GetPeerStatus() after update = %v, want %v", got, NodeStatusActive)
	}
	if got := mm.TotalPeerCount(); got != 1 {
		t.Fatalf("TotalPeerCount() = %d, want 1", got)
	}
}

func TestRecordHeartbeatSuccess_MarksPeerActive(t *testing.T) {
	mm := NewMembershipManager(threeNodeConfig(), "v1")
	mm.UpdatePeerStatus("peer1", NodeStatusFailed)

	mm.RecordHeartbeatSuccess("peer1", 5*time.Millisecond)

	if got := mm.GetPeerStatus("peer1"); got != NodeStatusActive {
		t.Fatalf("GetPeerStatus() = %v, want %v", got, NodeStatusActive)
	}
}

func TestRecordHeartbeatSuccess_UnknownPeerIsNoOp(t *testing.T) {
	mm := NewMembershipManager(threeNodeConfig(), "v1")

	mm.RecordHeartbeatSuccess("ghost", time.Millisecond)

	if got := mm.TotalPeerCount(); got != 0 {
		t.Fatalf("TotalPeerCount() = %d, want 0 (heartbeat for unknown peer must not create it)", got)
	}
}

func TestRecordHeartbeatFailure_EscalatesAtThreshold(t *testing.T) {
	tests := []struct {
		name     string
		failures int
		want     NodeStatus
	}{
		{"below threshold demotes active to suspect", 1, NodeStatusSuspect},
		{"just below threshold stays suspect", 2, NodeStatusSuspect},
		{"at threshold becomes failed", 3, NodeStatusFailed},
		{"past threshold stays failed", 4, NodeStatusFailed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mm := NewMembershipManager(threeNodeConfig(), "v1") // FailureThreshold: 3
			mm.UpdatePeerStatus("peer1", NodeStatusActive)

			for i := 0; i < tt.failures; i++ {
				mm.RecordHeartbeatFailure("peer1")
			}

			if got := mm.GetPeerStatus("peer1"); got != tt.want {
				t.Fatalf("after %d failures, GetPeerStatus() = %v, want %v", tt.failures, got, tt.want)
			}
		})
	}
}

func TestRecordHeartbeatFailure_UnknownPeerIsNoOp(t *testing.T) {
	mm := NewMembershipManager(threeNodeConfig(), "v1")

	mm.RecordHeartbeatFailure("ghost")

	if got := mm.TotalPeerCount(); got != 0 {
		t.Fatalf("TotalPeerCount() = %d, want 0", got)
	}
}

func TestHasQuorum_Boundary(t *testing.T) {
	// quorumSize() = (3/2)+1 = 2.
	tests := []struct {
		name        string
		localActive bool
		activePeers int
		want        bool
	}{
		{"nothing active", false, 0, false},
		{"one peer active, local inactive", false, 1, false},
		{"local active alone", true, 0, false},
		{"local active plus one peer meets quorum", true, 1, true},
		{"two peers active without local meets quorum", false, 2, true},
		{"everything active", true, 2, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mm := NewMembershipManager(threeNodeConfig(), "v1")
			if tt.localActive {
				mm.SetLocalStatus(NodeStatusActive)
			}
			peerIDs := []node.NodeID{"peer1", "peer2"}
			for i := 0; i < tt.activePeers; i++ {
				mm.UpdatePeerStatus(peerIDs[i], NodeStatusActive)
			}

			if got := mm.HasQuorum(); got != tt.want {
				t.Fatalf("HasQuorum() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestActivateIfQuorum_CheckThenAct(t *testing.T) {
	// quorumSize() = 2; ActivateIfQuorum counts the local node as the +1
	// that activation itself would contribute, so 1 active peer suffices.
	t.Run("insufficient peers leaves status unchanged", func(t *testing.T) {
		mm := NewMembershipManager(threeNodeConfig(), "v1")

		activated := mm.ActivateIfQuorum()

		if activated {
			t.Fatal("ActivateIfQuorum() = true, want false")
		}
		if got := mm.GetLocalStatus(); got != NodeStatusJoining {
			t.Fatalf("GetLocalStatus() = %v, want %v (unchanged)", got, NodeStatusJoining)
		}
	})

	t.Run("sufficient peers activates local node", func(t *testing.T) {
		mm := NewMembershipManager(threeNodeConfig(), "v1")
		mm.UpdatePeerStatus("peer1", NodeStatusActive)

		activated := mm.ActivateIfQuorum()

		if !activated {
			t.Fatal("ActivateIfQuorum() = false, want true")
		}
		if got := mm.GetLocalStatus(); got != NodeStatusActive {
			t.Fatalf("GetLocalStatus() = %v, want %v", got, NodeStatusActive)
		}
	})

	t.Run("already-active local node stays active and reports true", func(t *testing.T) {
		mm := NewMembershipManager(threeNodeConfig(), "v1")
		mm.UpdatePeerStatus("peer1", NodeStatusActive)
		mm.SetLocalStatus(NodeStatusActive)

		activated := mm.ActivateIfQuorum()

		if !activated {
			t.Fatal("ActivateIfQuorum() = false, want true")
		}
		if got := mm.GetLocalStatus(); got != NodeStatusActive {
			t.Fatalf("GetLocalStatus() = %v, want %v", got, NodeStatusActive)
		}
	})
}

func TestGetClusterView(t *testing.T) {
	mm := NewMembershipManager(threeNodeConfig(), "v1")
	mm.SetLocalStatus(NodeStatusActive)
	mm.UpdatePeerStatus("peer1", NodeStatusSuspect)
	mm.UpdatePeerStatus("peer2", NodeStatusFailed)

	view := mm.GetClusterView()

	want := map[node.NodeID]NodeStatus{
		"self":  NodeStatusActive,
		"peer1": NodeStatusSuspect,
		"peer2": NodeStatusFailed,
	}
	if len(view) != len(want) {
		t.Fatalf("len(GetClusterView()) = %d, want %d", len(view), len(want))
	}
	for id, status := range want {
		if got := view[id]; got != status {
			t.Fatalf("GetClusterView()[%v] = %v, want %v", id, got, status)
		}
	}
}

func TestGetPeers_MapsStatus(t *testing.T) {
	mm := NewMembershipManager(threeNodeConfig(), "v1")
	mm.AddPeer("peer1", "peer1:9000", "peer1:9001")
	mm.UpdatePeerStatus("peer1", NodeStatusActive)
	mm.AddPeer("peer2", "peer2:9000", "peer2:9001")
	mm.UpdatePeerStatus("peer2", NodeStatusJoining) // no PeerStatus equivalent

	peers := mm.GetPeers()
	if len(peers) != 2 {
		t.Fatalf("len(GetPeers()) = %d, want 2", len(peers))
	}

	byID := make(map[node.NodeID]node.PeerInfo, len(peers))
	for _, p := range peers {
		byID[p.ID] = p
	}

	p1, ok := byID["peer1"]
	if !ok {
		t.Fatal("GetPeers() missing peer1")
	}
	if p1.Addr != "peer1:9000" {
		t.Fatalf("peer1 Addr = %v, want peer1:9000", p1.Addr)
	}
	if p1.Status != node.PeerStatusActive {
		t.Fatalf("peer1 Status = %v, want %v", p1.Status, node.PeerStatusActive)
	}

	p2, ok := byID["peer2"]
	if !ok {
		t.Fatal("GetPeers() missing peer2")
	}
	if p2.Status != node.PeerStatusUnknown {
		t.Fatalf("peer2 (Joining) Status = %v, want %v (no PeerStatus equivalent)", p2.Status, node.PeerStatusUnknown)
	}
}

func TestGetAllNodes_IncludesLocal(t *testing.T) {
	mm := NewMembershipManager(threeNodeConfig(), "v1")
	mm.UpdatePeerStatus("peer1", NodeStatusActive)
	mm.UpdatePeerStatus("peer2", NodeStatusActive)

	all := mm.GetAllNodes()

	want := map[node.NodeID]bool{"self": true, "peer1": true, "peer2": true}
	if len(all) != len(want) {
		t.Fatalf("len(GetAllNodes()) = %d, want %d", len(all), len(want))
	}
	for _, id := range all {
		if !want[id] {
			t.Fatalf("GetAllNodes() returned unexpected id %v", id)
		}
		delete(want, id)
	}
	if len(want) != 0 {
		t.Fatalf("GetAllNodes() missing expected ids: %v", want)
	}
}

func TestGetAddress_SpecialCasesLocalNode(t *testing.T) {
	mm := NewMembershipManager(threeNodeConfig(), "v1")
	mm.AddPeer("peer1", "peer1:9000", "peer1:9001")

	if got := mm.GetAddress("self"); got != "local:9000" {
		t.Fatalf("GetAddress(self) = %v, want %v", got, "local:9000")
	}
	if got := mm.GetAddress("peer1"); got != "peer1:9000" {
		t.Fatalf("GetAddress(peer1) = %v, want %v", got, "peer1:9000")
	}
	if got := mm.GetAddress("ghost"); got != "" {
		t.Fatalf("GetAddress(ghost) = %v, want empty string", got)
	}
}

func TestGetHTTPAddress_SpecialCasesLocalNode(t *testing.T) {
	mm := NewMembershipManager(threeNodeConfig(), "v1")
	mm.AddPeer("peer1", "peer1:9000", "peer1:9001")

	if got := mm.GetHTTPAddress("self"); got != "local:9000" {
		t.Fatalf("GetHTTPAddress(self) = %v, want %v", got, "local:9000")
	}
	if got := mm.GetHTTPAddress("peer1"); got != "peer1:9001" {
		t.Fatalf("GetHTTPAddress(peer1) = %v, want %v", got, "peer1:9001")
	}
	if got := mm.GetHTTPAddress("ghost"); got != "" {
		t.Fatalf("GetHTTPAddress(ghost) = %v, want empty string", got)
	}
}

func TestAddPeer_IgnoresDuplicate(t *testing.T) {
	mm := NewMembershipManager(threeNodeConfig(), "v1")
	mm.AddPeer("peer1", "peer1:9000", "peer1:9001")
	mm.UpdatePeerStatus("peer1", NodeStatusActive)

	mm.AddPeer("peer1", "different:9999", "different:9998")

	addr, _ := mm.GetPeerAddr("peer1")
	if addr != "peer1:9000" {
		t.Fatalf("GetPeerAddr(peer1) = %v, want %v (duplicate AddPeer must not overwrite)", addr, "peer1:9000")
	}
	if got := mm.GetPeerStatus("peer1"); got != NodeStatusActive {
		t.Fatalf("GetPeerStatus(peer1) = %v, want %v (duplicate AddPeer must not reset status)", got, NodeStatusActive)
	}
}

func TestRemovePeer(t *testing.T) {
	mm := NewMembershipManager(threeNodeConfig(), "v1")
	mm.UpdatePeerStatus("peer1", NodeStatusActive)

	mm.RemovePeer("peer1")

	if got := mm.GetPeerStatus("peer1"); got != NodeStatusUnknown {
		t.Fatalf("GetPeerStatus(peer1) after removal = %v, want %v", got, NodeStatusUnknown)
	}
	if got := mm.TotalPeerCount(); got != 0 {
		t.Fatalf("TotalPeerCount() after removal = %d, want 0", got)
	}
}

func TestGetActivePeers(t *testing.T) {
	mm := NewMembershipManager(threeNodeConfig(), "v1")
	mm.UpdatePeerStatus("peer1", NodeStatusActive)
	mm.UpdatePeerStatus("peer2", NodeStatusSuspect)

	active := mm.GetActivePeers()
	if len(active) != 1 || active[0] != "peer1" {
		t.Fatalf("GetActivePeers() = %v, want [peer1]", active)
	}
	if got := mm.ActivePeerCount(); got != 1 {
		t.Fatalf("ActivePeerCount() = %d, want 1", got)
	}
	if got := mm.TotalPeerCount(); got != 2 {
		t.Fatalf("TotalPeerCount() = %d, want 2", got)
	}
}
