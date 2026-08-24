package adapter

import (
	"bytes"
	"testing"

	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/contracts/vclock"
	"goquorum.io/v2/contracts/wire"
	"goquorum.io/v2/infra/pool"
	"goquorum.io/v2/infra/transport/iouring"
)

type mockServerAdapterHandler struct {
	putKey        []byte
	putSS         *SiblingSet
	putCalled     bool
	getKey        []byte
	getCalled     bool
	heartbeatDone bool
	merkleDone    bool
	gossipDone    bool
	leavingDone   bool
	connCalled    bool
	disconnCalled bool

	getReturnSS  *SiblingSet
	getReturnErr error
	merkleRoot   []byte
	gossipReturn []GossipEntry
}

func (m *mockServerAdapterHandler) OnRemotePut(connFD int, corrID uint64, key []byte, ss *SiblingSet, reply func(error)) {
	m.putKey = append([]byte(nil), key...)
	m.putSS = ss
	m.putCalled = true
	reply(nil)
}

func (m *mockServerAdapterHandler) OnRemoteGet(connFD int, corrID uint64, key []byte, reply func(*SiblingSet, error)) {
	m.getKey = append([]byte(nil), key...)
	m.getCalled = true
	reply(m.getReturnSS, m.getReturnErr)
}

func (m *mockServerAdapterHandler) OnHeartbeat(connFD int, corrID uint64, reply func(error)) {
	m.heartbeatDone = true
	reply(nil)
}

func (m *mockServerAdapterHandler) OnGetMerkleRoot(connFD int, corrID uint64, reply func([]byte, error)) {
	m.merkleDone = true
	reply(m.merkleRoot, nil)
}

func (m *mockServerAdapterHandler) OnGossipExchange(connFD int, corrID uint64, peerID node.NodeID, entries []GossipEntry, reply func([]GossipEntry, error)) {
	m.gossipDone = true
	reply(m.gossipReturn, nil)
}

func (m *mockServerAdapterHandler) OnNotifyLeaving(connFD int, corrID uint64, peerID node.NodeID, reply func(error)) {
	m.leavingDone = true
	reply(nil)
}

func (m *mockServerAdapterHandler) OnClientConnected(connFD int, remoteAddr string) {
	m.connCalled = true
}

func (m *mockServerAdapterHandler) OnClientDisconnected(connFD int, err error) {
	m.disconnCalled = true
}

var _ ServerAdapterHandler = (*mockServerAdapterHandler)(nil)

func TestServerAdapter_Inbound_AllRequests(t *testing.T) {
	bp := pool.NewDefaultArrayPool[byte]()
	server := iouring.NewServer(nil, nil, bp, nil)
	mockHandler := &mockServerAdapterHandler{
		getReturnSS: &SiblingSet{
			Siblings: []Sibling{
				{Value: []byte("val1"), VClock: vclock.NewVectorClock()},
			},
		},
		merkleRoot: []byte("mock-root-hash"),
		gossipReturn: []GossipEntry{
			{NodeID: "node-2", Status: 1, Version: 1},
		},
	}
	serverAdapter := NewServerAdapter(server, mockHandler)

	// 1. RemotePut
	putReq := wire.RemotePutRequest{
		Key: []byte("test-key"),
		Siblings: &wire.SiblingSet{
			Siblings: []wire.StorageSibling{
				{Value: []byte("put-val"), VClock: vclock.NewVectorClock()},
			},
		},
	}
	putBody, err := putReq.Marshal()
	if err != nil {
		t.Fatalf("Marshal putReq: %v", err)
	}
	serverAdapter.OnMessage(1, iouring.FrameHeader{
		MessageID:     uint16(wire.MsgRemotePutRequest),
		CorrelationID: 101,
	}, putBody)

	if !mockHandler.putCalled || string(mockHandler.putKey) != "test-key" {
		t.Fatalf("RemotePut handler was not called properly: %+v", mockHandler)
	}

	// 2. RemoteGet
	getReq := wire.RemoteGetRequest{Key: []byte("get-key")}
	getBody, err := getReq.Marshal()
	if err != nil {
		t.Fatalf("Marshal getReq: %v", err)
	}
	serverAdapter.OnMessage(1, iouring.FrameHeader{
		MessageID:     uint16(wire.MsgRemoteGetRequest),
		CorrelationID: 102,
	}, getBody)

	if !mockHandler.getCalled || string(mockHandler.getKey) != "get-key" {
		t.Fatalf("RemoteGet handler was not called properly: %+v", mockHandler)
	}

	// 3. Heartbeat
	serverAdapter.OnMessage(1, iouring.FrameHeader{
		MessageID:     uint16(wire.MsgHeartbeatRequest),
		CorrelationID: 103,
	}, nil)
	if !mockHandler.heartbeatDone {
		t.Fatal("Heartbeat handler was not called")
	}

	// 4. GetMerkleRoot
	serverAdapter.OnMessage(1, iouring.FrameHeader{
		MessageID:     uint16(wire.MsgGetMerkleRootRequest),
		CorrelationID: 104,
	}, nil)
	if !mockHandler.merkleDone {
		t.Fatal("GetMerkleRoot handler was not called")
	}

	// 5. NotifyLeaving
	serverAdapter.OnMessage(1, iouring.FrameHeader{
		MessageID:     uint16(wire.MsgNotifyLeavingRequest),
		CorrelationID: 105,
	}, nil)
	if !mockHandler.leavingDone {
		t.Fatal("NotifyLeaving handler was not called")
	}

	// 6. GossipExchange
	gossipReq := wire.GossipExchangeRequest{
		Entries: []wire.GossipEntry{
			{NodeID: "node-1", Status: 1, Version: 1},
		},
	}
	gossipBody, err := gossipReq.Marshal()
	if err != nil {
		t.Fatalf("Marshal gossipReq: %v", err)
	}
	serverAdapter.OnMessage(1, iouring.FrameHeader{
		MessageID:     uint16(wire.MsgGossipExchangeRequest),
		CorrelationID: 106,
	}, gossipBody)
	if !mockHandler.gossipDone {
		t.Fatal("GossipExchange handler was not called")
	}
}

func TestServerAdapter_Hooks(t *testing.T) {
	bp := pool.NewDefaultArrayPool[byte]()
	server := iouring.NewServer(nil, nil, bp, nil)
	serverAdapter := NewServerAdapter(server, nil)

	connectedCalled := false
	serverAdapter.OnConnectedHook = func(connFD int, addr string) {
		if connFD == 42 && addr == "1.2.3.4:5678" {
			connectedCalled = true
		}
	}
	serverAdapter.OnConnected(42, "1.2.3.4:5678")
	if !connectedCalled {
		t.Fatal("OnConnectedHook not called")
	}

	disconnectedCalled := false
	serverAdapter.OnDisconnectedHook = func(connFD int, err error) {
		if connFD == 42 {
			disconnectedCalled = true
		}
	}
	serverAdapter.OnDisconnected(42, nil)
	if !disconnectedCalled {
		t.Fatal("OnDisconnectedHook not called")
	}

	unhandledCalled := false
	serverAdapter.OnUnhandledMsgHook = func(connFD int, hdr iouring.FrameHeader, body []byte) {
		if connFD == 42 && hdr.MessageID == 999 && bytes.Equal(body, []byte("xyz")) {
			unhandledCalled = true
		}
	}
	serverAdapter.OnMessage(42, iouring.FrameHeader{MessageID: 999}, []byte("xyz"))
	if !unhandledCalled {
		t.Fatal("OnUnhandledMsgHook not called")
	}
}

func TestServerAdapter_SendError_CorruptedBody(t *testing.T) {
	bp := pool.NewDefaultArrayPool[byte]()
	server := iouring.NewServer(nil, nil, bp, nil)
	mockHandler := &mockServerAdapterHandler{}
	serverAdapter := NewServerAdapter(server, mockHandler)

	// Send corrupted RemotePut frame (less than minimum length)
	serverAdapter.OnMessage(1, iouring.FrameHeader{
		MessageID:     uint16(wire.MsgRemotePutRequest),
		CorrelationID: 201,
	}, []byte{0x01})

	if mockHandler.putCalled {
		t.Fatal("handler should not be called for corrupted payload")
	}
}
