package adapter

import (
	"errors"
	"fmt"
	"time"

	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/contracts/wire"
	"goquorum.io/v2/infra/pool"
	"goquorum.io/v2/infra/reactor"
	"goquorum.io/v2/infra/transport/iouring"
)

// GossipEntry represents a peer's heartbeat state gossiped between nodes.
type GossipEntry = wire.GossipEntry

// ClientAdapterHandler defines typed event hooks for all responses and connection events from peers.
type ClientAdapterHandler interface {
	// OnRemotePutResponse handles a write replication response from peerID.
	OnRemotePutResponse(peerID node.NodeID, corrID uint64, status wire.StatusCode)
	// OnRemoteGetResponse handles a read replication response from peerID.
	OnRemoteGetResponse(peerID node.NodeID, corrID uint64, siblings *SiblingSet, status wire.StatusCode)
	// OnHeartbeatResponse handles a heartbeat ping response from peerID.
	OnHeartbeatResponse(peerID node.NodeID, corrID uint64, status wire.StatusCode)
	// OnGetMerkleRootResponse handles a Merkle root response from peerID.
	OnGetMerkleRootResponse(peerID node.NodeID, corrID uint64, root []byte, status wire.StatusCode)
	// OnNotifyLeavingResponse handles a graceful leaving response from peerID.
	OnNotifyLeavingResponse(peerID node.NodeID, corrID uint64, status wire.StatusCode)
	// OnGossipExchangeResponse handles a gossip state digest response from peerID.
	OnGossipExchangeResponse(peerID node.NodeID, corrID uint64, entries []GossipEntry)
	// OnPeerConnected is invoked when an outbound TCP connection to peerID succeeds.
	OnPeerConnected(peerID node.NodeID, addr string)
	// OnPeerDisconnected is invoked when an outbound connection to peerID is dropped.
	OnPeerDisconnected(peerID node.NodeID, err error)
	// OnPeerConnectError is invoked when an outbound connection attempt to peerID fails.
	OnPeerConnectError(peerID node.NodeID, err error)
	// OnRPCError is invoked when an outbound RPC times out or suffers a transport error.
	OnRPCError(peerID node.NodeID, corrID uint64, rpcType uint16, err error)
}

// ClientTransport is the outbound networking port used by the engine layer to communicate with peers.
type ClientTransport interface {
	// RemotePut initiates a write replication request to node id with correlation ID corrID.
	RemotePut(id node.NodeID, corrID uint64, key []byte, siblings *SiblingSet) error
	// RemoteGet initiates a read request to node id with correlation ID corrID.
	RemoteGet(id node.NodeID, corrID uint64, key []byte) error
	// Heartbeat initiates a heartbeat ping to node id with correlation ID corrID.
	Heartbeat(id node.NodeID, corrID uint64) error
	// GetMerkleRoot initiates an anti-entropy Merkle root fetch with correlation ID corrID.
	GetMerkleRoot(id node.NodeID, corrID uint64) error
	// NotifyLeaving informs node id that the local node is leaving gracefully with correlation ID corrID.
	NotifyLeaving(id node.NodeID, corrID uint64) error
	// GossipExchange initiates a gossip exchange with node id with correlation ID corrID.
	GossipExchange(id node.NodeID, corrID uint64, entries []GossipEntry) error
	// Dial initiates an asynchronous TCP connection to addr for node id.
	Dial(id node.NodeID, addr string) error
	// Close releases all connections held by the transport.
	Close() error
}

type pendingClientRPC struct {
	rpcType uint16
	nodeID  node.NodeID
	timer   reactor.TimerID
}

const defaultRequestTimeout = 5 * time.Second

var errConnClosed = errors.New("transport: connection closed")

// ClientAdapter adapts an event-driven iouring.Client into a domain Transport engine using event hooks.
type ClientAdapter struct {
	client         *iouring.Client
	r              *reactor.Reactor
	bytePool       *pool.BucketArrayPool[byte]
	slots          *pool.SlotTable[pendingClientRPC]
	handler        ClientAdapterHandler
	requestTimeout time.Duration

	// Hooks for connection lifecycle and unhandled inbound frames
	OnConnectedHook    func(id node.NodeID, addr string)
	OnDisconnectedHook func(id node.NodeID, err error)
	OnConnectErrorHook func(id node.NodeID, err error)
	OnMessageHook      func(id node.NodeID, hdr iouring.FrameHeader, body []byte)
}

var _ ClientTransport = (*ClientAdapter)(nil)
var _ iouring.ClientHandler = (*ClientAdapter)(nil)

// NewClientAdapter creates a new ClientAdapter over an event-driven iouring.Client.
func NewClientAdapter(client *iouring.Client, r *reactor.Reactor) *ClientAdapter {
	bp := client.BytePool()
	if bp == nil {
		bp = pool.NewDefaultArrayPool[byte]()
	}
	a := &ClientAdapter{
		client:         client,
		r:              r,
		bytePool:       bp,
		slots:          pool.NewSlotTable[pendingClientRPC](1024),
		requestTimeout: defaultRequestTimeout,
	}
	client.SetHandler(a)
	return a
}

// SetHandler sets the event hook handler that receives peer replies and lifecycle events.
func (a *ClientAdapter) SetHandler(h ClientAdapterHandler) {
	a.handler = h
}

// SetInboundHandler sets the event hook handler (alias for SetHandler).
func (a *ClientAdapter) SetInboundHandler(h ClientAdapterHandler) {
	a.handler = h
}

// BytePool returns the byte buffer pool used by this ClientAdapter.
func (a *ClientAdapter) BytePool() *pool.BucketArrayPool[byte] {
	return a.bytePool
}

// SetRequestTimeout sets the per-RPC request timeout duration.
func (a *ClientAdapter) SetRequestTimeout(d time.Duration) {
	a.requestTimeout = d
}

// Dial establishes an outbound TCP connection to peer id at addr.
func (a *ClientAdapter) Dial(id node.NodeID, addr string) error {
	return a.client.Dial(id, addr)
}

// HandleCompletion routes io_uring CQE events to the client.
func (a *ClientAdapter) HandleCompletion(ev reactor.Event) bool {
	return a.client.HandleCompletion(ev)
}

// OnFrame implements iouring.ClientHandler with zero closure allocations, dispatching to ClientInboundHandler.
func (a *ClientAdapter) OnFrame(id node.NodeID, hdr iouring.FrameHeader, body []byte) {
	slot, ok := a.slots.Get(hdr.CorrelationID)
	if !ok {
		if a.OnMessageHook != nil {
			a.OnMessageHook(id, hdr, body)
		}
		return
	}
	defer a.slots.Release(hdr.CorrelationID)
	a.r.CancelTimer(slot.Value.timer)

	if a.handler == nil {
		return
	}

	switch slot.Value.rpcType {
	case uint16(wire.MsgRemotePutRequest):
		var resp wire.RemotePutResponse
		if uErr := resp.Unmarshal(body); uErr != nil {
			a.handler.OnRPCError(id, hdr.CorrelationID, slot.Value.rpcType, uErr)
			return
		}
		a.handler.OnRemotePutResponse(id, hdr.CorrelationID, resp.Status)

	case uint16(wire.MsgRemoteGetRequest):
		var resp wire.RemoteGetResponse
		if uErr := resp.Unmarshal(body); uErr != nil {
			a.handler.OnRPCError(id, hdr.CorrelationID, slot.Value.rpcType, uErr)
			return
		}
		a.handler.OnRemoteGetResponse(id, hdr.CorrelationID, resp.Siblings, resp.Status)

	case uint16(wire.MsgHeartbeatRequest):
		var resp wire.HeartbeatResponse
		if uErr := resp.Unmarshal(body); uErr != nil {
			a.handler.OnRPCError(id, hdr.CorrelationID, slot.Value.rpcType, uErr)
			return
		}
		a.handler.OnHeartbeatResponse(id, hdr.CorrelationID, resp.Status)

	case uint16(wire.MsgGetMerkleRootRequest):
		var resp wire.GetMerkleRootResponse
		if uErr := resp.Unmarshal(body); uErr != nil {
			a.handler.OnRPCError(id, hdr.CorrelationID, slot.Value.rpcType, uErr)
			return
		}
		a.handler.OnGetMerkleRootResponse(id, hdr.CorrelationID, resp.Root, resp.Status)

	case uint16(wire.MsgNotifyLeavingRequest):
		var resp wire.NotifyLeavingResponse
		if uErr := resp.Unmarshal(body); uErr != nil {
			a.handler.OnRPCError(id, hdr.CorrelationID, slot.Value.rpcType, uErr)
			return
		}
		a.handler.OnNotifyLeavingResponse(id, hdr.CorrelationID, resp.Status)

	case uint16(wire.MsgGossipExchangeRequest):
		var resp wire.GossipExchangeResponse
		if uErr := resp.Unmarshal(body); uErr != nil {
			a.handler.OnRPCError(id, hdr.CorrelationID, slot.Value.rpcType, uErr)
			return
		}
		a.handler.OnGossipExchangeResponse(id, hdr.CorrelationID, resp.Entries)
	}
}

// OnConnected implements iouring.ClientHandler.
func (a *ClientAdapter) OnConnected(id node.NodeID, addr string) {
	if a.OnConnectedHook != nil {
		a.OnConnectedHook(id, addr)
	}
	if a.handler != nil {
		a.handler.OnPeerConnected(id, addr)
	}
}

// OnDisconnected implements iouring.ClientHandler.
func (a *ClientAdapter) OnDisconnected(id node.NodeID, err error) {
	if a.OnDisconnectedHook != nil {
		a.OnDisconnectedHook(id, err)
	}
	if a.handler != nil {
		a.handler.OnPeerDisconnected(id, err)
	}
}

// OnConnectError implements iouring.ClientHandler.
func (a *ClientAdapter) OnConnectError(id node.NodeID, err error) {
	if a.OnConnectErrorHook != nil {
		a.OnConnectErrorHook(id, err)
	}
	if a.handler != nil {
		a.handler.OnPeerConnectError(id, err)
	}
}

// RemotePut replicates a write to node id with correlation ID corrID.
func (a *ClientAdapter) RemotePut(id node.NodeID, corrID uint64, key []byte, siblings *SiblingSet) error {
	slot := a.slots.Acquire(corrID)

	timer := a.r.ScheduleOnce(a.requestTimeout, func() {
		_, ok := a.slots.Get(corrID)
		if !ok {
			return
		}
		defer a.slots.Release(corrID)
		if a.handler != nil {
			a.handler.OnRPCError(id, corrID, uint16(wire.MsgRemotePutRequest), fmt.Errorf("transport: remote put to %s: timed out waiting for reply", id))
		}
	})

	slot.Value = pendingClientRPC{
		rpcType: uint16(wire.MsgRemotePutRequest),
		nodeID:  id,
		timer:   timer,
	}

	req := wire.RemotePutRequest{Key: key, Siblings: siblings}
	estimatedLen := len(key) + 64
	buf := a.bytePool.Rent(estimatedLen)
	body, err := req.AppendMarshalBinary(buf[:0])
	if err != nil {
		a.r.CancelTimer(timer)
		a.slots.Release(corrID)
		a.bytePool.Return(buf)
		return err
	}

	if err := a.client.Request(id, uint16(wire.MsgRemotePutRequest), corrID, body); err != nil {
		a.r.CancelTimer(timer)
		a.slots.Release(corrID)
		a.bytePool.Return(buf)
		return err
	}
	a.bytePool.Return(buf)
	return nil
}

// RemoteGet reads a key's sibling set from node id with correlation ID corrID.
func (a *ClientAdapter) RemoteGet(id node.NodeID, corrID uint64, key []byte) error {
	slot := a.slots.Acquire(corrID)

	timer := a.r.ScheduleOnce(a.requestTimeout, func() {
		_, ok := a.slots.Get(corrID)
		if !ok {
			return
		}
		defer a.slots.Release(corrID)
		if a.handler != nil {
			a.handler.OnRPCError(id, corrID, uint16(wire.MsgRemoteGetRequest), fmt.Errorf("transport: remote get to %s: timed out waiting for reply", id))
		}
	})

	slot.Value = pendingClientRPC{
		rpcType: uint16(wire.MsgRemoteGetRequest),
		nodeID:  id,
		timer:   timer,
	}

	req := wire.RemoteGetRequest{Key: key}
	estimatedLen := len(key) + 8
	buf := a.bytePool.Rent(estimatedLen)
	body, err := req.AppendMarshalBinary(buf[:0])
	if err != nil {
		a.r.CancelTimer(timer)
		a.slots.Release(corrID)
		a.bytePool.Return(buf)
		return err
	}

	if err := a.client.Request(id, uint16(wire.MsgRemoteGetRequest), corrID, body); err != nil {
		a.r.CancelTimer(timer)
		a.slots.Release(corrID)
		a.bytePool.Return(buf)
		return err
	}
	a.bytePool.Return(buf)
	return nil
}

// Heartbeat sends a heartbeat ping to node id with correlation ID corrID.
func (a *ClientAdapter) Heartbeat(id node.NodeID, corrID uint64) error {
	slot := a.slots.Acquire(corrID)

	timer := a.r.ScheduleOnce(a.requestTimeout, func() {
		_, ok := a.slots.Get(corrID)
		if !ok {
			return
		}
		defer a.slots.Release(corrID)
		if a.handler != nil {
			a.handler.OnRPCError(id, corrID, uint16(wire.MsgHeartbeatRequest), fmt.Errorf("transport: heartbeat to %s: timed out waiting for reply", id))
		}
	})

	slot.Value = pendingClientRPC{
		rpcType: uint16(wire.MsgHeartbeatRequest),
		nodeID:  id,
		timer:   timer,
	}

	if err := a.client.Request(id, uint16(wire.MsgHeartbeatRequest), corrID, nil); err != nil {
		a.r.CancelTimer(timer)
		a.slots.Release(corrID)
		return err
	}
	return nil
}

// GetMerkleRoot fetches node id's current anti-entropy Merkle root with correlation ID corrID.
func (a *ClientAdapter) GetMerkleRoot(id node.NodeID, corrID uint64) error {
	slot := a.slots.Acquire(corrID)

	timer := a.r.ScheduleOnce(a.requestTimeout, func() {
		_, ok := a.slots.Get(corrID)
		if !ok {
			return
		}
		defer a.slots.Release(corrID)
		if a.handler != nil {
			a.handler.OnRPCError(id, corrID, uint16(wire.MsgGetMerkleRootRequest), fmt.Errorf("transport: get merkle root to %s: timed out waiting for reply", id))
		}
	})

	slot.Value = pendingClientRPC{
		rpcType: uint16(wire.MsgGetMerkleRootRequest),
		nodeID:  id,
		timer:   timer,
	}

	if err := a.client.Request(id, uint16(wire.MsgGetMerkleRootRequest), corrID, nil); err != nil {
		a.r.CancelTimer(timer)
		a.slots.Release(corrID)
		return err
	}
	return nil
}

// NotifyLeaving informs node id that the local node is leaving the cluster gracefully with correlation ID corrID.
func (a *ClientAdapter) NotifyLeaving(id node.NodeID, corrID uint64) error {
	slot := a.slots.Acquire(corrID)

	timer := a.r.ScheduleOnce(a.requestTimeout, func() {
		_, ok := a.slots.Get(corrID)
		if !ok {
			return
		}
		defer a.slots.Release(corrID)
		if a.handler != nil {
			a.handler.OnRPCError(id, corrID, uint16(wire.MsgNotifyLeavingRequest), fmt.Errorf("transport: notify leaving to %s: timed out waiting for reply", id))
		}
	})

	slot.Value = pendingClientRPC{
		rpcType: uint16(wire.MsgNotifyLeavingRequest),
		nodeID:  id,
		timer:   timer,
	}

	if err := a.client.Request(id, uint16(wire.MsgNotifyLeavingRequest), corrID, nil); err != nil {
		a.r.CancelTimer(timer)
		a.slots.Release(corrID)
		return err
	}
	return nil
}

// GossipExchange sends the local node's gossip state to node id with correlation ID corrID.
func (a *ClientAdapter) GossipExchange(id node.NodeID, corrID uint64, entries []GossipEntry) error {
	slot := a.slots.Acquire(corrID)

	timer := a.r.ScheduleOnce(a.requestTimeout, func() {
		_, ok := a.slots.Get(corrID)
		if !ok {
			return
		}
		defer a.slots.Release(corrID)
		if a.handler != nil {
			a.handler.OnRPCError(id, corrID, uint16(wire.MsgGossipExchangeRequest), fmt.Errorf("transport: gossip exchange to %s: timed out waiting for reply", id))
		}
	})

	slot.Value = pendingClientRPC{
		rpcType: uint16(wire.MsgGossipExchangeRequest),
		nodeID:  id,
		timer:   timer,
	}

	req := wire.GossipExchangeRequest{Entries: entries}
	estimatedLen := 32*len(entries) + 4
	buf := a.bytePool.Rent(estimatedLen)
	body, err := req.AppendMarshalBinary(buf[:0])
	if err != nil {
		a.r.CancelTimer(timer)
		a.slots.Release(corrID)
		a.bytePool.Return(buf)
		return err
	}

	if err := a.client.Request(id, uint16(wire.MsgGossipExchangeRequest), corrID, body); err != nil {
		a.r.CancelTimer(timer)
		a.slots.Release(corrID)
		a.bytePool.Return(buf)
		return err
	}
	a.bytePool.Return(buf)
	return nil
}

// Close releases every connection the client holds.
func (a *ClientAdapter) Close() error {
	a.slots.ForEach(func(id uint64, s *pool.Slot[pendingClientRPC]) {
		a.r.CancelTimer(s.Value.timer)
		if a.handler != nil {
			a.handler.OnRPCError(s.Value.nodeID, id, s.Value.rpcType, errConnClosed)
		}
	})
	a.slots.Reset()
	return a.client.Close()
}
