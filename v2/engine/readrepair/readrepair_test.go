package readrepair

import (
	"sync"
	"testing"

	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/contracts/vclock"
	"goquorum.io/v2/engine/config"
	"goquorum.io/v2/engine/storage"
	"goquorum.io/v2/engine/transport"
)

// fakeTransport is a transport.Transport where only RemotePut is exercised
// by TriggerRepair; every other method is an unused no-op to satisfy the
// interface.
type fakeTransport struct {
	mu       sync.Mutex
	putCalls []node.NodeID
}

func (f *fakeTransport) RemotePut(id node.NodeID, key []byte, siblings *storage.SiblingSet, done func(error)) {
	f.mu.Lock()
	f.putCalls = append(f.putCalls, id)
	f.mu.Unlock()
	done(nil)
}

func (f *fakeTransport) RemoteGet(id node.NodeID, key []byte, done func(*storage.SiblingSet, error)) {
	done(nil, nil)
}
func (f *fakeTransport) Heartbeat(id node.NodeID, done func(error))             { done(nil) }
func (f *fakeTransport) GetMerkleRoot(id node.NodeID, done func([]byte, error)) { done(nil, nil) }
func (f *fakeTransport) NotifyLeaving(id node.NodeID, done func(error))         { done(nil) }
func (f *fakeTransport) GossipExchange(id node.NodeID, entries []transport.GossipEntry, done func([]transport.GossipEntry, error)) {
	done(nil, nil)
}
func (f *fakeTransport) Close() error { return nil }

func (f *fakeTransport) calls() []node.NodeID {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]node.NodeID(nil), f.putCalls...)
}

func tickedClock(ids ...node.NodeID) vclock.VectorClock {
	vc := vclock.NewVectorClock()
	for _, id := range ids {
		vc.Tick(id)
	}
	return vc
}

func TestTriggerRepair_EnabledRepairsStaleReplica(t *testing.T) {
	tr := &fakeTransport{}
	rr := NewReadRepairer("local", tr, config.ReadRepairConfig{Enabled: true, Probability: 1.0})

	fresh := tickedClock("a")
	stale := vclock.NewVectorClock() // Empty: dominated by fresh.

	merged := []storage.Sibling{{Value: []byte("v"), VClock: fresh}}
	responses := []ReplicaRead{
		{NodeID: "replica-1", SiblingSet: &storage.SiblingSet{Siblings: []storage.Sibling{{Value: []byte("v"), VClock: fresh}}}},
		{NodeID: "replica-2", SiblingSet: &storage.SiblingSet{Siblings: []storage.Sibling{{Value: nil, VClock: stale}}}},
	}

	rr.TriggerRepair([]byte("k"), merged, responses)

	calls := tr.calls()
	if len(calls) != 1 || calls[0] != "replica-2" {
		t.Fatalf("expected exactly one repair call to replica-2, got %v", calls)
	}
}

func TestTriggerRepair_DisabledSkipsEntirely(t *testing.T) {
	tr := &fakeTransport{}
	rr := NewReadRepairer("local", tr, config.ReadRepairConfig{Enabled: false, Probability: 1.0})

	fresh := tickedClock("a")
	stale := vclock.NewVectorClock()

	merged := []storage.Sibling{{Value: []byte("v"), VClock: fresh}}
	responses := []ReplicaRead{
		{NodeID: "replica-2", SiblingSet: &storage.SiblingSet{Siblings: []storage.Sibling{{Value: nil, VClock: stale}}}},
	}

	rr.TriggerRepair([]byte("k"), merged, responses)

	if calls := tr.calls(); len(calls) != 0 {
		t.Fatalf("expected no repair calls while disabled, got %v", calls)
	}
}

func TestTriggerRepair_ReplicaAlreadyDominatingIsNotRepaired(t *testing.T) {
	tr := &fakeTransport{}
	rr := NewReadRepairer("local", tr, config.ReadRepairConfig{Enabled: true, Probability: 1.0})

	upToDate := tickedClock("a")

	merged := []storage.Sibling{{Value: []byte("v"), VClock: upToDate}}
	responses := []ReplicaRead{
		{NodeID: "replica-1", SiblingSet: &storage.SiblingSet{Siblings: []storage.Sibling{{Value: []byte("v"), VClock: upToDate}}}},
	}

	rr.TriggerRepair([]byte("k"), merged, responses)

	if calls := tr.calls(); len(calls) != 0 {
		t.Fatalf("expected no repair call for a replica that already dominates merged, got %v", calls)
	}
}
