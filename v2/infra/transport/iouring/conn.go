package iouring

import (
	"errors"
	"fmt"
	"syscall"

	"goquorum.io/v2/engine/reactor"
	"goquorum.io/v2/infra/ioruntime"
	"goquorum.io/v2/infra/pool"
)

// wireSchemaVersion is the framing schema version written into outbound frames.
const wireSchemaVersion uint16 = 1

// recvBufSize is the fixed size of a connection's receive buffer.
const recvBufSize = 64 * 1024

// errConnClosed indicates a connection has been closed or lost.
var errConnClosed = errors.New("iouring: connection closed")

// tcpConn is the base struct managing raw socket I/O and io_uring frame streaming.
type tcpConn struct {
	rt          *ioruntime.Runtime
	fd          int
	bytePool    *pool.BucketArrayPool[byte]
	recvBuf     []byte
	reassembler Reassembler
	onDead      func(error)
	closed      bool
}

func newTCPConn(rt *ioruntime.Runtime, bp *pool.BucketArrayPool[byte], fd int, onDead func(error)) tcpConn {
	if bp == nil {
		bp = pool.NewDefaultArrayPool[byte]()
	}
	recvBuf := bp.Rent(recvBufSize)
	if cap(recvBuf) >= recvBufSize {
		recvBuf = recvBuf[:recvBufSize]
	} else {
		recvBuf = make([]byte, recvBufSize)
	}

	c := tcpConn{
		rt:       rt,
		fd:       fd,
		bytePool: bp,
		recvBuf:  recvBuf,
		onDead:   onDead,
	}
	c.reassembler.Init(bp, DefaultReassemblerCap)
	return c
}

// armRecv submits an asynchronous io_uring recv on the connection socket.
func (c *tcpConn) armRecv() error {
	if c.closed {
		return errConnClosed
	}
	return c.rt.SubmitRecv(c.fd, c.recvBuf, makeUserData(c.fd, 0))
}

// processRecv processes a completion event, feeds bytes into reassembler,
// invokes onFrame for each complete frame, and re-arms the socket.
func (c *tcpConn) processRecv(ev reactor.Event, onFrame func(FrameHeader, []byte)) error {
	if ev.Err != nil {
		return fmt.Errorf("iouring: recv: %w", ev.Err)
	}
	if ev.Result <= 0 {
		return fmt.Errorf("%w: peer closed connection", errConnClosed)
	}

	c.reassembler.Feed(c.recvBuf[:ev.Result])
	for {
		hdr, body, ok := c.reassembler.Next()
		if !ok {
			break
		}
		onFrame(hdr, body)
	}

	if err := c.armRecv(); err != nil {
		return fmt.Errorf("iouring: submitting recv: %w", err)
	}
	return nil
}

// fail closes the underlying socket and notifies the onDead observer.
func (c *tcpConn) fail(err error) {
	if c.closed {
		return
	}
	c.closed = true
	_ = syscall.Close(c.fd)
	if c.bytePool != nil && cap(c.recvBuf) > 0 {
		c.bytePool.Return(c.recvBuf)
		c.recvBuf = nil
	}
	c.reassembler.Release()
	if c.onDead != nil {
		c.onDead(err)
	}
}

// close explicitly tears down the connection.
func (c *tcpConn) close() error {
	c.fail(errConnClosed)
	return nil
}

// makeUserData packs (fd, seq) into a 64-bit io_uring tag.
func makeUserData(fd int, seq uint64) uint64 {
	return uint64(uint32(fd))<<32 | (seq & 0xffffffff)
}

// splitUserData unpacks a 64-bit tag into (fd, seq).
func splitUserData(ud uint64) (fd int, seq uint64) {
	return int(uint32(ud >> 32)), ud & 0xffffffff
}
