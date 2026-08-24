package adapter

import (
	"bytes"
	"errors"
	"net"
	"testing"

	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/contracts/vclock"
	"goquorum.io/v2/contracts/wire"
	"goquorum.io/v2/infra/ioruntime"
	"goquorum.io/v2/infra/reactor"
	"goquorum.io/v2/infra/transport/iouring"
)

type testClientAdapterHandler struct {
	putRespPeer   node.NodeID
	putRespCorr   uint64
	putRespStatus wire.StatusCode

	getRespPeer     node.NodeID
	getRespCorr     uint64
	getRespSiblings *SiblingSet
	getRespStatus   wire.StatusCode

	heartbeatPeer   node.NodeID
	heartbeatCorr   uint64
	heartbeatStatus wire.StatusCode

	merklePeer   node.NodeID
	merkleCorr   uint64
	merkleRoot   []byte
	merkleStatus wire.StatusCode

	notifyPeer   node.NodeID
	notifyCorr   uint64
	notifyStatus wire.StatusCode

	gossipPeer    node.NodeID
	gossipCorr    uint64
	gossipEntries []GossipEntry

	connectedPeer node.NodeID
	connectedAddr string

	disconnectedPeer node.NodeID
	disconnectedErr  error

	connectErrPeer node.NodeID
	connectErrErr  error

	rpcErrPeer    node.NodeID
	rpcErrCorr    uint64
	rpcErrRPCType uint16
	rpcErrErr     error
}

func (h *testClientAdapterHandler) OnRemotePutResponse(peerID node.NodeID, corrID uint64, status wire.StatusCode) {
	h.putRespPeer = peerID
	h.putRespCorr = corrID
	h.putRespStatus = status
}

func (h *testClientAdapterHandler) OnRemoteGetResponse(peerID node.NodeID, corrID uint64, siblings *SiblingSet, status wire.StatusCode) {
	h.getRespPeer = peerID
	h.getRespCorr = corrID
	h.getRespSiblings = siblings
	h.getRespStatus = status
}

func (h *testClientAdapterHandler) OnHeartbeatResponse(peerID node.NodeID, corrID uint64, status wire.StatusCode) {
	h.heartbeatPeer = peerID
	h.heartbeatCorr = corrID
	h.heartbeatStatus = status
}

func (h *testClientAdapterHandler) OnGetMerkleRootResponse(peerID node.NodeID, corrID uint64, root []byte, status wire.StatusCode) {
	h.merklePeer = peerID
	h.merkleCorr = corrID
	h.merkleRoot = root
	h.merkleStatus = status
}

func (h *testClientAdapterHandler) OnNotifyLeavingResponse(peerID node.NodeID, corrID uint64, status wire.StatusCode) {
	h.notifyPeer = peerID
	h.notifyCorr = corrID
	h.notifyStatus = status
}

func (h *testClientAdapterHandler) OnGossipExchangeResponse(peerID node.NodeID, corrID uint64, entries []GossipEntry) {
	h.gossipPeer = peerID
	h.gossipCorr = corrID
	h.gossipEntries = entries
}

func (h *testClientAdapterHandler) OnPeerConnected(peerID node.NodeID, addr string) {
	h.connectedPeer = peerID
	h.connectedAddr = addr
}

func (h *testClientAdapterHandler) OnPeerDisconnected(peerID node.NodeID, err error) {
	h.disconnectedPeer = peerID
	h.disconnectedErr = err
}

func (h *testClientAdapterHandler) OnPeerConnectError(peerID node.NodeID, err error) {
	h.connectErrPeer = peerID
	h.connectErrErr = err
}

func (h *testClientAdapterHandler) OnRPCError(peerID node.NodeID, corrID uint64, rpcType uint16, err error) {
	h.rpcErrPeer = peerID
	h.rpcErrCorr = corrID
	h.rpcErrRPCType = rpcType
	h.rpcErrErr = err
}

var _ ClientAdapterHandler = (*testClientAdapterHandler)(nil)

func newTestClientAdapter(t *testing.T) (*ClientAdapter, *testClientAdapterHandler, *reactor.Reactor, *ioruntime.Runtime) {
	t.Helper()
	rt, err := ioruntime.New(64)
	if err != nil {
		t.Fatalf("ioruntime.New: %v", err)
	}
	r := reactor.New(rt)
	client := iouring.NewClient(rt, r, nil, nil)
	adapter := NewClientAdapter(client, r)
	handler := &testClientAdapterHandler{}
	adapter.SetInboundHandler(handler)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	_ = client.Dial("node-1", ln.Addr().String())

	return adapter, handler, r, rt
}

func TestClientAdapter_RemotePut_Success(t *testing.T) {
	adapter, handler, r, rt := newTestClientAdapter(t)
	defer rt.Close()
	_ = r

	const peerID node.NodeID = "node-1"
	const corrID uint64 = 42
	key := []byte("key-put")
	vc := vclock.NewVectorClock()
	vc.Set("node-1", 1)
	siblings := &SiblingSet{
		Siblings: []Sibling{
			{Value: []byte("val1"), VClock: vc, Timestamp: 100},
		},
	}

	if err := adapter.RemotePut(peerID, corrID, key, siblings); err != nil {
		t.Fatalf("RemotePut: %v", err)
	}

	// Simulate reply frame
	resp := wire.RemotePutResponse{Status: wire.StatusOK}
	respBytes, err := resp.Marshal()
	if err != nil {
		t.Fatal(err)
	}

	adapter.OnFrame(peerID, iouring.FrameHeader{MessageID: uint16(wire.MsgRemotePutResponse), CorrelationID: corrID}, respBytes)

	if handler.putRespPeer != peerID || handler.putRespCorr != corrID || handler.putRespStatus != wire.StatusOK {
		t.Fatalf("unexpected put response: peer=%s corr=%d status=%d", handler.putRespPeer, handler.putRespCorr, handler.putRespStatus)
	}
}

func TestClientAdapter_RemoteGet_Success(t *testing.T) {
	adapter, handler, _, rt := newTestClientAdapter(t)
	defer rt.Close()

	const peerID node.NodeID = "node-1"
	const corrID uint64 = 43
	key := []byte("key-get")

	if err := adapter.RemoteGet(peerID, corrID, key); err != nil {
		t.Fatalf("RemoteGet: %v", err)
	}

	vc := vclock.NewVectorClock()
	vc.Set("node-1", 2)
	resp := wire.RemoteGetResponse{
		Status: wire.StatusOK,
		Siblings: &SiblingSet{
			Siblings: []Sibling{
				{Value: []byte("get-val"), VClock: vc, Timestamp: 200},
			},
		},
	}
	respBytes, err := resp.Marshal()
	if err != nil {
		t.Fatal(err)
	}

	adapter.OnFrame(peerID, iouring.FrameHeader{MessageID: uint16(wire.MsgRemoteGetResponse), CorrelationID: corrID}, respBytes)

	if handler.getRespPeer != peerID || handler.getRespCorr != corrID || handler.getRespStatus != wire.StatusOK {
		t.Fatalf("unexpected get response: peer=%s corr=%d status=%d", handler.getRespPeer, handler.getRespCorr, handler.getRespStatus)
	}
	if handler.getRespSiblings == nil || len(handler.getRespSiblings.Siblings) != 1 || !bytes.Equal(handler.getRespSiblings.Siblings[0].Value, []byte("get-val")) {
		t.Fatalf("unexpected siblings: %+v", handler.getRespSiblings)
	}
}

func TestClientAdapter_RemoteGet_KeyNotFound(t *testing.T) {
	adapter, handler, _, rt := newTestClientAdapter(t)
	defer rt.Close()

	const peerID node.NodeID = "node-1"
	const corrID uint64 = 44
	key := []byte("missing-key")

	if err := adapter.RemoteGet(peerID, corrID, key); err != nil {
		t.Fatalf("RemoteGet: %v", err)
	}

	resp := wire.RemoteGetResponse{Status: wire.StatusKeyNotFound}
	respBytes, err := resp.Marshal()
	if err != nil {
		t.Fatal(err)
	}

	adapter.OnFrame(peerID, iouring.FrameHeader{MessageID: uint16(wire.MsgRemoteGetResponse), CorrelationID: corrID}, respBytes)

	if handler.getRespStatus != wire.StatusKeyNotFound {
		t.Fatalf("expected StatusKeyNotFound, got: %d", handler.getRespStatus)
	}
}

func TestClientAdapter_Heartbeat_Success(t *testing.T) {
	adapter, handler, _, rt := newTestClientAdapter(t)
	defer rt.Close()

	const peerID node.NodeID = "node-1"
	const corrID uint64 = 45

	if err := adapter.Heartbeat(peerID, corrID); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}

	resp := wire.HeartbeatResponse{Status: wire.StatusOK}
	respBytes, _ := resp.Marshal()
	adapter.OnFrame(peerID, iouring.FrameHeader{MessageID: uint16(wire.MsgHeartbeatResponse), CorrelationID: corrID}, respBytes)

	if handler.heartbeatPeer != peerID || handler.heartbeatCorr != corrID || handler.heartbeatStatus != wire.StatusOK {
		t.Fatalf("unexpected heartbeat: %+v", handler)
	}
}

func TestClientAdapter_GetMerkleRoot_Success(t *testing.T) {
	adapter, handler, _, rt := newTestClientAdapter(t)
	defer rt.Close()

	const peerID node.NodeID = "node-1"
	const corrID uint64 = 46

	if err := adapter.GetMerkleRoot(peerID, corrID); err != nil {
		t.Fatalf("GetMerkleRoot: %v", err)
	}

	expectedRoot := []byte("merkle-root-sha256")
	resp := wire.GetMerkleRootResponse{Status: wire.StatusOK, Root: expectedRoot}
	respBytes, _ := resp.Marshal()
	adapter.OnFrame(peerID, iouring.FrameHeader{MessageID: uint16(wire.MsgGetMerkleRootResponse), CorrelationID: corrID}, respBytes)

	if handler.merkleCorr != corrID || !bytes.Equal(handler.merkleRoot, expectedRoot) {
		t.Fatalf("unexpected merkle root: %+v", handler)
	}
}

func TestClientAdapter_NotifyLeaving_Success(t *testing.T) {
	adapter, handler, _, rt := newTestClientAdapter(t)
	defer rt.Close()

	const peerID node.NodeID = "node-1"
	const corrID uint64 = 47

	if err := adapter.NotifyLeaving(peerID, corrID); err != nil {
		t.Fatalf("NotifyLeaving: %v", err)
	}

	resp := wire.NotifyLeavingResponse{Status: wire.StatusOK}
	respBytes, _ := resp.Marshal()
	adapter.OnFrame(peerID, iouring.FrameHeader{MessageID: uint16(wire.MsgNotifyLeavingResponse), CorrelationID: corrID}, respBytes)

	if handler.notifyCorr != corrID || handler.notifyStatus != wire.StatusOK {
		t.Fatalf("unexpected notify leaving: %+v", handler)
	}
}

func TestClientAdapter_GossipExchange_Success(t *testing.T) {
	adapter, handler, _, rt := newTestClientAdapter(t)
	defer rt.Close()

	const peerID node.NodeID = "node-1"
	const corrID uint64 = 48
	entries := []GossipEntry{
		{NodeID: "node-1", Addr: "127.0.0.1:8001", Status: 1, Version: 10, UpdatedAt: 12345},
	}

	if err := adapter.GossipExchange(peerID, corrID, entries); err != nil {
		t.Fatalf("GossipExchange: %v", err)
	}

	replyEntries := []GossipEntry{
		{NodeID: "node-2", Addr: "127.0.0.1:8002", Status: 1, Version: 15, UpdatedAt: 12350},
	}
	resp := wire.GossipExchangeResponse{Entries: replyEntries}
	respBytes, _ := resp.Marshal()
	adapter.OnFrame(peerID, iouring.FrameHeader{MessageID: uint16(wire.MsgGossipExchangeResponse), CorrelationID: corrID}, respBytes)

	if handler.gossipCorr != corrID || len(handler.gossipEntries) != 1 || handler.gossipEntries[0].NodeID != "node-2" {
		t.Fatalf("unexpected gossip entries: %+v", handler.gossipEntries)
	}
}

func TestClientAdapter_Hooks(t *testing.T) {
	adapter, handler, _, rt := newTestClientAdapter(t)
	defer rt.Close()

	var connectedID node.NodeID
	var disconnectedID node.NodeID
	var connectErrID node.NodeID
	var unsolicitedMsgID uint16
	var unsolicitedBody []byte

	adapter.OnConnectedHook = func(id node.NodeID, addr string) { connectedID = id }
	adapter.OnDisconnectedHook = func(id node.NodeID, err error) { disconnectedID = id }
	adapter.OnConnectErrorHook = func(id node.NodeID, err error) { connectErrID = id }
	adapter.OnMessageHook = func(id node.NodeID, hdr iouring.FrameHeader, body []byte) {
		unsolicitedMsgID = hdr.MessageID
		unsolicitedBody = body
	}

	adapter.OnConnected("node-1", "127.0.0.1:9000")
	if connectedID != "node-1" || handler.connectedPeer != "node-1" {
		t.Fatalf("expected node-1, got hook=%s handler=%s", connectedID, handler.connectedPeer)
	}

	adapter.OnDisconnected("node-1", errors.New("lost"))
	if disconnectedID != "node-1" || handler.disconnectedPeer != "node-1" {
		t.Fatalf("expected node-1, got hook=%s handler=%s", disconnectedID, handler.disconnectedPeer)
	}

	adapter.OnConnectError("node-2", errors.New("refused"))
	if connectErrID != "node-2" || handler.connectErrPeer != "node-2" {
		t.Fatalf("expected node-2, got hook=%s handler=%s", connectErrID, handler.connectErrPeer)
	}

	adapter.OnFrame("node-3", iouring.FrameHeader{MessageID: 99, CorrelationID: 999}, []byte("unsolicited"))
	if unsolicitedMsgID != 99 || !bytes.Equal(unsolicitedBody, []byte("unsolicited")) {
		t.Fatalf("unexpected unsolicited: %d, %s", unsolicitedMsgID, unsolicitedBody)
	}
}
