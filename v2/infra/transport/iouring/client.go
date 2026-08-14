package iouring

import (
	"fmt"
	"time"

	"goquorum.io/v2/contracts"
	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/engine/reactor"
	"goquorum.io/v2/engine/storage"
	"goquorum.io/v2/engine/transport"
	"goquorum.io/v2/infra/ioruntime"
)

// Client implements engine/transport.Transport over the wire protocol in
// wire.go/frame.go, driven by io_uring. Peer addresses are resolved by
// whatever address book the caller wires in (see Dial) — Client itself
// does not resolve node.NodeID to an address on its own, the same
// division of responsibility infra/transport/httprpc.Client documents.
//
// Every method must be called from the same reactor.Reactor goroutine
// that delivers HandleCompletion, per the same single-goroutine discipline
// engine/reactor.Reactor documents and infra/storage/journal.Store
// follows. Client keeps a *reactor.Reactor (unlike journal.Store) because
// sendRequest's timeout is implemented as a reactor timer.
//
// Only Heartbeat is wired end-to-end onto conn.go in this pass; the
// remaining five engine/transport.Transport RPCs are thin stubs (see each
// method's TODO) since the wire encode/decode they need already exists in
// wire.go and the pattern to wire them is identical to Heartbeat's.
type Client struct {
	rt *ioruntime.Runtime
	r  *reactor.Reactor

	// requestTimeout bounds how long a request waits for a reply before
	// sendRequest fails it; defaultRequestTimeout unless overridden (tests
	// in this package override it directly to keep a timeout test fast).
	requestTimeout time.Duration

	addrs map[node.NodeID]string
	conns map[node.NodeID]*conn
	byFD  map[int]*conn
}

var _ transport.Transport = (*Client)(nil)

// NewClient creates a Client that submits io_uring operations through rt
// and schedules request timeouts on r.
func NewClient(rt *ioruntime.Runtime, r *reactor.Reactor) *Client {
	return &Client{
		rt:             rt,
		r:              r,
		requestTimeout: defaultRequestTimeout,
		addrs:          make(map[node.NodeID]string),
		conns:          make(map[node.NodeID]*conn),
		byFD:           make(map[int]*conn),
	}
}

// Dial establishes (or re-establishes) a persistent connection to node id
// at addr, remembering addr so a later reconnect (e.g. after the
// connection dies) can redial it without the caller repeating it. See
// conn.go's dialConn doc for the blocking-connect tradeoff this makes.
func (c *Client) Dial(id node.NodeID, addr string) error {
	c.addrs[id] = addr
	_, err := c.connect(id)
	return err
}

// HandleCompletion dispatches ev to whichever of this Client's connections
// owns it, returning true if one claimed it. See doc.go for the userData
// encoding this relies on to route by fd.
func (c *Client) HandleCompletion(ev reactor.Event) bool {
	fd, _ := splitUserData(ev.UserData)
	cn, ok := c.byFD[fd]
	if !ok {
		return false
	}
	return cn.HandleCompletion(ev)
}

// connect dials (or redials) the connection to id using the address last
// passed to Dial, replacing any existing entry.
func (c *Client) connect(id node.NodeID) (*conn, error) {
	addr, ok := c.addrs[id]
	if !ok {
		return nil, fmt.Errorf("iouring: no known address for node %q; call Dial first", id)
	}
	cn, err := dialConn(c.rt, c.r, addr)
	if err != nil {
		return nil, err
	}
	if old, ok := c.conns[id]; ok {
		delete(c.byFD, old.fd)
		_ = old.close()
	}
	c.conns[id] = cn
	c.byFD[cn.fd] = cn
	return cn, nil
}

// connFor returns id's existing live connection, or lazily (re)dials it
// using the address most recently passed to Dial.
func (c *Client) connFor(id node.NodeID) (*conn, error) {
	if cn, ok := c.conns[id]; ok && !cn.closed {
		return cn, nil
	}
	return c.connect(id)
}

// Heartbeat pings node id for liveness. This is the one RPC this pass
// wires fully onto conn.go's real, io_uring-driven wire protocol.
func (c *Client) Heartbeat(id node.NodeID, done func(error)) {
	cn, err := c.connFor(id)
	if err != nil {
		done(err)
		return
	}
	body, _ := (HeartbeatRequest{}).Marshal() // never fails: no fields to encode.
	cn.sendRequest(MsgHeartbeatRequest, body, c.requestTimeout, func(hdr FrameHeader, respBody []byte, err error) {
		if err != nil {
			done(err)
			return
		}
		var resp HeartbeatResponse
		if err := resp.Unmarshal(respBody); err != nil {
			done(err)
			return
		}
		done(StatusCodeToError(resp.Status))
	})
}

// RemotePut replicates a write to node id.
//
// TODO(v2): wire into conn.go following Heartbeat's pattern:
// cn.sendRequest(MsgRemotePutRequest, (&RemotePutRequest{Key: key,
// Siblings: siblings}).Marshal(), ..., decode RemotePutResponse).
func (c *Client) RemotePut(id node.NodeID, key []byte, siblings *storage.SiblingSet, done func(error)) {
	done(contracts.ErrNotImplemented)
}

// RemoteGet reads a key's sibling set from node id.
//
// TODO(v2): wire into conn.go following Heartbeat's pattern:
// cn.sendRequest(MsgRemoteGetRequest, (&RemoteGetRequest{Key:
// key}).Marshal(), ..., decode RemoteGetResponse).
func (c *Client) RemoteGet(id node.NodeID, key []byte, done func(*storage.SiblingSet, error)) {
	done(nil, contracts.ErrNotImplemented)
}

// GetMerkleRoot fetches node id's current anti-entropy Merkle root.
//
// TODO(v2): wire into conn.go following Heartbeat's pattern:
// cn.sendRequest(MsgGetMerkleRootRequest, ..., decode
// GetMerkleRootResponse).
func (c *Client) GetMerkleRoot(id node.NodeID, done func([]byte, error)) {
	done(nil, contracts.ErrNotImplemented)
}

// NotifyLeaving informs node id that the local node is leaving the
// cluster gracefully.
//
// TODO(v2): wire into conn.go following Heartbeat's pattern:
// cn.sendRequest(MsgNotifyLeavingRequest, ..., decode
// NotifyLeavingResponse).
func (c *Client) NotifyLeaving(id node.NodeID, done func(error)) {
	done(contracts.ErrNotImplemented)
}

// GossipExchange sends the local node's gossip state to node id and
// returns its reply.
//
// TODO(v2): wire into conn.go following Heartbeat's pattern:
// cn.sendRequest(MsgGossipExchangeRequest, (&GossipExchangeRequest{Entries:
// entries}).Marshal(), ..., decode GossipExchangeResponse).
func (c *Client) GossipExchange(id node.NodeID, entries []transport.GossipEntry, done func([]transport.GossipEntry, error)) {
	done(nil, contracts.ErrNotImplemented)
}

// Close releases every connection this Client holds, closing each
// connection's underlying socket.
func (c *Client) Close() error {
	for id, cn := range c.conns {
		_ = cn.close()
		delete(c.byFD, cn.fd)
		delete(c.conns, id)
	}
	return nil
}
