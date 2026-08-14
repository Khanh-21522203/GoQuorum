package iouring

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"syscall"
	"time"

	"goquorum.io/v2/engine/reactor"
	"goquorum.io/v2/infra/ioruntime"
)

// wireSchemaVersion is the SchemaVersion this build writes into every
// frame it sends. See frame.go's FrameHeader doc comment for how a reader
// on an older schema stays compatible regardless of this value.
const wireSchemaVersion uint16 = 1

// defaultRequestTimeout bounds how long sendRequest waits for a reply
// before failing the call on the caller's behalf.
const defaultRequestTimeout = 5 * time.Second

// recvBufSize is the fixed size of a connection's receive buffer.
const recvBufSize = 64 * 1024

// listenBacklog is the backlog passed to syscall.Listen.
const listenBacklog = 128

// errConnClosed is delivered to any request still pending (or newly
// submitted) once a connection has been torn down, whether by an explicit
// Close, a recv error, or the peer closing its end.
var errConnClosed = errors.New("iouring: connection closed")

// pendingRequest tracks one in-flight request awaiting its reply frame.
type pendingRequest struct {
	onReply func(FrameHeader, []byte, error)
	timer   reactor.TimerID
}

// conn is one persistent connection to one peer, multiplexing many
// concurrent request/reply pairs (correlated by FrameHeader.CorrelationID)
// over a single TCP socket driven by io_uring. Every method on conn must
// be called from the reactor goroutine that also delivers its
// HandleCompletion, exactly like every other reactor-owned type in this
// codebase (see infra/storage/journal's package doc for the pattern this
// mirrors). See doc.go for the userData encoding that makes
// HandleCompletion composable across several conns (and other
// HandleCompletion owners) sharing one reactor.Reactor.
type conn struct {
	rt *ioruntime.Runtime
	r  *reactor.Reactor

	fd   int
	addr string

	reassembler Reassembler
	recvBuf     []byte

	nextCorrelationID uint64
	nextSendSeq       uint64
	pending           map[uint64]pendingRequest // by CorrelationID
	sendCorrelation   map[uint64]uint64         // by send userData -> CorrelationID

	closed bool
}

// dialConn establishes a new TCP connection to addr with a plain blocking
// connect (see doc.go's "Dialing" note for why this is an accepted
// tradeoff) and arms the persistent recv loop.
func dialConn(rt *ioruntime.Runtime, r *reactor.Reactor, addr string) (*conn, error) {
	fd, err := connectTCP(addr)
	if err != nil {
		return nil, err
	}
	c := &conn{
		rt:              rt,
		r:               r,
		fd:              fd,
		addr:            addr,
		recvBuf:         make([]byte, recvBufSize),
		pending:         make(map[uint64]pendingRequest),
		sendCorrelation: make(map[uint64]uint64),
	}
	c.armRecv()
	return c, nil
}

// armRecv submits the next recv on this connection's socket, keeping it
// always listening for the next frame (or frame fragment).
func (c *conn) armRecv() {
	if c.closed {
		return
	}
	if err := c.rt.SubmitRecv(c.fd, c.recvBuf, makeUserData(c.fd, 0)); err != nil {
		c.fail(fmt.Errorf("iouring: submitting recv: %w", err))
	}
}

// HandleCompletion dispatches ev to this conn if ev.UserData's encoded fd
// (see doc.go) matches this conn's socket, returning true if so. A caller
// composing several HandleCompletion owners on one reactor.Reactor tries
// each in turn and stops at the first one that returns true.
func (c *conn) HandleCompletion(ev reactor.Event) bool {
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

func (c *conn) onRecvCompletion(ev reactor.Event) {
	if c.closed {
		return
	}
	if err := recvLoop(c.rt, c.fd, c.recvBuf, &c.reassembler, ev, c.dispatchReply); err != nil {
		c.fail(err)
	}
}

// dispatchReply matches a decoded reply frame to the pending request that
// is awaiting it, if any, and delivers it. A reply with no matching
// pending entry (its timeout already fired, or it is a stray/duplicate
// frame) is silently dropped.
func (c *conn) dispatchReply(hdr FrameHeader, body []byte) {
	p, ok := c.pending[hdr.CorrelationID]
	if !ok {
		return
	}
	delete(c.pending, hdr.CorrelationID)
	c.r.CancelTimer(p.timer)
	p.onReply(hdr, body, nil)
}

// onSendCompletion handles the completion of a request's send operation.
// A successful send has nothing further to do: the request stays pending
// until its reply frame arrives (or its timeout fires). A failed send
// resolves the request immediately, since no reply will ever come for a
// frame that was never actually written to the socket.
func (c *conn) onSendCompletion(ev reactor.Event, seq uint64) {
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

// sendRequest sends one framed request of msgID with body over c and
// invokes onReply exactly once: when the matching reply frame arrives,
// when the send itself fails, when the connection dies before a reply
// arrives, or when timeout elapses with no reply.
func (c *conn) sendRequest(msgID MessageID, body []byte, timeout time.Duration, onReply func(FrameHeader, []byte, error)) {
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
	if c.nextSendSeq == 0 { // guard the reserved recv sequence (0) on wraparound
		c.nextSendSeq++
	}
	ud := makeUserData(c.fd, c.nextSendSeq)
	c.sendCorrelation[ud] = correlationID

	frameBytes := EncodeFrame(uint16(msgID), wireSchemaVersion, correlationID, body)
	if err := c.rt.SubmitSend(c.fd, frameBytes, ud); err != nil {
		delete(c.sendCorrelation, ud)
		if p, ok := c.pending[correlationID]; ok {
			delete(c.pending, correlationID)
			c.r.CancelTimer(p.timer)
			p.onReply(FrameHeader{}, nil, fmt.Errorf("iouring: submitting send: %w", err))
		}
	}
}

// fail tears the connection down: it marks conn closed, resolves every
// still-pending request with err, and closes the underlying socket. It is
// a no-op if the connection is already closed.
func (c *conn) fail(err error) {
	if c.closed {
		return
	}
	c.closed = true
	for id, p := range c.pending {
		delete(c.pending, id)
		c.r.CancelTimer(p.timer)
		p.onReply(FrameHeader{}, nil, err)
	}
	c.sendCorrelation = nil
	_ = syscall.Close(c.fd)
}

// close explicitly tears the connection down, resolving any still-pending
// requests with errConnClosed.
func (c *conn) close() error {
	c.fail(errConnClosed)
	return nil
}

// --- userData encoding -----------------------------------------------------
//
// See doc.go for why every io_uring userData value submitted by this
// package encodes its owning fd in the high 32 bits: it is what lets
// HandleCompletion report "not mine" for a completion belonging to a
// different conn (or a different HandleCompletion owner entirely) sharing
// the same reactor.Reactor.

func makeUserData(fd int, seq uint64) uint64 {
	return uint64(uint32(fd))<<32 | (seq & 0xffffffff)
}

func splitUserData(ud uint64) (fd int, seq uint64) {
	return int(uint32(ud >> 32)), ud & 0xffffffff
}

// recvLoop processes one completed recv operation shared by conn (client
// side) and server.go's per-accepted-connection dispatch (server side):
// it validates the completion, feeds newly-received bytes into
// reassembler, decodes every complete frame now buffered (invoking onFrame
// for each, in arrival order), and re-arms the next recv so the connection
// keeps listening. It returns a non-nil error, and does NOT re-arm, when
// the connection should be considered dead (recv error, or the peer
// closing its end).
func recvLoop(rt *ioruntime.Runtime, fd int, recvBuf []byte, reassembler *Reassembler, ev reactor.Event, onFrame func(FrameHeader, []byte)) error {
	if ev.Err != nil {
		return fmt.Errorf("iouring: recv: %w", ev.Err)
	}
	if ev.Result <= 0 {
		return fmt.Errorf("%w: peer closed connection", errConnClosed)
	}

	reassembler.Feed(recvBuf[:ev.Result])
	for {
		hdr, body, ok := reassembler.Next()
		if !ok {
			break
		}
		onFrame(hdr, body)
	}

	if err := rt.SubmitRecv(fd, recvBuf, makeUserData(fd, 0)); err != nil {
		return fmt.Errorf("iouring: submitting recv: %w", err)
	}
	return nil
}

// --- raw socket setup (dial side) -------------------------------------------

// connectTCP resolves addr and performs a plain blocking connect,
// returning the raw socket fd. See doc.go's "Dialing" note for why this
// blocks the calling (reactor) goroutine, and why that is acceptable here.
func connectTCP(addr string) (int, error) {
	tcpAddr, err := net.ResolveTCPAddr("tcp", addr)
	if err != nil {
		return -1, fmt.Errorf("iouring: resolving %q: %w", addr, err)
	}
	family, sa, err := sockaddrFor(tcpAddr)
	if err != nil {
		return -1, err
	}

	fd, err := syscall.Socket(family, syscall.SOCK_STREAM, 0)
	if err != nil {
		return -1, fmt.Errorf("iouring: socket: %w", err)
	}
	if err := syscall.Connect(fd, sa); err != nil {
		_ = syscall.Close(fd)
		return -1, fmt.Errorf("iouring: connect to %q: %w", addr, err)
	}
	return fd, nil
}

// listenTCP resolves addr, binds, and listens, returning the raw
// listening socket fd.
func listenTCP(addr string) (int, error) {
	tcpAddr, err := net.ResolveTCPAddr("tcp", addr)
	if err != nil {
		return -1, fmt.Errorf("iouring: resolving %q: %w", addr, err)
	}
	family, sa, err := sockaddrFor(tcpAddr)
	if err != nil {
		return -1, err
	}

	fd, err := syscall.Socket(family, syscall.SOCK_STREAM, 0)
	if err != nil {
		return -1, fmt.Errorf("iouring: socket: %w", err)
	}
	if err := syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1); err != nil {
		_ = syscall.Close(fd)
		return -1, fmt.Errorf("iouring: setsockopt SO_REUSEADDR: %w", err)
	}
	if err := syscall.Bind(fd, sa); err != nil {
		_ = syscall.Close(fd)
		return -1, fmt.Errorf("iouring: bind %q: %w", addr, err)
	}
	if err := syscall.Listen(fd, listenBacklog); err != nil {
		_ = syscall.Close(fd)
		return -1, fmt.Errorf("iouring: listen: %w", err)
	}
	return fd, nil
}

// sockaddrFor converts a resolved *net.TCPAddr into the (family, Sockaddr)
// pair syscall.Socket/Connect/Bind expect, choosing IPv4 or IPv6 based on
// what the address actually resolved to.
func sockaddrFor(addr *net.TCPAddr) (family int, sa syscall.Sockaddr, err error) {
	if ip4 := addr.IP.To4(); ip4 != nil {
		sa4 := &syscall.SockaddrInet4{Port: addr.Port}
		copy(sa4.Addr[:], ip4)
		return syscall.AF_INET, sa4, nil
	}
	ip16 := addr.IP.To16()
	if ip16 == nil {
		return 0, nil, fmt.Errorf("iouring: %q did not resolve to a usable IP", addr)
	}
	sa6 := &syscall.SockaddrInet6{Port: addr.Port}
	copy(sa6.Addr[:], ip16)
	return syscall.AF_INET6, sa6, nil
}

// sockaddrToString renders a syscall.Sockaddr obtained from
// syscall.Getsockname as a "host:port" string.
func sockaddrToString(sa syscall.Sockaddr) (string, error) {
	switch v := sa.(type) {
	case *syscall.SockaddrInet4:
		return net.JoinHostPort(net.IP(v.Addr[:]).String(), strconv.Itoa(v.Port)), nil
	case *syscall.SockaddrInet6:
		return net.JoinHostPort(net.IP(v.Addr[:]).String(), strconv.Itoa(v.Port)), nil
	default:
		return "", fmt.Errorf("iouring: unsupported sockaddr type %T", sa)
	}
}
