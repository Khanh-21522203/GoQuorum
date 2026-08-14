package iouring

import (
	"errors"
	"net"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/contracts/quorumerr"
	"goquorum.io/v2/engine/reactor"
	"goquorum.io/v2/infra/ioruntime"
)

// fakeHandler is a RequestHandler test double that records how many times
// Heartbeat was called and lets a test control the error it answers with.
type fakeHandler struct {
	calls atomic.Int64
	err   error
}

func (h *fakeHandler) Heartbeat() error {
	h.calls.Add(1)
	return h.err
}

// testEnd bundles a real ioruntime.Runtime with a real reactor.Reactor
// driving it on its own goroutine, mirroring the pattern
// infra/ioruntime's and infra/storage/journal's own tests use for a real
// io_uring-backed loopback.
type testEnd struct {
	rt *ioruntime.Runtime
	r  *reactor.Reactor
}

func newTestEnd(t *testing.T) *testEnd {
	t.Helper()
	rt, err := ioruntime.New(64)
	if err != nil {
		t.Fatalf("ioruntime.New: %v", err)
	}
	return &testEnd{rt: rt, r: reactor.New(rt)}
}

// run starts te.r.Run on its own goroutine and registers cleanup that
// stops it and closes te.rt, in that order.
func (te *testEnd) run(t *testing.T) {
	t.Helper()
	errCh := make(chan error, 1)
	go func() { errCh <- te.r.Run() }()
	t.Cleanup(func() {
		te.r.RequestStop()
		select {
		case err := <-errCh:
			if err != nil {
				t.Errorf("reactor Run returned error: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("reactor Run did not return after RequestStop")
		}
		_ = te.rt.Close()
	})
}

// call runs fn on te's reactor goroutine (never on the calling test
// goroutine, per every reactor-owned type's single-goroutine contract) and
// waits up to 5s for it to signal completion via done.
func call(t *testing.T, te *testEnd, fn func()) {
	t.Helper()
	done := make(chan struct{})
	te.r.PostFunc(func() {
		fn()
		close(done)
	})
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("call did not complete on the reactor goroutine")
	}
}

// TestServerClient_Heartbeat_RoundTrip proves a real io_uring TCP
// round-trip end to end: Client.Heartbeat sends a real MsgHeartbeatRequest
// frame over a real loopback connection, Server decodes it and dispatches
// it to a fake RequestHandler, sends a real MsgHeartbeatResponse frame
// back, and Client's done callback fires with the right result.
func TestServerClient_Heartbeat_RoundTrip(t *testing.T) {
	serverEnd := newTestEnd(t)
	handler := &fakeHandler{}
	server := NewServer(serverEnd.rt, handler)
	serverEnd.r.SetEventHandler(func(ev reactor.Event) { server.HandleCompletion(ev) })
	serverEnd.run(t)

	var listenErr error
	call(t, serverEnd, func() { listenErr = server.Listen("127.0.0.1:0") })
	if listenErr != nil {
		t.Fatalf("Listen: %v", listenErr)
	}
	addr := server.Addr()

	clientEnd := newTestEnd(t)
	client := NewClient(clientEnd.rt, clientEnd.r)
	clientEnd.r.SetEventHandler(func(ev reactor.Event) { client.HandleCompletion(ev) })
	clientEnd.run(t)

	const peerID node.NodeID = "peer-round-trip"
	var dialErr error
	call(t, clientEnd, func() { dialErr = client.Dial(peerID, addr) })
	if dialErr != nil {
		t.Fatalf("Dial: %v", dialErr)
	}

	hbDone := make(chan error, 1)
	clientEnd.r.PostFunc(func() {
		client.Heartbeat(peerID, func(err error) { hbDone <- err })
	})
	select {
	case err := <-hbDone:
		if err != nil {
			t.Fatalf("Heartbeat: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Heartbeat did not complete")
	}

	if got := handler.calls.Load(); got != 1 {
		t.Fatalf("handler.Heartbeat called %d times, want 1", got)
	}

	call(t, clientEnd, func() { _ = client.Close() })
}

// TestServerClient_Heartbeat_HandlerError proves a RequestHandler error is
// carried back to the client as the equivalent quorumerr sentinel, via a
// real StatusCode round trip on the wire.
func TestServerClient_Heartbeat_HandlerError(t *testing.T) {
	serverEnd := newTestEnd(t)
	handler := &fakeHandler{err: quorumerr.ErrStorageClosed}
	server := NewServer(serverEnd.rt, handler)
	serverEnd.r.SetEventHandler(func(ev reactor.Event) { server.HandleCompletion(ev) })
	serverEnd.run(t)

	var listenErr error
	call(t, serverEnd, func() { listenErr = server.Listen("127.0.0.1:0") })
	if listenErr != nil {
		t.Fatalf("Listen: %v", listenErr)
	}
	addr := server.Addr()

	clientEnd := newTestEnd(t)
	client := NewClient(clientEnd.rt, clientEnd.r)
	clientEnd.r.SetEventHandler(func(ev reactor.Event) { client.HandleCompletion(ev) })
	clientEnd.run(t)

	const peerID node.NodeID = "peer-handler-error"
	var dialErr error
	call(t, clientEnd, func() { dialErr = client.Dial(peerID, addr) })
	if dialErr != nil {
		t.Fatalf("Dial: %v", dialErr)
	}

	hbDone := make(chan error, 1)
	clientEnd.r.PostFunc(func() {
		client.Heartbeat(peerID, func(err error) { hbDone <- err })
	})
	select {
	case err := <-hbDone:
		if !errors.Is(err, quorumerr.ErrStorageClosed) {
			t.Fatalf("Heartbeat error = %v, want %v", err, quorumerr.ErrStorageClosed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Heartbeat did not complete")
	}

	call(t, clientEnd, func() { _ = client.Close() })
}

// TestClient_Heartbeat_ConnectionRefused proves Dial to an address nothing
// is listening on fails fast rather than hanging: dialConn's blocking
// syscall.Connect gets ECONNREFUSED immediately on loopback.
func TestClient_Heartbeat_ConnectionRefused(t *testing.T) {
	deadAddr := reserveDeadAddr(t)

	clientEnd := newTestEnd(t)
	client := NewClient(clientEnd.rt, clientEnd.r)
	clientEnd.r.SetEventHandler(func(ev reactor.Event) { client.HandleCompletion(ev) })
	clientEnd.run(t)

	dialDone := make(chan error, 1)
	clientEnd.r.PostFunc(func() { dialDone <- client.Dial("peer-refused", deadAddr) })
	select {
	case err := <-dialDone:
		if err == nil {
			t.Fatal("Dial to an address nothing is listening on unexpectedly succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Dial did not return promptly for a refused connection; it hung")
	}
}

// TestClient_Heartbeat_Timeout proves Client.Heartbeat's timeout mechanism
// (a reactor timer racing the reply) fires and calls done with an error,
// rather than hanging, when the peer accepts the connection but never
// replies.
func TestClient_Heartbeat_Timeout(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err == nil {
			// Accept the connection and then do nothing: never reply, so
			// the client's request has no way to complete except via its
			// own timeout.
			t.Cleanup(func() { _ = conn.Close() })
		}
	}()

	clientEnd := newTestEnd(t)
	client := NewClient(clientEnd.rt, clientEnd.r)
	client.requestTimeout = 300 * time.Millisecond
	clientEnd.r.SetEventHandler(func(ev reactor.Event) { client.HandleCompletion(ev) })
	clientEnd.run(t)

	const peerID node.NodeID = "peer-timeout"
	var dialErr error
	call(t, clientEnd, func() { dialErr = client.Dial(peerID, ln.Addr().String()) })
	if dialErr != nil {
		t.Fatalf("Dial: %v", dialErr)
	}

	hbDone := make(chan error, 1)
	clientEnd.r.PostFunc(func() {
		client.Heartbeat(peerID, func(err error) { hbDone <- err })
	})
	select {
	case err := <-hbDone:
		if err == nil {
			t.Fatal("Heartbeat unexpectedly succeeded against a peer that never replies")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Heartbeat did not time out; it hung")
	}

	call(t, clientEnd, func() { _ = client.Close() })
}

// reserveDeadAddr binds an ephemeral loopback port, learns its address,
// and closes it immediately, yielding an address that (barring an
// improbable concurrent rebind by something else) nothing is listening
// on.
func reserveDeadAddr(t *testing.T) string {
	t.Helper()
	fd, err := listenTCP("127.0.0.1:0")
	if err != nil {
		t.Fatalf("listenTCP: %v", err)
	}
	sa, err := syscall.Getsockname(fd)
	if err != nil {
		t.Fatalf("Getsockname: %v", err)
	}
	addr, err := sockaddrToString(sa)
	if err != nil {
		t.Fatalf("sockaddrToString: %v", err)
	}
	if err := syscall.Close(fd); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return addr
}
