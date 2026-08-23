package transport

import (
	"bytes"
	"errors"
	"net"
	"testing"
	"time"

	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/contracts/quorumerr"
	"goquorum.io/v2/contracts/vclock"
	"goquorum.io/v2/contracts/wire"
	"goquorum.io/v2/engine/adapter/storage"
	"goquorum.io/v2/engine/reactor"
	"goquorum.io/v2/infra/ioruntime"
	"goquorum.io/v2/infra/transport/iouring"
)

type fakeEventSource struct{}

func (f *fakeEventSource) Poll(dst []reactor.Event, deadline time.Time) ([]reactor.Event, error) {
	time.Sleep(10 * time.Millisecond)
	return dst, nil
}
func (f *fakeEventSource) Wake() error  { return nil }
func (f *fakeEventSource) Close() error { return nil }

func newTestAdapter(t *testing.T) (*Adapter, *reactor.Reactor, *ioruntime.Runtime) {
	t.Helper()
	rt, err := ioruntime.New(64)
	if err != nil {
		t.Fatalf("ioruntime.New: %v", err)
	}
	r := reactor.New(rt)
	client := iouring.NewClient(rt, r, nil, nil)
	adapter := NewAdapter(client, r)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	_ = client.Dial("node-1", ln.Addr().String())

	return adapter, r, rt
}

func TestAdapter_RemotePut_Success(t *testing.T) {
	adapter, r, rt := newTestAdapter(t)
	defer rt.Close()

	const peerID node.NodeID = "node-1"
	key := []byte("key-put")
	vc := vclock.NewVectorClock()
	vc.Set("node-1", 1)
	siblings := &storage.SiblingSet{
		Siblings: []storage.Sibling{
			{Value: []byte("val1"), VClock: vc, Timestamp: 100},
		},
	}

	var doneErr error
	var doneCalled bool
	adapter.RemotePut(peerID, key, siblings, func(err error) {
		doneCalled = true
		doneErr = err
	})

	slotID := adapter.nextReqID

	// Simulate reply frame
	resp := wire.RemotePutResponse{Status: wire.StatusOK}
	respBytes, err := resp.Marshal()
	if err != nil {
		t.Fatal(err)
	}

	adapter.OnFrame(peerID, iouring.FrameHeader{MessageID: uint16(wire.MsgRemotePutResponse), CorrelationID: slotID}, respBytes)

	if !doneCalled {
		t.Fatal("expected done callback to be invoked")
	}
	if doneErr != nil {
		t.Fatalf("expected nil error, got: %v", doneErr)
	}
	_ = r
}

func TestAdapter_RemoteGet_Success(t *testing.T) {
	adapter, _, rt := newTestAdapter(t)
	defer rt.Close()

	const peerID node.NodeID = "node-1"
	key := []byte("key-get")

	var gotSiblings *storage.SiblingSet
	var doneErr error
	var doneCalled bool

	adapter.RemoteGet(peerID, key, func(ss *storage.SiblingSet, err error) {
		doneCalled = true
		gotSiblings = ss
		doneErr = err
	})

	slotID := adapter.nextReqID

	// Simulate reply frame
	vc := vclock.NewVectorClock()
	vc.Set("node-1", 2)
	resp := wire.RemoteGetResponse{
		Status: wire.StatusOK,
		Siblings: &storage.SiblingSet{
			Siblings: []storage.Sibling{
				{Value: []byte("get-val"), VClock: vc, Timestamp: 200},
			},
		},
	}
	respBytes, err := resp.Marshal()
	if err != nil {
		t.Fatal(err)
	}

	adapter.OnFrame(peerID, iouring.FrameHeader{MessageID: uint16(wire.MsgRemoteGetResponse), CorrelationID: slotID}, respBytes)

	if !doneCalled {
		t.Fatal("expected done callback to be invoked")
	}
	if doneErr != nil {
		t.Fatalf("expected nil error, got: %v", doneErr)
	}
	if gotSiblings == nil || len(gotSiblings.Siblings) != 1 || !bytes.Equal(gotSiblings.Siblings[0].Value, []byte("get-val")) {
		t.Fatalf("unexpected siblings: %+v", gotSiblings)
	}
}

func TestAdapter_RemoteGet_KeyNotFound(t *testing.T) {
	adapter, _, rt := newTestAdapter(t)
	defer rt.Close()

	const peerID node.NodeID = "node-1"
	key := []byte("missing-key")

	var doneErr error
	var doneCalled bool

	adapter.RemoteGet(peerID, key, func(ss *storage.SiblingSet, err error) {
		doneCalled = true
		doneErr = err
	})

	slotID := adapter.nextReqID

	resp := wire.RemoteGetResponse{Status: wire.StatusKeyNotFound}
	respBytes, err := resp.Marshal()
	if err != nil {
		t.Fatal(err)
	}

	adapter.OnFrame(peerID, iouring.FrameHeader{MessageID: uint16(wire.MsgRemoteGetResponse), CorrelationID: slotID}, respBytes)

	if !doneCalled {
		t.Fatal("expected done callback")
	}
	if !errors.Is(doneErr, quorumerr.ErrKeyNotFound) {
		t.Fatalf("expected ErrKeyNotFound, got: %v", doneErr)
	}
}

func TestAdapter_Heartbeat_Success(t *testing.T) {
	adapter, _, rt := newTestAdapter(t)
	defer rt.Close()

	const peerID node.NodeID = "node-1"
	var doneErr error
	var doneCalled bool

	adapter.Heartbeat(peerID, func(err error) {
		doneCalled = true
		doneErr = err
	})

	slotID := adapter.nextReqID
	resp := wire.HeartbeatResponse{Status: wire.StatusOK}
	respBytes, _ := resp.Marshal()
	adapter.OnFrame(peerID, iouring.FrameHeader{MessageID: uint16(wire.MsgHeartbeatResponse), CorrelationID: slotID}, respBytes)

	if !doneCalled || doneErr != nil {
		t.Fatalf("doneCalled=%v, err=%v", doneCalled, doneErr)
	}
}

func TestAdapter_GetMerkleRoot_Success(t *testing.T) {
	adapter, _, rt := newTestAdapter(t)
	defer rt.Close()

	const peerID node.NodeID = "node-1"
	var gotRoot []byte
	var doneErr error

	adapter.GetMerkleRoot(peerID, func(root []byte, err error) {
		gotRoot = root
		doneErr = err
	})

	slotID := adapter.nextReqID
	expectedRoot := []byte("merkle-root-sha256")
	resp := wire.GetMerkleRootResponse{Status: wire.StatusOK, Root: expectedRoot}
	respBytes, _ := resp.Marshal()
	adapter.OnFrame(peerID, iouring.FrameHeader{MessageID: uint16(wire.MsgGetMerkleRootResponse), CorrelationID: slotID}, respBytes)

	if doneErr != nil || !bytes.Equal(gotRoot, expectedRoot) {
		t.Fatalf("err=%v, gotRoot=%q", doneErr, gotRoot)
	}
}

func TestAdapter_NotifyLeaving_Success(t *testing.T) {
	adapter, _, rt := newTestAdapter(t)
	defer rt.Close()

	const peerID node.NodeID = "node-1"
	var doneErr error
	var doneCalled bool

	adapter.NotifyLeaving(peerID, func(err error) {
		doneCalled = true
		doneErr = err
	})

	slotID := adapter.nextReqID
	resp := wire.NotifyLeavingResponse{Status: wire.StatusOK}
	respBytes, _ := resp.Marshal()
	adapter.OnFrame(peerID, iouring.FrameHeader{MessageID: uint16(wire.MsgNotifyLeavingResponse), CorrelationID: slotID}, respBytes)

	if !doneCalled || doneErr != nil {
		t.Fatalf("doneCalled=%v, err=%v", doneCalled, doneErr)
	}
}

func TestAdapter_GossipExchange_Success(t *testing.T) {
	adapter, _, rt := newTestAdapter(t)
	defer rt.Close()

	const peerID node.NodeID = "node-1"
	entries := []GossipEntry{
		{NodeID: "node-1", Addr: "127.0.0.1:8001", Status: 1, Version: 10, UpdatedAt: 1000},
	}

	var gotEntries []GossipEntry
	var doneErr error

	adapter.GossipExchange(peerID, entries, func(reply []GossipEntry, err error) {
		gotEntries = reply
		doneErr = err
	})

	slotID := adapter.nextReqID
	replyEntries := []GossipEntry{
		{NodeID: "node-2", Addr: "127.0.0.1:8002", Status: 1, Version: 15, UpdatedAt: 2000},
	}
	resp := wire.GossipExchangeResponse{Entries: replyEntries}
	respBytes, _ := resp.Marshal()
	adapter.OnFrame(peerID, iouring.FrameHeader{MessageID: uint16(wire.MsgGossipExchangeResponse), CorrelationID: slotID}, respBytes)

	if doneErr != nil || len(gotEntries) != 1 || gotEntries[0].NodeID != "node-2" {
		t.Fatalf("err=%v, gotEntries=%+v", doneErr, gotEntries)
	}
}

func TestAdapter_Hooks(t *testing.T) {
	adapter, _, rt := newTestAdapter(t)
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
	if connectedID != "node-1" {
		t.Fatalf("expected node-1, got %s", connectedID)
	}

	adapter.OnDisconnected("node-1", errors.New("lost"))
	if disconnectedID != "node-1" {
		t.Fatalf("expected node-1, got %s", disconnectedID)
	}

	adapter.OnConnectError("node-2", errors.New("refused"))
	if connectErrID != "node-2" {
		t.Fatalf("expected node-2, got %s", connectErrID)
	}

	adapter.OnFrame("node-3", iouring.FrameHeader{MessageID: 99, CorrelationID: 999}, []byte("unsolicited"))
	if unsolicitedMsgID != 99 || !bytes.Equal(unsolicitedBody, []byte("unsolicited")) {
		t.Fatalf("unexpected unsolicited: %d, %s", unsolicitedMsgID, unsolicitedBody)
	}
}
