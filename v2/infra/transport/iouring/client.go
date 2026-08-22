package iouring

import (
	"fmt"

	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/engine/reactor"
	"goquorum.io/v2/infra/ioruntime"
	"goquorum.io/v2/infra/pool"
)

// ClientHandler receives asynchronous network events from Client via static method dispatch.
type ClientHandler interface {
	// OnFrame is called when an inbound framed message arrives from peer id.
	OnFrame(id node.NodeID, hdr FrameHeader, body []byte)

	// OnConnected is called when a connection to peer id is established.
	OnConnected(id node.NodeID, addr string)

	// OnDisconnected is called when a connection to peer id dies or is closed.
	OnDisconnected(id node.NodeID, err error)

	// OnConnectError is called when establishing connection to peer id fails.
	OnConnectError(id node.NodeID, err error)
}

// Client is a pure, domain-agnostic io_uring transport engine multiplexing peer sockets.
type Client struct {
	rt       *ioruntime.Runtime
	r        *reactor.Reactor
	bytePool *pool.BucketArrayPool[byte]
	handler  ClientHandler

	addrs map[node.NodeID]string
	conns map[node.NodeID]*clientConn
	byFD  map[int]*clientConn
}

// NewClient creates a new pure transport Client with an optional shared byte pool.
func NewClient(rt *ioruntime.Runtime, r *reactor.Reactor, bytePool *pool.BucketArrayPool[byte], handler ClientHandler) *Client {
	if bytePool == nil {
		bytePool = pool.NewDefaultArrayPool[byte]()
	}
	return &Client{
		rt:       rt,
		r:        r,
		bytePool: bytePool,
		handler:  handler,
		addrs:    make(map[node.NodeID]string),
		conns:    make(map[node.NodeID]*clientConn),
		byFD:     make(map[int]*clientConn),
	}
}

// BytePool returns the byte buffer pool used by this Client.
func (c *Client) BytePool() *pool.BucketArrayPool[byte] {
	return c.bytePool
}

// SetHandler sets or updates the event hookback handler.
func (c *Client) SetHandler(h ClientHandler) {
	c.handler = h
}

// Dial registers a peer address and establishes a connection.
func (c *Client) Dial(id node.NodeID, addr string) error {
	c.addrs[id] = addr
	_, err := c.connect(id)
	return err
}

// Send transmits a one-way frame without expecting an RPC response.
func (c *Client) Send(id node.NodeID, msgID uint16, correlationID uint64, body []byte) error {
	cn, err := c.connFor(id)
	if err != nil {
		return err
	}
	return cn.send(msgID, correlationID, body)
}

// Request transmits a two-way RPC frame. When the response frame arrives,
// ClientHandler.OnResponse is invoked with matching CorrelationID (0 closures!).
func (c *Client) Request(id node.NodeID, msgID uint16, correlationID uint64, body []byte) error {
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

func (c *Client) connect(id node.NodeID) (*clientConn, error) {
	addr, ok := c.addrs[id]
	if !ok {
		err := fmt.Errorf("iouring: no known address for node %q; call Dial first", id)
		if c.handler != nil {
			c.handler.OnConnectError(id, err)
		}
		return nil, err
	}
	var cn *clientConn
	var err error
	cn, err = dialClientConn(c.rt, c.bytePool, c.r, id, c, addr, func(dErr error) {
		if cn != nil {
			delete(c.byFD, cn.fd)
		}
		if c.conns[id] == cn {
			delete(c.conns, id)
		}
		if c.handler != nil {
			c.handler.OnDisconnected(id, dErr)
		}
	})
	if err != nil {
		if c.handler != nil {
			c.handler.OnConnectError(id, err)
		}
		return nil, err
	}
	if old, ok := c.conns[id]; ok {
		delete(c.byFD, old.fd)
		_ = old.close()
	}
	c.conns[id] = cn
	c.byFD[cn.fd] = cn
	if c.handler != nil {
		c.handler.OnConnected(id, addr)
	}
	return cn, nil
}

func (c *Client) connFor(id node.NodeID) (*clientConn, error) {
	if cn, ok := c.conns[id]; ok && !cn.closed {
		return cn, nil
	}
	return c.connect(id)
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

// clientConn manages an outbound socket connection.
type clientConn struct {
	tcpConn

	client    *Client
	id        node.NodeID
	r         *reactor.Reactor
	addr      string
	nextReqID uint64
	sendSlots *pool.SlotTable[[]byte]
}

func dialClientConn(rt *ioruntime.Runtime, bp *pool.BucketArrayPool[byte], r *reactor.Reactor, id node.NodeID, client *Client, addr string, onDead func(error)) (*clientConn, error) {
	fd, err := connectTCP(addr)
	if err != nil {
		return nil, err
	}
	c := &clientConn{
		tcpConn:   newTCPConn(rt, bp, fd, onDead),
		client:    client,
		id:        id,
		r:         r,
		addr:      addr,
		sendSlots: pool.NewSlotTable[[]byte](1024),
	}
	if err := c.armRecv(); err != nil {
		c.fail(err)
		return nil, err
	}
	return c, nil
}

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
	if err := c.processRecv(ev, c.dispatchFrame); err != nil {
		c.fail(err)
	}
}

func (c *clientConn) dispatchFrame(hdr FrameHeader, body []byte) {
	if c.client != nil && c.client.handler != nil {
		c.client.handler.OnFrame(c.id, hdr, body)
	}
}

func (c *clientConn) onSendCompletion(ev reactor.Event, seq uint64) {
	if seq == 0 {
		return
	}
	slotID := seq - 1
	slot, ok := c.sendSlots.Get(slotID)
	if ok {
		if c.bytePool != nil && cap(slot.Value) > 0 {
			c.bytePool.Return(slot.Value)
		}
		c.sendSlots.Release(slotID)
	}
	if ev.Err != nil {
		c.fail(ev.Err)
	}
}

func (c *clientConn) send(msgID uint16, correlationID uint64, body []byte) error {
	if c.closed {
		return errConnClosed
	}
	c.nextReqID++
	slotID := c.nextReqID
	slot := c.sendSlots.Acquire(slotID)

	totalLen := FrameHeaderSize + len(body)
	var sendBuf []byte
	if c.bytePool != nil {
		sendBuf = c.bytePool.Rent(totalLen)
	}
	frameBytes := EncodeFrameTo(sendBuf, msgID, wireSchemaVersion, correlationID, body)
	slot.Value = frameBytes

	sendSeq := slotID + 1
	if err := c.rt.SubmitSend(c.fd, frameBytes, makeUserData(c.fd, sendSeq)); err != nil {
		c.sendSlots.Release(slotID)
		if c.bytePool != nil && cap(frameBytes) > 0 {
			c.bytePool.Return(frameBytes)
		}
		return err
	}
	return nil
}

func (c *clientConn) fail(err error) {
	if c.closed {
		return
	}
	if c.sendSlots != nil {
		c.sendSlots.ForEach(func(id uint64, s *pool.Slot[[]byte]) {
			if c.bytePool != nil && cap(s.Value) > 0 {
				c.bytePool.Return(s.Value)
			}
		})
		c.sendSlots.Reset()
	}
	c.tcpConn.fail(err)
}
