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

// defaultRequestTimeout bounds RPC reply wait duration.
const defaultRequestTimeout = 5 * time.Second

// pendingRequest tracks an in-flight RPC awaiting a reply.
type pendingRequest struct {
	onReply func(FrameHeader, []byte, error)
	timer   reactor.TimerID
}

// Client implements engine/transport.Transport over the io_uring wire protocol.
type Client struct {
	rt *ioruntime.Runtime
	r  *reactor.Reactor

	requestTimeout time.Duration

	addrs map[node.NodeID]string
	conns map[node.NodeID]*clientConn
	byFD  map[int]*clientConn

	// OnConnected is invoked when a connection to peer id is established.
	OnConnected func(id node.NodeID, addr string)

	// OnDisconnected is invoked when a connection to peer id dies or is closed.
	OnDisconnected func(id node.NodeID, err error)

	// OnConnectError is invoked when connecting to peer id fails.
	OnConnectError func(id node.NodeID, err error)

	// OnMessage is invoked when an unsolicited (non-RPC reply) framed message arrives.
	OnMessage func(id node.NodeID, hdr FrameHeader, body []byte)
}

var _ transport.Transport = (*Client)(nil)

// NewClient creates a new io_uring transport Client.
func NewClient(rt *ioruntime.Runtime, r *reactor.Reactor) *Client {
	return &Client{
		rt:             rt,
		r:              r,
		requestTimeout: defaultRequestTimeout,
		addrs:          make(map[node.NodeID]string),
		conns:          make(map[node.NodeID]*clientConn),
		byFD:           make(map[int]*clientConn),
	}
}

// Dial registers a peer's address and establishes a connection.
func (c *Client) Dial(id node.NodeID, addr string) error {
	c.addrs[id] = addr
	_, err := c.connect(id)
	return err
}

// Send transmits a framed message to peer id without expecting an RPC reply.
func (c *Client) Send(id node.NodeID, msgID MessageID, correlationID uint64, body []byte) error {
	cn, err := c.connFor(id)
	if err != nil {
		return err
	}
	return cn.send(msgID, correlationID, body)
}

// HandleCompletion dispatches an io_uring event to the appropriate connection.
func (c *Client) HandleCompletion(ev reactor.Event) bool {
	fd, _ := splitUserData(ev.UserData)
	cn, ok := c.byFD[fd]
	if !ok {
		return false
	}
	return cn.HandleCompletion(ev)
}

// connect establishes a connection to id using its configured address.
func (c *Client) connect(id node.NodeID) (*clientConn, error) {
	addr, ok := c.addrs[id]
	if !ok {
		err := fmt.Errorf("iouring: no known address for node %q; call Dial first", id)
		if c.OnConnectError != nil {
			c.OnConnectError(id, err)
		}
		return nil, err
	}
	var cn *clientConn
	var err error
	cn, err = dialClientConn(c.rt, c.r, id, c, addr, func(dErr error) {
		if cn != nil {
			delete(c.byFD, cn.fd)
		}
		if c.conns[id] == cn {
			delete(c.conns, id)
		}
		if c.OnDisconnected != nil {
			c.OnDisconnected(id, dErr)
		}
	})
	if err != nil {
		if c.OnConnectError != nil {
			c.OnConnectError(id, err)
		}
		return nil, err
	}
	if old, ok := c.conns[id]; ok {
		delete(c.byFD, old.fd)
		_ = old.close()
	}
	c.conns[id] = cn
	c.byFD[cn.fd] = cn
	if c.OnConnected != nil {
		c.OnConnected(id, addr)
	}
	return cn, nil
}

// connFor returns id's live connection or lazily reconnects if closed.
func (c *Client) connFor(id node.NodeID) (*clientConn, error) {
	if cn, ok := c.conns[id]; ok && !cn.closed {
		return cn, nil
	}
	return c.connect(id)
}

// Heartbeat sends a heartbeat ping to node id.
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
func (c *Client) RemotePut(id node.NodeID, key []byte, siblings *storage.SiblingSet, done func(error)) {
	done(contracts.ErrNotImplemented)
}

// RemoteGet reads a key's sibling set from node id.
func (c *Client) RemoteGet(id node.NodeID, key []byte, done func(*storage.SiblingSet, error)) {
	done(nil, contracts.ErrNotImplemented)
}

// GetMerkleRoot fetches node id's current anti-entropy Merkle root.
func (c *Client) GetMerkleRoot(id node.NodeID, done func([]byte, error)) {
	done(nil, contracts.ErrNotImplemented)
}

// NotifyLeaving informs node id that the local node is leaving the cluster gracefully.
func (c *Client) NotifyLeaving(id node.NodeID, done func(error)) {
	done(contracts.ErrNotImplemented)
}

// GossipExchange sends the local node's gossip state to node id and returns its reply.
func (c *Client) GossipExchange(id node.NodeID, entries []transport.GossipEntry, done func([]transport.GossipEntry, error)) {
	done(nil, contracts.ErrNotImplemented)
}

// Close releases every connection this Client holds.
func (c *Client) Close() error {
	for id, cn := range c.conns {
		_ = cn.close()
		delete(c.byFD, cn.fd)
		delete(c.conns, id)
	}
	return nil
}

// clientConn manages an outbound connection multiplexing concurrent RPC requests.
type clientConn struct {
	tcpConn

	client *Client
	id     node.NodeID
	r      *reactor.Reactor
	addr   string

	nextCorrelationID uint64
	nextSendSeq       uint64
	pending           map[uint64]pendingRequest
	sendCorrelation   map[uint64]uint64
}

// dialClientConn establishes a TCP connection to addr and arms the receive loop.
func dialClientConn(rt *ioruntime.Runtime, r *reactor.Reactor, id node.NodeID, client *Client, addr string, onDead func(error)) (*clientConn, error) {
	fd, err := connectTCP(addr)
	if err != nil {
		return nil, err
	}
	c := &clientConn{
		tcpConn:         newTCPConn(rt, fd, onDead),
		client:          client,
		id:              id,
		r:               r,
		addr:            addr,
		pending:         make(map[uint64]pendingRequest),
		sendCorrelation: make(map[uint64]uint64),
	}
	if err := c.armRecv(); err != nil {
		c.fail(err)
		return nil, err
	}
	return c, nil
}

// HandleCompletion dispatches an io_uring completion event to this clientConn.
func (c *clientConn) HandleCompletion(ev reactor.Event) bool {
	fd, seq := splitUserData(ev.UserData)
	if fd != c.fd {
		return false
	}
	if seq == 0 {
		c.onRecvCompletion(ev)
	} else {
		c.onSendCompletion(ev, seq)
	}
	return true
}

func (c *clientConn) onRecvCompletion(ev reactor.Event) {
	if c.closed {
		return
	}
	if err := c.processRecv(ev, c.dispatchReply); err != nil {
		c.fail(err)
	}
}

func (c *clientConn) dispatchReply(hdr FrameHeader, body []byte) {
	p, ok := c.pending[hdr.CorrelationID]
	if ok {
		delete(c.pending, hdr.CorrelationID)
		c.r.CancelTimer(p.timer)
		p.onReply(hdr, body, nil)
		return
	}
	if c.client != nil && c.client.OnMessage != nil {
		c.client.OnMessage(c.id, hdr, body)
	}
}

func (c *clientConn) onSendCompletion(ev reactor.Event, seq uint64) {
	ud := makeUserData(c.fd, seq)
	correlationID, ok := c.sendCorrelation[ud]
	delete(c.sendCorrelation, ud)
	if !ok || ev.Err == nil {
		return
	}
	p, ok := c.pending[correlationID]
	if !ok {
		return
	}
	delete(c.pending, correlationID)
	c.r.CancelTimer(p.timer)
	p.onReply(FrameHeader{}, nil, fmt.Errorf("iouring: sending request: %w", ev.Err))
}

func (c *clientConn) send(msgID MessageID, correlationID uint64, body []byte) error {
	if c.closed {
		return errConnClosed
	}
	c.nextSendSeq++
	if c.nextSendSeq == 0 {
		c.nextSendSeq++
	}
	return c.sendFrame(c.nextSendSeq, msgID, correlationID, body)
}

// sendRequest transmits a framed RPC request and registers a reply callback.
func (c *clientConn) sendRequest(msgID MessageID, body []byte, timeout time.Duration, onReply func(FrameHeader, []byte, error)) {
	if c.closed {
		onReply(FrameHeader{}, nil, errConnClosed)
		return
	}

	c.nextCorrelationID++
	correlationID := c.nextCorrelationID

	timer := c.r.ScheduleOnce(timeout, func() {
		p, ok := c.pending[correlationID]
		if !ok {
			return
		}
		delete(c.pending, correlationID)
		p.onReply(FrameHeader{}, nil, fmt.Errorf("iouring: request %s: timed out waiting for reply", msgID))
	})
	c.pending[correlationID] = pendingRequest{onReply: onReply, timer: timer}

	c.nextSendSeq++
	if c.nextSendSeq == 0 { // guard reserved recv sequence (0) on wraparound
		c.nextSendSeq++
	}
	ud := makeUserData(c.fd, c.nextSendSeq)
	c.sendCorrelation[ud] = correlationID

	if err := c.sendFrame(c.nextSendSeq, msgID, correlationID, body); err != nil {
		delete(c.sendCorrelation, ud)
		if p, ok := c.pending[correlationID]; ok {
			delete(c.pending, correlationID)
			c.r.CancelTimer(p.timer)
			p.onReply(FrameHeader{}, nil, fmt.Errorf("iouring: submitting send: %w", err))
		}
	}
}

// fail tears down the connection and cancels all pending requests.
func (c *clientConn) fail(err error) {
	if c.closed {
		return
	}
	for id, p := range c.pending {
		delete(c.pending, id)
		c.r.CancelTimer(p.timer)
		p.onReply(FrameHeader{}, nil, err)
	}
	c.sendCorrelation = nil
	c.tcpConn.fail(err)
}
