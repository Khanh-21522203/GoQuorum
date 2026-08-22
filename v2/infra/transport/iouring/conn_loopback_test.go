package iouring

import (
	"bytes"
	"errors"
	"net"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/contracts/quorumerr"
	"goquorum.io/v2/contracts/wire"
	"goquorum.io/v2/engine/reactor"
	"goquorum.io/v2/infra/ioruntime"
	"goquorum.io/v2/infra/pool"
)

// fakeHandler is a RequestHandler test double.
type fakeHandler struct {
	calls atomic.Int64
	err   error
}

func (h *fakeHandler) Heartbeat() error {
	h.calls.Add(1)
	return h.err
}

// testEnd bundles a real ioruntime.Runtime with a real reactor.Reactor.
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

// run starts te.r.Run in the background and registers graceful cleanup.
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

// call executes fn on the reactor's pinned goroutine and blocks until done.
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

type testServerHandler struct {
	server      *Server
	fake        *fakeHandler
	onConnected func(connFD int, remoteAddr string)
	onDisconn   func(connFD int, err error)
	onConnErr   func(err error)
}

func (h *testServerHandler) OnMessage(connFD int, hdr FrameHeader, body []byte) {
	switch wire.MessageID(hdr.MessageID) {
	case wire.MsgHeartbeatRequest:
		var req wire.HeartbeatRequest
		_ = req.Unmarshal(body)
		var err error
		if h.fake != nil {
			err = h.fake.Heartbeat()
		}
		resp := wire.HeartbeatResponse{Status: wire.StatusCodeFromError(err)}
		respBody, _ := resp.Marshal()
		_ = h.server.Send(connFD, uint16(wire.MsgHeartbeatResponse), hdr.CorrelationID, respBody)
	}
}

func (h *testServerHandler) OnConnected(connFD int, remoteAddr string) {
	if h.onConnected != nil {
		h.onConnected(connFD, remoteAddr)
	}
}

func (h *testServerHandler) OnDisconnected(connFD int, err error) {
	if h.onDisconn != nil {
		h.onDisconn(connFD, err)
	}
}

func (h *testServerHandler) OnConnectError(err error) {
	if h.onConnErr != nil {
		h.onConnErr(err)
	}
}

// testCluster provides an end-to-end loopback fixture with Server, Client, and TransportAdapter.
type testCluster struct {
	serverEnd *testEnd
	clientEnd *testEnd
	server    *Server
	client    *Client
	adapter   *TransportAdapter
	handler   *fakeHandler
	peerID    node.NodeID
}

func newTestCluster(t *testing.T, handler *fakeHandler) *testCluster {
	t.Helper()
	if handler == nil {
		handler = &fakeHandler{}
	}

	serverEnd := newTestEnd(t)
	serverHandler := &testServerHandler{fake: handler}
	server := NewServer(serverEnd.rt, nil, serverHandler)
	serverHandler.server = server

	serverEnd.r.SetEventHandler(func(ev reactor.Event) { server.HandleCompletion(ev) })
	serverEnd.run(t)

	var listenErr error
	call(t, serverEnd, func() { listenErr = server.Listen("127.0.0.1:0") })
	if listenErr != nil {
		t.Fatalf("Listen: %v", listenErr)
	}

	clientEnd := newTestEnd(t)
	client := NewClient(clientEnd.rt, clientEnd.r, nil, nil)
	adapter := NewTransportAdapter(client, clientEnd.r)

	clientEnd.r.SetEventHandler(func(ev reactor.Event) { client.HandleCompletion(ev) })
	clientEnd.run(t)

	const peerID node.NodeID = "peer-cluster"
	var dialErr error
	call(t, clientEnd, func() { dialErr = adapter.Dial(peerID, server.Addr()) })
	if dialErr != nil {
		t.Fatalf("Dial: %v", dialErr)
	}

	return &testCluster{
		serverEnd: serverEnd,
		clientEnd: clientEnd,
		server:    server,
		client:    client,
		adapter:   adapter,
		handler:   handler,
		peerID:    peerID,
	}
}

func (c *testCluster) sendHeartbeat(t *testing.T) error {
	t.Helper()
	done := make(chan error, 1)
	c.clientEnd.r.PostFunc(func() {
		c.adapter.Heartbeat(c.peerID, func(err error) { done <- err })
	})
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("Heartbeat timed out waiting for reply")
		return nil
	}
}

func TestServerClient_Heartbeat(t *testing.T) {
	t.Run("Success", func(t *testing.T) {
		tc := newTestCluster(t, nil)
		if err := tc.sendHeartbeat(t); err != nil {
			t.Fatalf("Heartbeat failed: %v", err)
		}
		if got := tc.handler.calls.Load(); got != 1 {
			t.Fatalf("handler.Heartbeat called %d times, want 1", got)
		}
		call(t, tc.clientEnd, func() { _ = tc.adapter.Close() })
	})

	t.Run("HandlerError", func(t *testing.T) {
		tc := newTestCluster(t, &fakeHandler{err: quorumerr.ErrStorageClosed})
		err := tc.sendHeartbeat(t)
		if !errors.Is(err, quorumerr.ErrStorageClosed) {
			t.Fatalf("Heartbeat error = %v, want %v", err, quorumerr.ErrStorageClosed)
		}
		call(t, tc.clientEnd, func() { _ = tc.adapter.Close() })
	})

	t.Run("ConnectionRefused", func(t *testing.T) {
		deadAddr := reserveDeadAddr(t)

		clientEnd := newTestEnd(t)
		client := NewClient(clientEnd.rt, clientEnd.r, nil, nil)
		adapter := NewTransportAdapter(client, clientEnd.r)
		clientEnd.r.SetEventHandler(func(ev reactor.Event) { client.HandleCompletion(ev) })
		clientEnd.run(t)

		dialDone := make(chan error, 1)
		clientEnd.r.PostFunc(func() { dialDone <- adapter.Dial("peer-refused", deadAddr) })
		select {
		case err := <-dialDone:
			if err == nil {
				t.Fatal("Dial to dead address unexpectedly succeeded")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Dial did not return promptly for refused connection")
		}
	})

	t.Run("Timeout", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("net.Listen: %v", err)
		}
		defer ln.Close()

		go func() {
			conn, err := ln.Accept()
			if err == nil {
				t.Cleanup(func() { _ = conn.Close() })
			}
		}()

		clientEnd := newTestEnd(t)
		client := NewClient(clientEnd.rt, clientEnd.r, nil, nil)
		adapter := NewTransportAdapter(client, clientEnd.r)
		adapter.requestTimeout = 300 * time.Millisecond
		clientEnd.r.SetEventHandler(func(ev reactor.Event) { client.HandleCompletion(ev) })
		clientEnd.run(t)

		const peerID node.NodeID = "peer-timeout"
		var dialErr error
		call(t, clientEnd, func() { dialErr = adapter.Dial(peerID, ln.Addr().String()) })
		if dialErr != nil {
			t.Fatalf("Dial: %v", dialErr)
		}

		hbDone := make(chan error, 1)
		clientEnd.r.PostFunc(func() {
			adapter.Heartbeat(peerID, func(err error) { hbDone <- err })
		})
		select {
		case err := <-hbDone:
			if err == nil {
				t.Fatal("Heartbeat unexpectedly succeeded against non-responsive peer")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("Heartbeat did not time out; it hung")
		}

		call(t, clientEnd, func() { _ = adapter.Close() })
	})
}

func TestServerClient_LifecycleHooks(t *testing.T) {
	serverEnd := newTestEnd(t)
	serverConnected := make(chan string, 1)
	serverDisconnected := make(chan error, 1)
	serverConnectErr := make(chan error, 1)

	sHandler := &testServerHandler{
		onConnected: func(connFD int, remoteAddr string) {
			serverConnected <- remoteAddr
		},
		onDisconn: func(connFD int, err error) {
			serverDisconnected <- err
		},
		onConnErr: func(err error) {
			serverConnectErr <- err
		},
	}
	server := NewServer(serverEnd.rt, nil, sHandler)
	sHandler.server = server

	serverEnd.r.SetEventHandler(func(ev reactor.Event) { server.HandleCompletion(ev) })
	serverEnd.run(t)

	call(t, serverEnd, func() {
		if err := server.Listen("127.0.0.1:0"); err != nil {
			t.Fatalf("Listen: %v", err)
		}
	})

	clientEnd := newTestEnd(t)
	client := NewClient(clientEnd.rt, clientEnd.r, nil, nil)
	adapter := NewTransportAdapter(client, clientEnd.r)
	clientEnd.r.SetEventHandler(func(ev reactor.Event) { client.HandleCompletion(ev) })
	clientEnd.run(t)

	clientConnected := make(chan node.NodeID, 1)
	clientDisconnected := make(chan node.NodeID, 1)
	clientConnectError := make(chan node.NodeID, 1)

	adapter.OnConnectedHook = func(id node.NodeID, addr string) {
		clientConnected <- id
	}
	adapter.OnDisconnectedHook = func(id node.NodeID, err error) {
		clientDisconnected <- id
	}
	adapter.OnConnectErrorHook = func(id node.NodeID, err error) {
		clientConnectError <- id
	}

	const peerID node.NodeID = "node-b"
	var dialErr error
	call(t, clientEnd, func() { dialErr = adapter.Dial(peerID, server.Addr()) })
	if dialErr != nil {
		t.Fatalf("Dial: %v", dialErr)
	}

	select {
	case id := <-clientConnected:
		if id != peerID {
			t.Fatalf("OnConnected got %q, want %q", id, peerID)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("OnConnected did not fire")
	}

	select {
	case <-serverConnected:
	case <-time.After(3 * time.Second):
		t.Fatal("OnConnected did not fire")
	}

	// Send a heartbeat roundtrip
	hbDone := make(chan error, 1)
	clientEnd.r.PostFunc(func() {
		adapter.Heartbeat(peerID, func(err error) { hbDone <- err })
	})
	select {
	case err := <-hbDone:
		if err != nil {
			t.Fatalf("Heartbeat: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Heartbeat did not complete")
	}

	// Close client side to trigger client disconnect hook
	call(t, clientEnd, func() { _ = adapter.Close() })

	select {
	case id := <-clientDisconnected:
		if id != peerID {
			t.Fatalf("OnDisconnected got %q, want %q", id, peerID)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("OnDisconnected did not fire")
	}

	// Test server.OnConnectError
	call(t, serverEnd, func() {
		server.HandleCompletion(reactor.Event{
			UserData: makeUserData(server.listenFD, 0),
			Err:      syscall.ECONNRESET,
		})
	})
	select {
	case err := <-serverConnectErr:
		if !errors.Is(err, syscall.ECONNRESET) {
			t.Fatalf("server OnConnectError got %v, want ECONNRESET", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server OnConnectError did not fire")
	}

	// Close server side to trigger server disconnect hook
	call(t, serverEnd, func() { _ = server.Close() })

	select {
	case <-serverDisconnected:
	case <-time.After(3 * time.Second):
		t.Fatal("OnDisconnected did not fire")
	}

	// Test client.OnConnectError
	deadAddr := reserveDeadAddr(t)
	call(t, clientEnd, func() { _ = adapter.Dial("dead-node", deadAddr) })
	select {
	case id := <-clientConnectError:
		if id != "dead-node" {
			t.Fatalf("OnConnectError got %q, want dead-node", id)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("OnConnectError did not fire")
	}
}

func TestServerClient_OnMessage_UnsolicitedPush(t *testing.T) {
	serverEnd := newTestEnd(t)
	var acceptedFD int
	sHandler := &testServerHandler{
		onConnected: func(connFD int, remoteAddr string) {
			acceptedFD = connFD
		},
	}
	server := NewServer(serverEnd.rt, nil, sHandler)
	sHandler.server = server

	serverEnd.r.SetEventHandler(func(ev reactor.Event) { server.HandleCompletion(ev) })
	serverEnd.run(t)

	call(t, serverEnd, func() {
		if err := server.Listen("127.0.0.1:0"); err != nil {
			t.Fatalf("Listen: %v", err)
		}
	})

	clientEnd := newTestEnd(t)
	client := NewClient(clientEnd.rt, clientEnd.r, nil, nil)
	adapter := NewTransportAdapter(client, clientEnd.r)
	clientEnd.r.SetEventHandler(func(ev reactor.Event) { client.HandleCompletion(ev) })
	clientEnd.run(t)

	clientReceivedMessage := make(chan []byte, 1)
	adapter.OnMessageHook = func(id node.NodeID, hdr FrameHeader, body []byte) {
		if wire.MessageID(hdr.MessageID) == wire.MsgGossipExchangeRequest {
			clientReceivedMessage <- body
		}
	}

	const peerID node.NodeID = "peer-push"
	call(t, clientEnd, func() {
		if err := adapter.Dial(peerID, server.Addr()); err != nil {
			t.Fatalf("Dial: %v", err)
		}
	})

	time.Sleep(50 * time.Millisecond)

	pushPayload := []byte("gossip-state-update")
	call(t, serverEnd, func() {
		if err := server.Send(acceptedFD, uint16(wire.MsgGossipExchangeRequest), 0, pushPayload); err != nil {
			t.Fatalf("server.Send: %v", err)
		}
	})

	select {
	case body := <-clientReceivedMessage:
		if !bytes.Equal(body, pushPayload) {
			t.Fatalf("client.OnMessage got %q, want %q", body, pushPayload)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("client.OnMessage did not fire for server push")
	}

	call(t, clientEnd, func() { _ = adapter.Close() })
	call(t, serverEnd, func() { _ = server.Close() })
}

func TestSharedBucketArrayPool_ClientServerAdapter(t *testing.T) {
	sharedPool := pool.NewDefaultArrayPool[byte]()

	serverEnd := newTestEnd(t)
	sHandler := &testServerHandler{}
	server := NewServer(serverEnd.rt, sharedPool, sHandler)
	sHandler.server = server

	serverEnd.r.SetEventHandler(func(ev reactor.Event) { server.HandleCompletion(ev) })
	serverEnd.run(t)

	call(t, serverEnd, func() {
		if err := server.Listen("127.0.0.1:0"); err != nil {
			t.Fatalf("Listen: %v", err)
		}
	})

	clientEnd := newTestEnd(t)
	client := NewClient(clientEnd.rt, clientEnd.r, sharedPool, nil)
	adapter := NewTransportAdapter(client, clientEnd.r)

	if client.BytePool() != sharedPool {
		t.Fatalf("client.BytePool() != sharedPool")
	}
	if server.BytePool() != sharedPool {
		t.Fatalf("server.BytePool() != sharedPool")
	}
	if adapter.BytePool() != sharedPool {
		t.Fatalf("adapter.BytePool() != sharedPool")
	}

	clientEnd.r.SetEventHandler(func(ev reactor.Event) { client.HandleCompletion(ev) })
	clientEnd.run(t)

	const peerID node.NodeID = "peer-shared-pool"
	call(t, clientEnd, func() {
		if err := adapter.Dial(peerID, server.Addr()); err != nil {
			t.Fatalf("Dial: %v", err)
		}
	})

	hbDone := make(chan error, 1)
	clientEnd.r.PostFunc(func() {
		adapter.Heartbeat(peerID, func(err error) { hbDone <- err })
	})

	select {
	case err := <-hbDone:
		if err != nil {
			t.Fatalf("Heartbeat failed with shared pool: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Heartbeat timed out with shared pool")
	}

	call(t, clientEnd, func() { _ = adapter.Close() })
	call(t, serverEnd, func() { _ = server.Close() })
}

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
