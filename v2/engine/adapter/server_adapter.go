package adapter

import (
	"encoding/binary"

	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/contracts/wire"
	"goquorum.io/v2/infra/pool"
	"goquorum.io/v2/infra/reactor"
	"goquorum.io/v2/infra/transport/iouring"
)

// ServerAdapterHandler defines the domain event hooks invoked when peer requests arrive over internal RPC.
type ServerAdapterHandler interface {
	// OnRemotePut is invoked when a remote coordinator replicates a key's sibling set.
	OnRemotePut(connFD int, corrID uint64, key []byte, ss *SiblingSet, reply func(error))
	// OnRemoteGet is invoked when a remote coordinator reads a key's local sibling set.
	OnRemoteGet(connFD int, corrID uint64, key []byte, reply func(*SiblingSet, error))
	// OnHeartbeat is invoked when a remote peer sends a heartbeat ping.
	OnHeartbeat(connFD int, corrID uint64, reply func(error))
	// OnGetMerkleRoot is invoked when a remote peer requests anti-entropy tree root hash.
	OnGetMerkleRoot(connFD int, corrID uint64, reply func(root []byte, err error))
	// OnGossipExchange is invoked when a remote peer sends gossip entries.
	OnGossipExchange(connFD int, corrID uint64, peerID node.NodeID, entries []GossipEntry, reply func([]GossipEntry, error))
	// OnNotifyLeaving is invoked when a remote peer announces graceful departure.
	OnNotifyLeaving(connFD int, corrID uint64, peerID node.NodeID, reply func(error))

	// OnClientConnected is invoked when a new remote client connects to this server.
	OnClientConnected(connFD int, remoteAddr string)
	// OnClientDisconnected is invoked when a remote client connection is dropped.
	OnClientDisconnected(connFD int, err error)
}

// ServerAdapter adapts an event-driven iouring.Server into a domain Inbound RPC engine with event hooks.
type ServerAdapter struct {
	server   *iouring.Server
	bytePool *pool.BucketArrayPool[byte]
	handler  ServerAdapterHandler

	// Optional extra event hooks for connection lifecycle and unhandled frames
	OnConnectedHook    func(connFD int, remoteAddr string)
	OnDisconnectedHook func(connFD int, err error)
	OnConnectErrorHook func(err error)
	OnUnhandledMsgHook func(connFD int, hdr iouring.FrameHeader, body []byte)
}

var _ iouring.ServerHandler = (*ServerAdapter)(nil)

// NewServerAdapter creates a new ServerAdapter wrapping an event-driven iouring.Server.
func NewServerAdapter(server *iouring.Server, handler ServerAdapterHandler) *ServerAdapter {
	bp := server.BytePool()
	if bp == nil {
		bp = pool.NewDefaultArrayPool[byte]()
	}
	a := &ServerAdapter{
		server:   server,
		bytePool: bp,
		handler:  handler,
	}
	server.SetHandler(a)
	return a
}

// SetHandler updates the domain inbound handler.
func (a *ServerAdapter) SetHandler(h ServerAdapterHandler) {
	a.handler = h
}

// Server returns the underlying iouring.Server.
func (a *ServerAdapter) Server() *iouring.Server {
	return a.server
}

// Listen starts listening on addr.
func (a *ServerAdapter) Listen(addr string) error {
	return a.server.Listen(addr)
}

// Addr returns the bound TCP listen address.
func (a *ServerAdapter) Addr() string {
	return a.server.Addr()
}

// HandleCompletion routes an io_uring completion event to the underlying server.
func (a *ServerAdapter) HandleCompletion(ev reactor.Event) bool {
	return a.server.HandleCompletion(ev)
}

// Close closes the underlying server and all open connections.
func (a *ServerAdapter) Close() error {
	return a.server.Close()
}

// OnMessage implements iouring.ServerHandler, decoding frames and dispatching to ServerInboundHandler.
func (a *ServerAdapter) OnMessage(connFD int, hdr iouring.FrameHeader, body []byte) {
	if a.handler == nil {
		if a.OnUnhandledMsgHook != nil {
			a.OnUnhandledMsgHook(connFD, hdr, body)
		}
		return
	}

	corrID := hdr.CorrelationID
	msgID := wire.MessageID(hdr.MessageID)

	switch msgID {
	case wire.MsgRemotePutRequest:
		var req wire.RemotePutRequest
		if err := req.Unmarshal(body); err != nil {
			a.sendError(connFD, uint16(wire.MsgRemotePutResponse), corrID, wire.StatusCorruptedData)
			return
		}
		a.handler.OnRemotePut(connFD, corrID, req.Key, req.Siblings, func(err error) {
			status := wire.StatusCodeFromError(err)
			resp := wire.RemotePutResponse{Status: status}
			buf := a.bytePool.Rent(2)
			respBytes, _ := resp.AppendMarshalBinary(buf[:0])
			_ = a.server.Send(connFD, uint16(wire.MsgRemotePutResponse), corrID, respBytes)
			a.bytePool.Return(buf)
		})

	case wire.MsgRemoteGetRequest:
		var req wire.RemoteGetRequest
		if err := req.Unmarshal(body); err != nil {
			a.sendError(connFD, uint16(wire.MsgRemoteGetResponse), corrID, wire.StatusCorruptedData)
			return
		}
		a.handler.OnRemoteGet(connFD, corrID, req.Key, func(ss *SiblingSet, err error) {
			status := wire.StatusCodeFromError(err)
			resp := wire.RemoteGetResponse{Status: status, Siblings: ss}
			buf := a.bytePool.Rent(64)
			respBytes, _ := resp.AppendMarshalBinary(buf[:0])
			_ = a.server.Send(connFD, uint16(wire.MsgRemoteGetResponse), corrID, respBytes)
			a.bytePool.Return(buf)
		})

	case wire.MsgHeartbeatRequest:
		a.handler.OnHeartbeat(connFD, corrID, func(err error) {
			status := wire.StatusCodeFromError(err)
			resp := wire.HeartbeatResponse{Status: status}
			buf := a.bytePool.Rent(2)
			respBytes, _ := resp.AppendMarshalBinary(buf[:0])
			_ = a.server.Send(connFD, uint16(wire.MsgHeartbeatResponse), corrID, respBytes)
			a.bytePool.Return(buf)
		})

	case wire.MsgGetMerkleRootRequest:
		a.handler.OnGetMerkleRoot(connFD, corrID, func(root []byte, err error) {
			status := wire.StatusCodeFromError(err)
			resp := wire.GetMerkleRootResponse{Status: status, Root: root}
			buf := a.bytePool.Rent(len(root) + 8)
			respBytes, _ := resp.AppendMarshalBinary(buf[:0])
			_ = a.server.Send(connFD, uint16(wire.MsgGetMerkleRootResponse), corrID, respBytes)
			a.bytePool.Return(buf)
		})

	case wire.MsgNotifyLeavingRequest:
		a.handler.OnNotifyLeaving(connFD, corrID, "", func(err error) {
			status := wire.StatusCodeFromError(err)
			resp := wire.NotifyLeavingResponse{Status: status}
			buf := a.bytePool.Rent(2)
			respBytes, _ := resp.AppendMarshalBinary(buf[:0])
			_ = a.server.Send(connFD, uint16(wire.MsgNotifyLeavingResponse), corrID, respBytes)
			a.bytePool.Return(buf)
		})

	case wire.MsgGossipExchangeRequest:
		var req wire.GossipExchangeRequest
		if err := req.Unmarshal(body); err != nil {
			a.sendError(connFD, uint16(wire.MsgGossipExchangeResponse), corrID, wire.StatusCorruptedData)
			return
		}
		a.handler.OnGossipExchange(connFD, corrID, "", req.Entries, func(entries []GossipEntry, err error) {
			resp := wire.GossipExchangeResponse{Entries: entries}
			buf := a.bytePool.Rent(32*len(entries) + 4)
			respBytes, _ := resp.AppendMarshalBinary(buf[:0])
			_ = a.server.Send(connFD, uint16(wire.MsgGossipExchangeResponse), corrID, respBytes)
			a.bytePool.Return(buf)
		})

	default:
		if a.OnUnhandledMsgHook != nil {
			a.OnUnhandledMsgHook(connFD, hdr, body)
		}
	}
}

func (a *ServerAdapter) sendError(connFD int, msgID uint16, corrID uint64, status wire.StatusCode) {
	buf := a.bytePool.Rent(2)
	buf = binary.BigEndian.AppendUint16(buf[:0], uint16(status))
	_ = a.server.Send(connFD, msgID, corrID, buf)
	a.bytePool.Return(buf)
}

// OnConnected implements iouring.ServerHandler.
func (a *ServerAdapter) OnConnected(connFD int, remoteAddr string) {
	if a.handler != nil {
		a.handler.OnClientConnected(connFD, remoteAddr)
	}
	if a.OnConnectedHook != nil {
		a.OnConnectedHook(connFD, remoteAddr)
	}
}

// OnDisconnected implements iouring.ServerHandler.
func (a *ServerAdapter) OnDisconnected(connFD int, err error) {
	if a.handler != nil {
		a.handler.OnClientDisconnected(connFD, err)
	}
	if a.OnDisconnectedHook != nil {
		a.OnDisconnectedHook(connFD, err)
	}
}

// OnConnectError implements iouring.ServerHandler.
func (a *ServerAdapter) OnConnectError(err error) {
	if a.OnConnectErrorHook != nil {
		a.OnConnectErrorHook(err)
	}
}
