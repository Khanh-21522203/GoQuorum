package adapter

import (
	"errors"
	"fmt"
	"time"

	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/contracts/wire"
	"goquorum.io/v2/engine/reactor"
	"goquorum.io/v2/infra/pool"
	"goquorum.io/v2/infra/transport/iouring"
)

// GossipEntry represents a peer's heartbeat state gossiped between nodes.
type GossipEntry = wire.GossipEntry

// Transport is the outbound networking port used by the engine layer to communicate with peers.
type Transport interface {
	// RemotePut sends a write replication request to node id and invokes done on reply or error.
	RemotePut(id node.NodeID, key []byte, siblings *SiblingSet, done func(error))
	// RemoteGet reads a key's sibling set from node id.
	RemoteGet(id node.NodeID, key []byte, done func(*SiblingSet, error))
	// Heartbeat sends a heartbeat ping to node id.
	Heartbeat(id node.NodeID, done func(error))
	// GetMerkleRoot requests node id's Merkle tree root hash for anti-entropy sync.
	GetMerkleRoot(id node.NodeID, done func(root []byte, err error))
	// NotifyLeaving informs node id that the local node is leaving gracefully.
	NotifyLeaving(id node.NodeID, done func(error))
	// GossipExchange exchanges gossip state with node id.
	GossipExchange(id node.NodeID, entries []GossipEntry, done func([]GossipEntry, error))
	// Dial initiates an asynchronous TCP connection to addr for node id.
	Dial(id node.NodeID, addr string) error
	// Close releases all connections held by the transport.
	Close() error
}

type rpcType uint8

const (
	rpcRemotePut rpcType = iota + 1
	rpcRemoteGet
	rpcHeartbeat
	rpcGetMerkleRoot
	rpcNotifyLeaving
	rpcGossipExchange
)

type pendingRPC struct {
	rpcType      rpcType
	timer        reactor.TimerID
	onErrDone    func(error)
	onGetDone    func(*SiblingSet, error)
	onMerkleDone func([]byte, error)
	onGossipDone func([]GossipEntry, error)
}

const defaultRequestTimeout = 5 * time.Second

var errConnClosed = errors.New("transport: connection closed")

// TransportAdapter adapts an event-driven iouring.Client into a domain Transport engine.
type TransportAdapter struct {
	client         *iouring.Client
	r              *reactor.Reactor
	bytePool       *pool.BucketArrayPool[byte]
	slots          *pool.SlotTable[pendingRPC]
	nextReqID      uint64
	requestTimeout time.Duration

	// Hooks for connection lifecycle and unhandled inbound frames
	OnConnectedHook    func(id node.NodeID, addr string)
	OnDisconnectedHook func(id node.NodeID, err error)
	OnConnectErrorHook func(id node.NodeID, err error)
	OnMessageHook      func(id node.NodeID, hdr iouring.FrameHeader, body []byte)
}

var _ Transport = (*TransportAdapter)(nil)
var _ iouring.ClientHandler = (*TransportAdapter)(nil)

// NewTransportAdapter creates a new Transport adapter over an event-driven iouring.Client.
func NewTransportAdapter(client *iouring.Client, r *reactor.Reactor) *TransportAdapter {
	bp := client.BytePool()
	if bp == nil {
		bp = pool.NewDefaultArrayPool[byte]()
	}
	a := &TransportAdapter{
		client:         client,
		r:              r,
		bytePool:       bp,
		slots:          pool.NewSlotTable[pendingRPC](1024),
		requestTimeout: defaultRequestTimeout,
	}
	client.SetHandler(a)
	return a
}

// BytePool returns the byte buffer pool used by this TransportAdapter.
func (a *TransportAdapter) BytePool() *pool.BucketArrayPool[byte] {
	return a.bytePool
}

// SetRequestTimeout sets the per-RPC request timeout duration.
func (a *TransportAdapter) SetRequestTimeout(d time.Duration) {
	a.requestTimeout = d
}

// Dial establishes an outbound TCP connection to peer id at addr.
func (a *TransportAdapter) Dial(id node.NodeID, addr string) error {
	return a.client.Dial(id, addr)
}

// HandleCompletion routes io_uring CQE events to the client.
func (a *TransportAdapter) HandleCompletion(ev reactor.Event) bool {
	return a.client.HandleCompletion(ev)
}

// OnFrame implements iouring.ClientHandler with zero closure allocations.
func (a *TransportAdapter) OnFrame(id node.NodeID, hdr iouring.FrameHeader, body []byte) {
	slot, ok := a.slots.Get(hdr.CorrelationID)
	if !ok {
		if a.OnMessageHook != nil {
			a.OnMessageHook(id, hdr, body)
		}
		return
	}
	defer a.slots.Release(hdr.CorrelationID)
	a.r.CancelTimer(slot.Value.timer)

	switch slot.Value.rpcType {
	case rpcRemotePut:
		var resp wire.RemotePutResponse
		if uErr := resp.Unmarshal(body); uErr != nil {
			if slot.Value.onErrDone != nil {
				slot.Value.onErrDone(uErr)
			}
			return
		}
		if slot.Value.onErrDone != nil {
			slot.Value.onErrDone(wire.StatusCodeToError(resp.Status))
		}

	case rpcRemoteGet:
		var resp wire.RemoteGetResponse
		if uErr := resp.Unmarshal(body); uErr != nil {
			if slot.Value.onGetDone != nil {
				slot.Value.onGetDone(nil, uErr)
			}
			return
		}
		if resp.Status != wire.StatusOK {
			if slot.Value.onGetDone != nil {
				slot.Value.onGetDone(nil, wire.StatusCodeToError(resp.Status))
			}
			return
		}
		if slot.Value.onGetDone != nil {
			slot.Value.onGetDone(resp.Siblings, nil)
		}

	case rpcHeartbeat:
		var resp wire.HeartbeatResponse
		if uErr := resp.Unmarshal(body); uErr != nil {
			if slot.Value.onErrDone != nil {
				slot.Value.onErrDone(uErr)
			}
			return
		}
		if slot.Value.onErrDone != nil {
			slot.Value.onErrDone(wire.StatusCodeToError(resp.Status))
		}

	case rpcGetMerkleRoot:
		var resp wire.GetMerkleRootResponse
		if uErr := resp.Unmarshal(body); uErr != nil {
			if slot.Value.onMerkleDone != nil {
				slot.Value.onMerkleDone(nil, uErr)
			}
			return
		}
		if resp.Status != wire.StatusOK {
			if slot.Value.onMerkleDone != nil {
				slot.Value.onMerkleDone(nil, wire.StatusCodeToError(resp.Status))
			}
			return
		}
		if slot.Value.onMerkleDone != nil {
			slot.Value.onMerkleDone(resp.Root, nil)
		}

	case rpcNotifyLeaving:
		var resp wire.NotifyLeavingResponse
		if uErr := resp.Unmarshal(body); uErr != nil {
			if slot.Value.onErrDone != nil {
				slot.Value.onErrDone(uErr)
			}
			return
		}
		if slot.Value.onErrDone != nil {
			slot.Value.onErrDone(wire.StatusCodeToError(resp.Status))
		}

	case rpcGossipExchange:
		var resp wire.GossipExchangeResponse
		if uErr := resp.Unmarshal(body); uErr != nil {
			if slot.Value.onGossipDone != nil {
				slot.Value.onGossipDone(nil, uErr)
			}
			return
		}
		if slot.Value.onGossipDone != nil {
			slot.Value.onGossipDone(resp.Entries, nil)
		}
	}
}

func (a *TransportAdapter) dispatchError(p pendingRPC, err error) {
	switch p.rpcType {
	case rpcRemotePut, rpcHeartbeat, rpcNotifyLeaving:
		if p.onErrDone != nil {
			p.onErrDone(err)
		}
	case rpcRemoteGet:
		if p.onGetDone != nil {
			p.onGetDone(nil, err)
		}
	case rpcGetMerkleRoot:
		if p.onMerkleDone != nil {
			p.onMerkleDone(nil, err)
		}
	case rpcGossipExchange:
		if p.onGossipDone != nil {
			p.onGossipDone(nil, err)
		}
	}
}

// OnConnected implements iouring.ClientHandler.
func (a *TransportAdapter) OnConnected(id node.NodeID, addr string) {
	if a.OnConnectedHook != nil {
		a.OnConnectedHook(id, addr)
	}
}

// OnDisconnected implements iouring.ClientHandler.
func (a *TransportAdapter) OnDisconnected(id node.NodeID, err error) {
	if a.OnDisconnectedHook != nil {
		a.OnDisconnectedHook(id, err)
	}
}

// OnConnectError implements iouring.ClientHandler.
func (a *TransportAdapter) OnConnectError(id node.NodeID, err error) {
	if a.OnConnectErrorHook != nil {
		a.OnConnectErrorHook(id, err)
	}
}

// RemotePut replicates a write to node id.
func (a *TransportAdapter) RemotePut(id node.NodeID, key []byte, siblings *SiblingSet, done func(error)) {
	a.nextReqID++
	slotID := a.nextReqID
	slot := a.slots.Acquire(slotID)

	timer := a.r.ScheduleOnce(a.requestTimeout, func() {
		s, ok := a.slots.Get(slotID)
		if !ok {
			return
		}
		defer a.slots.Release(slotID)
		if s.Value.onErrDone != nil {
			s.Value.onErrDone(fmt.Errorf("transport: remote put to %s: timed out waiting for reply", id))
		}
	})

	slot.Value = pendingRPC{
		rpcType:   rpcRemotePut,
		timer:     timer,
		onErrDone: done,
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
func (a *TransportAdapter) RemoteGet(id node.NodeID, key []byte, done func(*SiblingSet, error)) {
	a.nextReqID++
	slotID := a.nextReqID
	slot := a.slots.Acquire(slotID)

	timer := a.r.ScheduleOnce(a.requestTimeout, func() {
		s, ok := a.slots.Get(slotID)
		if !ok {
			return
		}
		defer a.slots.Release(slotID)
		if s.Value.onGetDone != nil {
			s.Value.onGetDone(nil, fmt.Errorf("transport: remote get to %s: timed out waiting for reply", id))
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
		defer a.slots.Release(slotID)
		if s.Value.onErrDone != nil {
			s.Value.onErrDone(fmt.Errorf("transport: heartbeat to %s: timed out waiting for reply", id))
		}
	})

	slot.Value = pendingRPC{
		rpcType:   rpcHeartbeat,
		timer:     timer,
		onErrDone: done,
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
		defer a.slots.Release(slotID)
		if s.Value.onMerkleDone != nil {
			s.Value.onMerkleDone(nil, fmt.Errorf("transport: get merkle root to %s: timed out waiting for reply", id))
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
		defer a.slots.Release(slotID)
		if s.Value.onErrDone != nil {
			s.Value.onErrDone(fmt.Errorf("transport: notify leaving to %s: timed out waiting for reply", id))
		}
	})

	slot.Value = pendingRPC{
		rpcType:   rpcNotifyLeaving,
		timer:     timer,
		onErrDone: done,
	}

	if err := a.client.Request(id, uint16(wire.MsgNotifyLeavingRequest), slotID, nil); err != nil {
		a.r.CancelTimer(timer)
		a.slots.Release(slotID)
		done(err)
	}
}

// GossipExchange sends the local node's gossip state to node id and returns its reply.
func (a *TransportAdapter) GossipExchange(id node.NodeID, entries []GossipEntry, done func([]GossipEntry, error)) {
	a.nextReqID++
	slotID := a.nextReqID
	slot := a.slots.Acquire(slotID)

	timer := a.r.ScheduleOnce(a.requestTimeout, func() {
		s, ok := a.slots.Get(slotID)
		if !ok {
			return
		}
		defer a.slots.Release(slotID)
		if s.Value.onGossipDone != nil {
			s.Value.onGossipDone(nil, fmt.Errorf("transport: gossip exchange to %s: timed out waiting for reply", id))
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
