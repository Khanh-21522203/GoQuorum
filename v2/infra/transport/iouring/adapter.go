package iouring

import (
	"fmt"
	"time"

	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/contracts/wire"
	"goquorum.io/v2/engine/reactor"
	"goquorum.io/v2/engine/storage"
	"goquorum.io/v2/engine/transport"
	"goquorum.io/v2/infra/pool"
)

// defaultRequestTimeout bounds RPC reply wait duration.
const defaultRequestTimeout = 5 * time.Second

type pendingRPCType uint8

const (
	rpcRemotePut pendingRPCType = iota + 1
	rpcRemoteGet
	rpcHeartbeat
	rpcGetMerkleRoot
	rpcNotifyLeaving
	rpcGossipExchange
)

type pendingRPC struct {
	rpcType         pendingRPCType
	timer           reactor.TimerID
	onPutDone       func(error)
	onGetDone       func(*storage.SiblingSet, error)
	onHeartbeatDone func(error)
	onMerkleDone    func([]byte, error)
	onLeavingDone   func(error)
	onGossipDone    func([]transport.GossipEntry, error)
}

// TransportAdapter bridges the domain engine/transport.Transport port to the
// pure io_uring Client using static event hookbacks and zero-allocation codecs.
type TransportAdapter struct {
	client         *Client
	r              *reactor.Reactor
	bytePool       *pool.BucketArrayPool[byte]
	slots          *pool.SlotTable[pendingRPC]
	nextReqID      uint64
	requestTimeout time.Duration

	// OnMessageHook is invoked when an unsolicited (non-RPC reply) framed message arrives.
	OnMessageHook func(id node.NodeID, hdr FrameHeader, body []byte)

	// OnConnectedHook is invoked when a connection to peer id is established.
	OnConnectedHook func(id node.NodeID, addr string)

	// OnDisconnectedHook is invoked when a connection to peer id dies.
	OnDisconnectedHook func(id node.NodeID, err error)

	// OnConnectErrorHook is invoked when connecting to peer id fails.
	OnConnectErrorHook func(id node.NodeID, err error)
}

var _ transport.Transport = (*TransportAdapter)(nil)
var _ ClientHandler = (*TransportAdapter)(nil)

// NewTransportAdapter creates a new TransportAdapter wrapping client.
func NewTransportAdapter(client *Client, r *reactor.Reactor) *TransportAdapter {
	a := &TransportAdapter{
		client:         client,
		r:              r,
		bytePool:       pool.NewDefaultArrayPool[byte](),
		slots:          pool.NewSlotTable[pendingRPC](1024),
		requestTimeout: defaultRequestTimeout,
	}
	client.handler = a
	return a
}

// Dial establishes an outbound TCP connection to peer id at addr.
func (a *TransportAdapter) Dial(id node.NodeID, addr string) error {
	return a.client.Dial(id, addr)
}

// HandleCompletion routes io_uring CQE events to the client.
func (a *TransportAdapter) HandleCompletion(ev reactor.Event) bool {
	return a.client.HandleCompletion(ev)
}

// OnFrame implements ClientHandler with zero closure allocations.
func (a *TransportAdapter) OnFrame(id node.NodeID, hdr FrameHeader, body []byte) {
	slot, ok := a.slots.Get(hdr.CorrelationID)
	if !ok {
		if a.OnMessageHook != nil {
			a.OnMessageHook(id, hdr, body)
		}
		return
	}

	p := slot.Value
	a.slots.Release(hdr.CorrelationID)
	a.r.CancelTimer(p.timer)

	switch p.rpcType {
	case rpcRemotePut:
		var resp wire.RemotePutResponse
		if uErr := resp.Unmarshal(body); uErr != nil {
			p.onPutDone(uErr)
			return
		}
		p.onPutDone(wire.StatusCodeToError(resp.Status))

	case rpcRemoteGet:
		var resp wire.RemoteGetResponse
		if uErr := resp.Unmarshal(body); uErr != nil {
			p.onGetDone(nil, uErr)
			return
		}
		if resp.Status != wire.StatusOK {
			p.onGetDone(nil, wire.StatusCodeToError(resp.Status))
			return
		}
		p.onGetDone(resp.Siblings, nil)

	case rpcHeartbeat:
		var resp wire.HeartbeatResponse
		if uErr := resp.Unmarshal(body); uErr != nil {
			p.onHeartbeatDone(uErr)
			return
		}
		p.onHeartbeatDone(wire.StatusCodeToError(resp.Status))

	case rpcGetMerkleRoot:
		var resp wire.GetMerkleRootResponse
		if uErr := resp.Unmarshal(body); uErr != nil {
			p.onMerkleDone(nil, uErr)
			return
		}
		if resp.Status != wire.StatusOK {
			p.onMerkleDone(nil, wire.StatusCodeToError(resp.Status))
			return
		}
		p.onMerkleDone(resp.Root, nil)

	case rpcNotifyLeaving:
		var resp wire.NotifyLeavingResponse
		if uErr := resp.Unmarshal(body); uErr != nil {
			p.onLeavingDone(uErr)
			return
		}
		p.onLeavingDone(wire.StatusCodeToError(resp.Status))

	case rpcGossipExchange:
		var resp wire.GossipExchangeResponse
		if uErr := resp.Unmarshal(body); uErr != nil {
			p.onGossipDone(nil, uErr)
			return
		}
		p.onGossipDone(resp.Entries, nil)
	}
}

func (a *TransportAdapter) dispatchError(p pendingRPC, err error) {
	switch p.rpcType {
	case rpcRemotePut:
		if p.onPutDone != nil {
			p.onPutDone(err)
		}
	case rpcRemoteGet:
		if p.onGetDone != nil {
			p.onGetDone(nil, err)
		}
	case rpcHeartbeat:
		if p.onHeartbeatDone != nil {
			p.onHeartbeatDone(err)
		}
	case rpcGetMerkleRoot:
		if p.onMerkleDone != nil {
			p.onMerkleDone(nil, err)
		}
	case rpcNotifyLeaving:
		if p.onLeavingDone != nil {
			p.onLeavingDone(err)
		}
	case rpcGossipExchange:
		if p.onGossipDone != nil {
			p.onGossipDone(nil, err)
		}
	}
}

func (a *TransportAdapter) OnConnected(id node.NodeID, addr string) {
	if a.OnConnectedHook != nil {
		a.OnConnectedHook(id, addr)
	}
}

func (a *TransportAdapter) OnDisconnected(id node.NodeID, err error) {
	if a.OnDisconnectedHook != nil {
		a.OnDisconnectedHook(id, err)
	}
}

func (a *TransportAdapter) OnConnectError(id node.NodeID, err error) {
	if a.OnConnectErrorHook != nil {
		a.OnConnectErrorHook(id, err)
	}
}

// RemotePut replicates a write to node id.
func (a *TransportAdapter) RemotePut(id node.NodeID, key []byte, siblings *storage.SiblingSet, done func(error)) {
	a.nextReqID++
	slotID := a.nextReqID
	slot := a.slots.Acquire(slotID)

	timer := a.r.ScheduleOnce(a.requestTimeout, func() {
		s, ok := a.slots.Get(slotID)
		if !ok {
			return
		}
		cb := s.Value.onPutDone
		a.slots.Release(slotID)
		if cb != nil {
			cb(fmt.Errorf("iouring: remote put to %s: timed out waiting for reply", id))
		}
	})

	slot.Value = pendingRPC{
		rpcType:   rpcRemotePut,
		timer:     timer,
		onPutDone: done,
	}

	req := wire.RemotePutRequest{Key: key, Siblings: siblings}
	estimatedLen := len(key) + 64
	buf := a.bytePool.Rent(estimatedLen)
	body, err := req.AppendMarshalBinary(buf[:0])
	if err != nil {
		a.r.CancelTimer(timer)
		a.slots.Release(slotID)
		a.bytePool.Return(buf)
		done(err)
		return
	}

	if err := a.client.Request(id, uint16(wire.MsgRemotePutRequest), slotID, body); err != nil {
		a.r.CancelTimer(timer)
		a.slots.Release(slotID)
		a.bytePool.Return(buf)
		done(err)
		return
	}
	a.bytePool.Return(buf)
}

// RemoteGet reads a key's sibling set from node id.
func (a *TransportAdapter) RemoteGet(id node.NodeID, key []byte, done func(*storage.SiblingSet, error)) {
	a.nextReqID++
	slotID := a.nextReqID
	slot := a.slots.Acquire(slotID)

	timer := a.r.ScheduleOnce(a.requestTimeout, func() {
		s, ok := a.slots.Get(slotID)
		if !ok {
			return
		}
		cb := s.Value.onGetDone
		a.slots.Release(slotID)
		if cb != nil {
			cb(nil, fmt.Errorf("iouring: remote get to %s: timed out waiting for reply", id))
		}
	})

	slot.Value = pendingRPC{
		rpcType:   rpcRemoteGet,
		timer:     timer,
		onGetDone: done,
	}

	req := wire.RemoteGetRequest{Key: key}
	estimatedLen := len(key) + 8
	buf := a.bytePool.Rent(estimatedLen)
	body, err := req.AppendMarshalBinary(buf[:0])
	if err != nil {
		a.r.CancelTimer(timer)
		a.slots.Release(slotID)
		a.bytePool.Return(buf)
		done(nil, err)
		return
	}

	if err := a.client.Request(id, uint16(wire.MsgRemoteGetRequest), slotID, body); err != nil {
		a.r.CancelTimer(timer)
		a.slots.Release(slotID)
		a.bytePool.Return(buf)
		done(nil, err)
		return
	}
	a.bytePool.Return(buf)
}

// Heartbeat sends a heartbeat ping to node id.
func (a *TransportAdapter) Heartbeat(id node.NodeID, done func(error)) {
	a.nextReqID++
	slotID := a.nextReqID
	slot := a.slots.Acquire(slotID)

	timer := a.r.ScheduleOnce(a.requestTimeout, func() {
		s, ok := a.slots.Get(slotID)
		if !ok {
			return
		}
		cb := s.Value.onHeartbeatDone
		a.slots.Release(slotID)
		if cb != nil {
			cb(fmt.Errorf("iouring: heartbeat to %s: timed out waiting for reply", id))
		}
	})

	slot.Value = pendingRPC{
		rpcType:         rpcHeartbeat,
		timer:           timer,
		onHeartbeatDone: done,
	}

	if err := a.client.Request(id, uint16(wire.MsgHeartbeatRequest), slotID, nil); err != nil {
		a.r.CancelTimer(timer)
		a.slots.Release(slotID)
		done(err)
	}
}

// GetMerkleRoot fetches node id's current anti-entropy Merkle root.
func (a *TransportAdapter) GetMerkleRoot(id node.NodeID, done func([]byte, error)) {
	a.nextReqID++
	slotID := a.nextReqID
	slot := a.slots.Acquire(slotID)

	timer := a.r.ScheduleOnce(a.requestTimeout, func() {
		s, ok := a.slots.Get(slotID)
		if !ok {
			return
		}
		cb := s.Value.onMerkleDone
		a.slots.Release(slotID)
		if cb != nil {
			cb(nil, fmt.Errorf("iouring: get merkle root to %s: timed out waiting for reply", id))
		}
	})

	slot.Value = pendingRPC{
		rpcType:      rpcGetMerkleRoot,
		timer:        timer,
		onMerkleDone: done,
	}

	if err := a.client.Request(id, uint16(wire.MsgGetMerkleRootRequest), slotID, nil); err != nil {
		a.r.CancelTimer(timer)
		a.slots.Release(slotID)
		done(nil, err)
	}
}

// NotifyLeaving informs node id that the local node is leaving the cluster gracefully.
func (a *TransportAdapter) NotifyLeaving(id node.NodeID, done func(error)) {
	a.nextReqID++
	slotID := a.nextReqID
	slot := a.slots.Acquire(slotID)

	timer := a.r.ScheduleOnce(a.requestTimeout, func() {
		s, ok := a.slots.Get(slotID)
		if !ok {
			return
		}
		cb := s.Value.onLeavingDone
		a.slots.Release(slotID)
		if cb != nil {
			cb(fmt.Errorf("iouring: notify leaving to %s: timed out waiting for reply", id))
		}
	})

	slot.Value = pendingRPC{
		rpcType:       rpcNotifyLeaving,
		timer:         timer,
		onLeavingDone: done,
	}

	if err := a.client.Request(id, uint16(wire.MsgNotifyLeavingRequest), slotID, nil); err != nil {
		a.r.CancelTimer(timer)
		a.slots.Release(slotID)
		done(err)
	}
}

// GossipExchange sends the local node's gossip state to node id and returns its reply.
func (a *TransportAdapter) GossipExchange(id node.NodeID, entries []transport.GossipEntry, done func([]transport.GossipEntry, error)) {
	a.nextReqID++
	slotID := a.nextReqID
	slot := a.slots.Acquire(slotID)

	timer := a.r.ScheduleOnce(a.requestTimeout, func() {
		s, ok := a.slots.Get(slotID)
		if !ok {
			return
		}
		cb := s.Value.onGossipDone
		a.slots.Release(slotID)
		if cb != nil {
			cb(nil, fmt.Errorf("iouring: gossip exchange to %s: timed out waiting for reply", id))
		}
	})

	slot.Value = pendingRPC{
		rpcType:      rpcGossipExchange,
		timer:        timer,
		onGossipDone: done,
	}

	req := wire.GossipExchangeRequest{Entries: entries}
	estimatedLen := 32*len(entries) + 4
	buf := a.bytePool.Rent(estimatedLen)
	body, err := req.AppendMarshalBinary(buf[:0])
	if err != nil {
		a.r.CancelTimer(timer)
		a.slots.Release(slotID)
		a.bytePool.Return(buf)
		done(nil, err)
		return
	}

	if err := a.client.Request(id, uint16(wire.MsgGossipExchangeRequest), slotID, body); err != nil {
		a.r.CancelTimer(timer)
		a.slots.Release(slotID)
		a.bytePool.Return(buf)
		done(nil, err)
		return
	}
	a.bytePool.Return(buf)
}

// Close releases every connection the client holds.
func (a *TransportAdapter) Close() error {
	a.slots.ForEach(func(id uint64, s *pool.Slot[pendingRPC]) {
		a.r.CancelTimer(s.Value.timer)
		a.dispatchError(s.Value, errConnClosed)
	})
	a.slots.Reset()
	return a.client.Close()
}
