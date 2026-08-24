package iouring

import (
	"fmt"
	"syscall"

	"goquorum.io/v2/infra/ioruntime"
	"goquorum.io/v2/infra/pool"
	"goquorum.io/v2/infra/reactor"
)

// ServerHandler receives inbound network events from Server via static method dispatch.
type ServerHandler interface {
	// OnMessage is invoked when a complete framed message is received.
	OnMessage(connFD int, hdr FrameHeader, body []byte)

	// OnConnected is invoked when an inbound connection is accepted.
	OnConnected(connFD int, remoteAddr string)

	// OnDisconnected is invoked when an accepted connection terminates.
	OnDisconnected(connFD int, err error)

	// OnConnectError is invoked when accepting an inbound connection fails.
	OnConnectError(err error)
}

// Server accepts inbound peer connections and dispatches incoming frames (0 domain knowledge).
type Server struct {
	rt       *ioruntime.Runtime
	bytePool *pool.BucketArrayPool[byte]
	handler  ServerHandler

	listenFD int
	addr     string
	conns    map[int]*serverConn
}

// NewServer creates a new io_uring transport Server with an optional shared byte pool.
func NewServer(rt *ioruntime.Runtime, bytePool *pool.BucketArrayPool[byte], handler ServerHandler) *Server {
	if bytePool == nil {
		bytePool = pool.NewDefaultArrayPool[byte]()
	}
	return &Server{
		rt:       rt,
		bytePool: bytePool,
		handler:  handler,
		listenFD: -1,
		conns:    make(map[int]*serverConn),
	}
}

// BytePool returns the byte buffer pool used by this Server.
func (s *Server) BytePool() *pool.BucketArrayPool[byte] {
	return s.bytePool
}

// SetHandler sets or updates the server event hookback handler.
func (s *Server) SetHandler(h ServerHandler) {
	s.handler = h
}

// Listen binds to addr and arms the accept loop.
func (s *Server) Listen(addr string) error {
	fd, err := listenTCP(addr)
	if err != nil {
		return err
	}
	bound, err := syscall.Getsockname(fd)
	if err != nil {
		_ = syscall.Close(fd)
		return fmt.Errorf("iouring: getsockname: %w", err)
	}
	boundAddr, err := sockaddrToString(bound)
	if err != nil {
		_ = syscall.Close(fd)
		return err
	}

	s.listenFD = fd
	s.addr = boundAddr
	s.armAccept()
	return nil
}

// Addr returns the address Listen actually bound to.
func (s *Server) Addr() string {
	return s.addr
}

// Send transmits a framed message to the client on connFD using zero-alloc pooled buffers.
func (s *Server) Send(connFD int, msgID uint16, correlationID uint64, body []byte) error {
	sc, ok := s.conns[connFD]
	if !ok || sc.closed {
		return errConnClosed
	}
	return sc.send(msgID, correlationID, body)
}

// HandleCompletion dispatches an io_uring completion event to this Server.
func (s *Server) HandleCompletion(ev reactor.Event) bool {
	fd, seq := splitUserData(ev.UserData)
	if fd == s.listenFD && seq == 0 {
		s.onAcceptCompletion(ev)
		return true
	}
	sc, ok := s.conns[fd]
	if !ok {
		return false
	}
	sc.handleCompletion(ev)
	return true
}

func (s *Server) armAccept() {
	if s.listenFD < 0 {
		return
	}
	_ = s.rt.SubmitAccept(s.listenFD, makeUserData(s.listenFD, 0))
}

func (s *Server) onAcceptCompletion(ev reactor.Event) {
	defer s.armAccept()
	if ev.Err != nil {
		if s.handler != nil {
			s.handler.OnConnectError(ev.Err)
		}
		return
	}
	connFD := int(ev.Result)
	var remoteAddrStr string
	if remoteAddr, err := syscall.Getpeername(connFD); err == nil {
		remoteAddrStr, _ = sockaddrToString(remoteAddr)
	}
	sc := &serverConn{
		tcpConn: newTCPConn(s.rt, s.bytePool, connFD, func(err error) {
			delete(s.conns, connFD)
			if s.handler != nil {
				s.handler.OnDisconnected(connFD, err)
			}
		}),
		server:    s,
		sendSlots: pool.NewSlotTable[[]byte](1024),
	}
	s.conns[connFD] = sc
	if s.handler != nil {
		s.handler.OnConnected(connFD, remoteAddrStr)
	}
	if err := sc.armRecv(); err != nil {
		sc.fail(err)
	}
}

// Close releases the listening socket and all active connections.
func (s *Server) Close() error {
	if s.listenFD >= 0 {
		_ = syscall.Close(s.listenFD)
		s.listenFD = -1
	}
	for fd, sc := range s.conns {
		sc.fail(errConnClosed)
		delete(s.conns, fd)
	}
	return nil
}

// serverConn handles an inbound peer connection.
type serverConn struct {
	tcpConn

	server    *Server
	nextReqID uint64
	sendSlots *pool.SlotTable[[]byte]
}

func (sc *serverConn) handleCompletion(ev reactor.Event) {
	if sc.closed {
		return
	}
	_, seq := splitUserData(ev.UserData)
	if seq == 0 {
		if err := sc.processRecv(ev, sc.onFrame); err != nil {
			sc.fail(err)
		}
		return
	}

	slotID := seq - 1
	slot, ok := sc.sendSlots.Get(slotID)
	if ok {
		if sc.bytePool != nil && cap(slot.Value) > 0 {
			sc.bytePool.Return(slot.Value)
		}
		sc.sendSlots.Release(slotID)
	}

	if ev.Err != nil {
		sc.fail(ev.Err)
	}
}

func (sc *serverConn) onFrame(hdr FrameHeader, body []byte) {
	if sc.server.handler != nil {
		sc.server.handler.OnMessage(sc.fd, hdr, body)
	}
}

func (sc *serverConn) send(msgID uint16, correlationID uint64, body []byte) error {
	if sc.closed {
		return errConnClosed
	}
	sc.nextReqID++
	slotID := sc.nextReqID
	slot := sc.sendSlots.Acquire(slotID)

	totalLen := FrameHeaderSize + len(body)
	var sendBuf []byte
	if sc.bytePool != nil {
		sendBuf = sc.bytePool.Rent(totalLen)
	}
	frameBytes := EncodeFrameTo(sendBuf, msgID, wireSchemaVersion, correlationID, body)
	slot.Value = frameBytes

	sendSeq := slotID + 1
	if err := sc.rt.SubmitSend(sc.fd, frameBytes, makeUserData(sc.fd, sendSeq)); err != nil {
		sc.sendSlots.Release(slotID)
		if sc.bytePool != nil && cap(frameBytes) > 0 {
			sc.bytePool.Return(frameBytes)
		}
		sc.fail(err)
		return err
	}
	return nil
}

func (sc *serverConn) fail(err error) {
	if sc.closed {
		return
	}
	if sc.sendSlots != nil {
		sc.sendSlots.ForEach(func(id uint64, s *pool.Slot[[]byte]) {
			if sc.bytePool != nil && cap(s.Value) > 0 {
				sc.bytePool.Return(s.Value)
			}
		})
		sc.sendSlots.Reset()
	}
	sc.tcpConn.fail(err)
}
