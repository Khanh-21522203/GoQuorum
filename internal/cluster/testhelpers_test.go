package cluster

import (
	"context"
	"sync"

	"GoQuorum/internal/common"
	"GoQuorum/internal/storage"
)

// mockRPCClient is a configurable RPCClient for tests.
type mockRPCClient struct {
	mu       sync.Mutex
	putFn    func(ctx context.Context, nodeID common.NodeID, key []byte, ss *storage.SiblingSet) error
	getFn    func(ctx context.Context, nodeID common.NodeID, key []byte) (*storage.SiblingSet, error)
	putCalls []putCall
	getCalls []getCall
}

type putCall struct {
	NodeID common.NodeID
	Key    []byte
	SS     *storage.SiblingSet
}

type getCall struct {
	NodeID common.NodeID
	Key    []byte
}

func (m *mockRPCClient) RemotePut(ctx context.Context, nodeID common.NodeID, key []byte, ss *storage.SiblingSet) error {
	m.mu.Lock()
	m.putCalls = append(m.putCalls, putCall{NodeID: nodeID, Key: key, SS: ss})
	fn := m.putFn
	m.mu.Unlock()
	if fn != nil {
		return fn(ctx, nodeID, key, ss)
	}
	return nil
}

func (m *mockRPCClient) RemoteGet(ctx context.Context, nodeID common.NodeID, key []byte) (*storage.SiblingSet, error) {
	m.mu.Lock()
	m.getCalls = append(m.getCalls, getCall{NodeID: nodeID, Key: key})
	fn := m.getFn
	m.mu.Unlock()
	if fn != nil {
		return fn(ctx, nodeID, key)
	}
	return nil, nil
}

func (m *mockRPCClient) SendHeartbeat(ctx context.Context, nodeID common.NodeID) error { return nil }
func (m *mockRPCClient) Heartbeat(ctx context.Context, nodeID common.NodeID) error     { return nil }
func (m *mockRPCClient) GetMerkleRoot(ctx context.Context, nodeID common.NodeID) ([]byte, error) {
	return make([]byte, 32), nil
}
func (m *mockRPCClient) NotifyLeaving(ctx context.Context, nodeID common.NodeID) error { return nil }
func (m *mockRPCClient) Close() error                                                  { return nil }

func (m *mockRPCClient) PutCallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.putCalls)
}
