package iouring

import (
	"fmt"
	"syscall"

	"goquorum.io/v2/engine/reactor"
	"goquorum.io/v2/infra/ioruntime"
)

// Server accepts inbound peer connections and dispatches incoming frames.
type Server struct {
	rt *ioruntime.Runtime

	listenFD int
	addr     string

	conns map[int]*serverConn // by connection fd

	// OnConnected is invoked when an inbound connection is accepted.
	OnConnected func(connFD int, remoteAddr string)

	// OnDisconnected is invoked when an accepted connection terminates.
	OnDisconnected func(connFD int, err error)

	// OnConnectError is invoked when accepting an inbound connection fails.
	OnConnectError func(err error)

	// OnMessage is invoked when a complete framed message is received.
	OnMessage func(connFD int, hdr FrameHeader, body []byte)
}

// NewServer creates a new io_uring transport Server.
func NewServer(rt *ioruntime.Runtime) *Server {
	return &Server{
		rt:       rt,
		listenFD: -1,
		conns:    make(map[int]*serverConn),
	}
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

// Send transmits a framed message to the client on connFD.
func (s *Server) Send(connFD int, msgID MessageID, correlationID uint64, body []byte) error {
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
	defer s.armAccept() // keep listening regardless of this accept's outcome.
	if ev.Err != nil {
		if s.OnConnectError != nil {
			s.OnConnectError(ev.Err)
		}
		return
	}
	connFD := int(ev.Result)
	var remoteAddrStr string
	if remoteAddr, err := syscall.Getpeername(connFD); err == nil {
		remoteAddrStr, _ = sockaddrToString(remoteAddr)
	}
	sc := &serverConn{
		tcpConn: newTCPConn(s.rt, connFD, func(err error) {
			delete(s.conns, connFD)
			if s.OnDisconnected != nil {
				s.OnDisconnected(connFD, err)
			}
		}),
		server: s,
	}
	s.conns[connFD] = sc
	if s.OnConnected != nil {
		s.OnConnected(connFD, remoteAddrStr)
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

	server      *Server
	nextSendSeq uint64
}

// handleCompletion routes io_uring completions for this serverConn.
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
	if ev.Err != nil {
		sc.fail(ev.Err)
	}
}

func (sc *serverConn) onFrame(hdr FrameHeader, body []byte) {
	if sc.server.OnMessage != nil {
		sc.server.OnMessage(sc.fd, hdr, body)
	}
}

func (sc *serverConn) send(msgID MessageID, correlationID uint64, body []byte) error {
	sc.nextSendSeq++
	if sc.nextSendSeq == 0 { // guard the reserved recv sequence (0) on wraparound.
		sc.nextSendSeq++
	}
	if err := sc.sendFrame(sc.nextSendSeq, msgID, correlationID, body); err != nil {
		sc.fail(err)
		return err
	}
	return nil
}
