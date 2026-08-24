package hashring

import (
	"errors"
	"testing"

	"goquorum.io/v2/contracts/node"
)

func TestHashRing_AddNode(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(hr *HashRing)
		add     node.NodeID
		wantErr error
	}{
		{
			name:    "first add succeeds",
			setup:   func(hr *HashRing) {},
			add:     "n1",
			wantErr: nil,
		},
		{
			name: "duplicate add fails",
			setup: func(hr *HashRing) {
				if err := hr.AddNode(&node.Node{ID: "n1"}); err != nil {
					t.Fatalf("setup: unexpected error: %v", err)
				}
			},
			add:     "n1",
			wantErr: ErrNodeExists,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hr := NewHashRing(8)
			tt.setup(hr)

			err := hr.AddNode(&node.Node{ID: tt.add})
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("AddNode() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestHashRing_RemoveNode(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(hr *HashRing)
		remove  node.NodeID
		wantErr error
	}{
		{
			name:    "remove unknown node fails",
			setup:   func(hr *HashRing) {},
			remove:  "ghost",
			wantErr: ErrNodeNotFound,
		},
		{
			name: "remove known node succeeds",
			setup: func(hr *HashRing) {
				if err := hr.AddNode(&node.Node{ID: "n1"}); err != nil {
					t.Fatalf("setup: unexpected error: %v", err)
				}
			},
			remove:  "n1",
			wantErr: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hr := NewHashRing(8)
			tt.setup(hr)

			err := hr.RemoveNode(tt.remove)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("RemoveNode() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestHashRing_RemoveNode_DropsAllVNodes(t *testing.T) {
	hr := NewHashRing(16)
	mustAdd(t, hr, "n1")
	mustAdd(t, hr, "n2")

	if err := hr.RemoveNode("n1"); err != nil {
		t.Fatalf("RemoveNode() unexpected error: %v", err)
	}

	for _, v := range hr.vnodes {
		if v.NodeID == "n1" {
			t.Fatalf("found vnode for removed node n1 still on the ring")
		}
	}
	if len(hr.vnodes) != 16 {
		t.Fatalf("len(vnodes) = %d, want 16 (only n2's vnodes remain)", len(hr.vnodes))
	}
}

func TestHashRing_GetPreferenceList_EmptyRing(t *testing.T) {
	hr := NewHashRing(8)

	_, err := hr.GetPreferenceList("some-key", 2)
	if !errors.Is(err, ErrEmptyRing) {
		t.Fatalf("GetPreferenceList() error = %v, want %v", err, ErrEmptyRing)
	}
}

func TestHashRing_GetPreferenceList_DistinctAndStable(t *testing.T) {
	hr := NewHashRing(32)
	mustAdd(t, hr, "n1")
	mustAdd(t, hr, "n2")
	mustAdd(t, hr, "n3")

	first, err := hr.GetPreferenceList("some-key", 2)
	if err != nil {
		t.Fatalf("GetPreferenceList() unexpected error: %v", err)
	}
	if len(first) != 2 {
		t.Fatalf("len(preference list) = %d, want 2", len(first))
	}
	if first[0] == first[1] {
		t.Fatalf("preference list contains a duplicate node: %v", first)
	}

	// Repeated calls against an unchanged ring must return the same order.
	second, err := hr.GetPreferenceList("some-key", 2)
	if err != nil {
		t.Fatalf("GetPreferenceList() unexpected error on second call: %v", err)
	}
	if first[0] != second[0] || first[1] != second[1] {
		t.Fatalf("GetPreferenceList() not stable: %v then %v", first, second)
	}
}

func TestHashRing_GetPreferenceList_ClampsToAvailableNodes(t *testing.T) {
	hr := NewHashRing(8)
	mustAdd(t, hr, "n1")
	mustAdd(t, hr, "n2")

	list, err := hr.GetPreferenceList("some-key", 10)
	if err != nil {
		t.Fatalf("GetPreferenceList() unexpected error: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("len(preference list) = %d, want 2 (clamped to ring size)", len(list))
	}
}

func TestHashRing_GetPrimaryNode(t *testing.T) {
	hr := NewHashRing(16)
	mustAdd(t, hr, "n1")
	mustAdd(t, hr, "n2")

	primary, err := hr.GetPrimaryNode("some-key")
	if err != nil {
		t.Fatalf("GetPrimaryNode() unexpected error: %v", err)
	}

	list, err := hr.GetPreferenceList("some-key", 1)
	if err != nil {
		t.Fatalf("GetPreferenceList() unexpected error: %v", err)
	}
	if primary != list[0] {
		t.Fatalf("GetPrimaryNode() = %v, want %v (first of preference list)", primary, list[0])
	}
}

func TestHashRing_GetPrimaryNode_EmptyRing(t *testing.T) {
	hr := NewHashRing(8)

	_, err := hr.GetPrimaryNode("some-key")
	if !errors.Is(err, ErrEmptyRing) {
		t.Fatalf("GetPrimaryNode() error = %v, want %v", err, ErrEmptyRing)
	}
}

func TestHashRing_SizeAndNodes(t *testing.T) {
	hr := NewHashRing(8)
	if got := hr.Size(); got != 0 {
		t.Fatalf("Size() = %d, want 0", got)
	}
	if got := hr.Nodes(); len(got) != 0 {
		t.Fatalf("Nodes() = %v, want empty", got)
	}

	mustAdd(t, hr, "n1")
	mustAdd(t, hr, "n2")
	mustAdd(t, hr, "n3")

	if got := hr.Size(); got != 3 {
		t.Fatalf("Size() = %d, want 3", got)
	}

	want := map[node.NodeID]bool{"n1": true, "n2": true, "n3": true}
	got := hr.Nodes()
	if len(got) != len(want) {
		t.Fatalf("len(Nodes()) = %d, want %d", len(got), len(want))
	}
	for _, n := range got {
		if !want[n.ID] {
			t.Fatalf("Nodes() returned unexpected node %v", n.ID)
		}
		delete(want, n.ID)
	}
	if len(want) != 0 {
		t.Fatalf("Nodes() missing expected nodes: %v", want)
	}

	if err := hr.RemoveNode("n1"); err != nil {
		t.Fatalf("RemoveNode() unexpected error: %v", err)
	}
	if got := hr.Size(); got != 2 {
		t.Fatalf("Size() after remove = %d, want 2", got)
	}
}

func mustAdd(t *testing.T, hr *HashRing, id node.NodeID) {
	t.Helper()
	if err := hr.AddNode(&node.Node{ID: id}); err != nil {
		t.Fatalf("AddNode(%v) unexpected error: %v", id, err)
	}
}
