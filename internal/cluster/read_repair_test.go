package cluster

import (
	"context"
	"testing"
	"time"

	"GoQuorum/internal/common"
	"GoQuorum/internal/config"
	"GoQuorum/internal/storage"
	"GoQuorum/internal/vclock"
)

func makeVClock(node string, counter uint64) vclock.VectorClock {
	vc := vclock.NewVectorClock()
	vc.SetString(node, counter)
	return vc
}

// ── Repair is skipped when probability = 0 ───────────────────────────────

func TestReadRepairSkippedAtZeroProbability(t *testing.T) {
	mock := &mockRPCClient{}
	rr := NewReadRepairer("n1", mock, config.ReadRepairConfig{
		Enabled:     true,
		Probability: 0.0,
		Timeout:     time.Second,
	})

	merged := []storage.Sibling{{Value: []byte("v"), VClock: makeVClock("n1", 2)}}
	staleSet := &storage.SiblingSet{Siblings: []storage.Sibling{
		{Value: []byte("old"), VClock: makeVClock("n1", 1)},
	}}
	responses := []ReadResponse{
		{NodeID: "n2", SiblingSet: staleSet},
	}

	rr.TriggerRepair(context.Background(), []byte("k"), merged, responses)
	time.Sleep(20 * time.Millisecond)

	if mock.PutCallCount() != 0 {
		t.Errorf("expected 0 repair RPCs at probability=0, got %d", mock.PutCallCount())
	}
}

// ── Repair is sent to a stale replica at probability = 1 ─────────────────

func TestReadRepairSentToStaleReplica(t *testing.T) {
	mock := &mockRPCClient{}
	rr := NewReadRepairer("n1", mock, config.ReadRepairConfig{
		Enabled:     true,
		Probability: 1.0,
		Timeout:     time.Second,
		Async:       false,
	})

	newVC := makeVClock("n1", 2)
	oldVC := makeVClock("n1", 1)

	merged := []storage.Sibling{{Value: []byte("new"), VClock: newVC}}
	staleSet := &storage.SiblingSet{Siblings: []storage.Sibling{
		{Value: []byte("old"), VClock: oldVC},
	}}
	responses := []ReadResponse{
		{NodeID: "n2", SiblingSet: staleSet},
	}

	rr.TriggerRepair(context.Background(), []byte("k"), merged, responses)

	if mock.PutCallCount() == 0 {
		t.Error("expected repair RPC to stale replica")
	}
}

// ── Self is not repaired ──────────────────────────────────────────────────

func TestReadRepairSkipsSelf(t *testing.T) {
	mock := &mockRPCClient{}
	rr := NewReadRepairer("n1", mock, config.ReadRepairConfig{
		Enabled:     true,
		Probability: 1.0,
		Timeout:     time.Second,
		Async:       false,
	})

	newVC := makeVClock("n1", 2)
	oldVC := makeVClock("n1", 1)

	merged := []storage.Sibling{{Value: []byte("new"), VClock: newVC}}
	staleSet := &storage.SiblingSet{Siblings: []storage.Sibling{
		{Value: []byte("old"), VClock: oldVC},
	}}

	// Self is the stale replica
	responses := []ReadResponse{
		{NodeID: common.NodeID("n1"), SiblingSet: staleSet},
	}

	rr.TriggerRepair(context.Background(), []byte("k"), merged, responses)

	if mock.PutCallCount() != 0 {
		t.Errorf("self should not be repaired via RPC, got %d calls", mock.PutCallCount())
	}
}

// ── Up-to-date replica is not repaired ────────────────────────────────────

func TestReadRepairSkipsUpToDateReplica(t *testing.T) {
	mock := &mockRPCClient{}
	rr := NewReadRepairer("n1", mock, config.ReadRepairConfig{
		Enabled:     true,
		Probability: 1.0,
		Timeout:     time.Second,
		Async:       false,
	})

	vc := makeVClock("n1", 2)
	merged := []storage.Sibling{{Value: []byte("v"), VClock: vc}}
	// Replica has the same VC
	upToDate := &storage.SiblingSet{Siblings: []storage.Sibling{
		{Value: []byte("v"), VClock: vc},
	}}
	responses := []ReadResponse{
		{NodeID: "n2", SiblingSet: upToDate},
	}

	rr.TriggerRepair(context.Background(), []byte("k"), merged, responses)

	if mock.PutCallCount() != 0 {
		t.Errorf("up-to-date replica should not be repaired, got %d calls", mock.PutCallCount())
	}
}

// ── Nil sibling set triggers repair ──────────────────────────────────────

func TestReadRepairNilSiblingSetNeedsRepair(t *testing.T) {
	mock := &mockRPCClient{}
	rr := NewReadRepairer("n1", mock, config.ReadRepairConfig{
		Enabled:     true,
		Probability: 1.0,
		Timeout:     time.Second,
		Async:       false,
	})

	vc := makeVClock("n1", 1)
	merged := []storage.Sibling{{Value: []byte("v"), VClock: vc}}
	responses := []ReadResponse{
		{NodeID: "n2", SiblingSet: nil},
	}

	rr.TriggerRepair(context.Background(), []byte("k"), merged, responses)

	if mock.PutCallCount() == 0 {
		t.Error("nil sibling set should trigger repair")
	}
}
