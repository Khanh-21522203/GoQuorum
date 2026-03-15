package cluster

import (
	"testing"

	"GoQuorum/internal/common"
)

func makeNode(id string) *common.Node {
	return &common.Node{
		ID:               common.NodeID(id),
		Addr:             id + ":7070",
		State:            common.NodeStateActive,
		VirtualNodeCount: 4, // small for tests
	}
}

// ── AddNode / Size ─────────────────────────────────────────────────────────

func TestHashRingAddNode(t *testing.T) {
	hr := NewHashRing(4)
	if err := hr.AddNode(makeNode("node1")); err != nil {
		t.Fatalf("AddNode: %v", err)
	}
	if hr.Size() != 1 {
		t.Errorf("expected size 1, got %d", hr.Size())
	}
}

func TestHashRingAddDuplicateReturnsError(t *testing.T) {
	hr := NewHashRing(4)
	hr.AddNode(makeNode("node1"))
	if err := hr.AddNode(makeNode("node1")); err != ErrNodeExists {
		t.Errorf("expected ErrNodeExists, got %v", err)
	}
}

// ── RemoveNode ─────────────────────────────────────────────────────────────

func TestHashRingRemoveNode(t *testing.T) {
	hr := NewHashRing(4)
	hr.AddNode(makeNode("node1"))
	hr.AddNode(makeNode("node2"))

	if err := hr.RemoveNode("node1"); err != nil {
		t.Fatalf("RemoveNode: %v", err)
	}
	if hr.Size() != 1 {
		t.Errorf("expected size 1 after removal, got %d", hr.Size())
	}
}

func TestHashRingRemoveMissingReturnsError(t *testing.T) {
	hr := NewHashRing(4)
	if err := hr.RemoveNode("ghost"); err != ErrNodeNotFound {
		t.Errorf("expected ErrNodeNotFound, got %v", err)
	}
}

// ── GetPreferenceList ──────────────────────────────────────────────────────

func TestHashRingPreferenceListEmpty(t *testing.T) {
	hr := NewHashRing(4)
	_, err := hr.GetPreferenceList("key", 3)
	if err != ErrEmptyRing {
		t.Errorf("expected ErrEmptyRing, got %v", err)
	}
}

func TestHashRingPreferenceListNDistinct(t *testing.T) {
	hr := NewHashRing(4)
	hr.AddNode(makeNode("n1"))
	hr.AddNode(makeNode("n2"))
	hr.AddNode(makeNode("n3"))

	list, err := hr.GetPreferenceList("somekey", 3)
	if err != nil {
		t.Fatalf("GetPreferenceList: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("expected 3 nodes, got %d", len(list))
	}

	// All distinct
	seen := make(map[common.NodeID]struct{})
	for _, id := range list {
		if _, dup := seen[id]; dup {
			t.Errorf("duplicate node %s in preference list", id)
		}
		seen[id] = struct{}{}
	}
}

func TestHashRingPreferenceListDeterministic(t *testing.T) {
	hr := NewHashRing(4)
	hr.AddNode(makeNode("n1"))
	hr.AddNode(makeNode("n2"))
	hr.AddNode(makeNode("n3"))

	l1, _ := hr.GetPreferenceList("key", 3)
	l2, _ := hr.GetPreferenceList("key", 3)

	for i := range l1 {
		if l1[i] != l2[i] {
			t.Errorf("preference list not deterministic at index %d", i)
		}
	}
}

func TestHashRingPreferenceListCapsAtNodeCount(t *testing.T) {
	hr := NewHashRing(4)
	hr.AddNode(makeNode("only"))

	list, err := hr.GetPreferenceList("key", 5)
	if err != nil {
		t.Fatalf("GetPreferenceList: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("expected 1 (capped), got %d", len(list))
	}
}

func TestHashRingRemoveExcludesNodeFromPrefList(t *testing.T) {
	hr := NewHashRing(8)
	hr.AddNode(makeNode("n1"))
	hr.AddNode(makeNode("n2"))
	hr.AddNode(makeNode("n3"))

	hr.RemoveNode("n1")

	list, _ := hr.GetPreferenceList("anykey", 3)
	for _, id := range list {
		if id == "n1" {
			t.Error("removed node n1 still appears in preference list")
		}
	}
}

// ── GetPrimaryNode ─────────────────────────────────────────────────────────

func TestHashRingGetPrimaryNode(t *testing.T) {
	hr := NewHashRing(4)
	hr.AddNode(makeNode("only"))

	primary, err := hr.GetPrimaryNode("key")
	if err != nil {
		t.Fatalf("GetPrimaryNode: %v", err)
	}
	if primary != "only" {
		t.Errorf("expected 'only', got %s", primary)
	}
}
