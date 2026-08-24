package iouring

import (
	"bytes"
	"errors"
	"net"
	"syscall"
	"testing"
	"time"

	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/contracts/wire"
	"goquorum.io/v2/infra/ioruntime"
	"goquorum.io/v2/infra/pool"
	"goquorum.io/v2/infra/reactor"
)

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
	onMessage   func(connFD int, hdr FrameHeader, body []byte)
	onConnected func(connFD int, remoteAddr string)
	onDisconn   func(connFD int, err error)
	onConnErr   func(err error)
}

func (h *testServerHandler) OnMessage(connFD int, hdr FrameHeader, body []byte) {
	if h.onMessage != nil {
		h.onMessage(connFD, hdr, body)
		return
	}
	// Default echo reply
	_ = h.server.Send(connFD, hdr.MessageID+1, hdr.CorrelationID, body)
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

type testClientHandler struct {
	onFrame      func(id node.NodeID, hdr FrameHeader, body []byte)
	onConnected  func(id node.NodeID, addr string)
	onDisconn    func(id node.NodeID, err error)
	onConnectErr func(id node.NodeID, err error)
}

func (h *testClientHandler) OnFrame(id node.NodeID, hdr FrameHeader, body []byte) {
	if h.onFrame != nil {
		h.onFrame(id, hdr, body)
	}
}

func (h *testClientHandler) OnConnected(id node.NodeID, addr string) {
	if h.onConnected != nil {
		h.onConnected(id, addr)
	}
}

func (h *testClientHandler) OnDisconnected(id node.NodeID, err error) {
	if h.onDisconn != nil {
		h.onDisconn(id, err)
	}
}

func (h *testClientHandler) OnConnectError(id node.NodeID, err error) {
	if h.onConnectErr != nil {
		h.onConnectErr(id, err)
	}
}

func TestServerClient_FrameExchange(t *testing.T) {
	serverEnd := newTestEnd(t)
	sHandler := &testServerHandler{}
	server := NewServer(serverEnd.rt, serverEnd.r, nil, sHandler)
	sHandler.server = server
	serverEnd.run(t)

	var listenErr error
	call(t, serverEnd, func() { listenErr = server.Listen("127.0.0.1:0") })
	if listenErr != nil {
		t.Fatalf("Listen: %v", listenErr)
	}

	clientEnd := newTestEnd(t)
	frameCh := make(chan []byte, 1)
	cHandler := &testClientHandler{
		onFrame: func(id node.NodeID, hdr FrameHeader, body []byte) {
			if hdr.CorrelationID == 42 && hdr.MessageID == uint16(wire.MsgHeartbeatResponse) {
				frameCh <- body
			}
		},
	}
	client := NewClient(clientEnd.rt, clientEnd.r, nil, cHandler)
	clientEnd.r.SetEventHandler(func(ev reactor.Event) { client.HandleCompletion(ev) })
	clientEnd.run(t)

	const peerID node.NodeID = "peer-1"
	call(t, clientEnd, func() {
		if err := client.Dial(peerID, server.Addr()); err != nil {
			t.Fatalf("Dial: %v", err)
		}
	})

	sendPayload := []byte("hello-iouring")
	call(t, clientEnd, func() {
		if err := client.Request(peerID, uint16(wire.MsgHeartbeatRequest), 42, sendPayload); err != nil {
			t.Fatalf("client.Request: %v", err)
		}
	})

	select {
	case body := <-frameCh:
		if !bytes.Equal(body, sendPayload) {
			t.Fatalf("got body %q, want %q", body, sendPayload)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for reply frame")
	}

	call(t, clientEnd, func() { _ = client.Close() })
	call(t, serverEnd, func() { _ = server.Close() })
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
	server := NewServer(serverEnd.rt, serverEnd.r, nil, sHandler)
	sHandler.server = server
	serverEnd.run(t)

	call(t, serverEnd, func() {
		if err := server.Listen("127.0.0.1:0"); err != nil {
			t.Fatalf("Listen: %v", err)
		}
	})

	clientEnd := newTestEnd(t)
	clientConnected := make(chan node.NodeID, 1)
	clientDisconnected := make(chan node.NodeID, 1)
	clientConnectError := make(chan node.NodeID, 1)

	cHandler := &testClientHandler{
		onConnected: func(id node.NodeID, addr string) {
			clientConnected <- id
		},
		onDisconn: func(id node.NodeID, err error) {
			clientDisconnected <- id
		},
		onConnectErr: func(id node.NodeID, err error) {
			clientConnectError <- id
		},
	}
	client := NewClient(clientEnd.rt, clientEnd.r, nil, cHandler)
	clientEnd.r.SetEventHandler(func(ev reactor.Event) { client.HandleCompletion(ev) })
	clientEnd.run(t)

	const peerID node.NodeID = "node-b"
	call(t, clientEnd, func() {
		if err := client.Dial(peerID, server.Addr()); err != nil {
			t.Fatalf("Dial: %v", err)
		}
	})

	select {
	case addr := <-serverConnected:
		if addr == "" {
			t.Fatal("server OnConnected got empty address")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server OnConnected did not fire")
	}

	select {
	case id := <-clientConnected:
		if id != peerID {
			t.Fatalf("client OnConnected got %q, want %q", id, peerID)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("client OnConnected did not fire")
	}

	// Close client side to trigger disconnect hooks
	call(t, clientEnd, func() { _ = client.Close() })

	select {
	case id := <-clientDisconnected:
		if id != peerID {
			t.Fatalf("client OnDisconnected got %q, want %q", id, peerID)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("client OnDisconnected did not fire")
	}

	// Test server.OnConnectError (white-box completion test on listenFD)
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

	// Close server side
	call(t, serverEnd, func() { _ = server.Close() })

	select {
	case <-serverDisconnected:
	case <-time.After(3 * time.Second):
		t.Fatal("server OnDisconnected did not fire")
	}

	// Test client.OnConnectError
	deadAddr := reserveDeadAddr(t)
	call(t, clientEnd, func() { _ = client.Dial("dead-node", deadAddr) })
	select {
	case id := <-clientConnectError:
		if id != "dead-node" {
			t.Fatalf("client OnConnectError got %q, want dead-node", id)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("client OnConnectError did not fire")
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
	server := NewServer(serverEnd.rt, serverEnd.r, nil, sHandler)
	sHandler.server = server
	serverEnd.run(t)

	call(t, serverEnd, func() {
		if err := server.Listen("127.0.0.1:0"); err != nil {
			t.Fatalf("Listen: %v", err)
		}
	})

	clientEnd := newTestEnd(t)
	clientReceivedMessage := make(chan []byte, 1)
	cHandler := &testClientHandler{
		onFrame: func(id node.NodeID, hdr FrameHeader, body []byte) {
			if wire.MessageID(hdr.MessageID) == wire.MsgGossipExchangeRequest {
				clientReceivedMessage <- body
			}
		},
	}
	client := NewClient(clientEnd.rt, clientEnd.r, nil, cHandler)
	clientEnd.r.SetEventHandler(func(ev reactor.Event) { client.HandleCompletion(ev) })
	clientEnd.run(t)

	const peerID node.NodeID = "peer-push"
	call(t, clientEnd, func() {
		if err := client.Dial(peerID, server.Addr()); err != nil {
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
			t.Fatalf("client received got %q, want %q", body, pushPayload)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("client did not receive server push")
	}

	call(t, clientEnd, func() { _ = client.Close() })
	call(t, serverEnd, func() { _ = server.Close() })
}

func TestSharedBucketArrayPool_ClientServer(t *testing.T) {
	sharedPool := pool.NewDefaultArrayPool[byte]()

	end := newTestEnd(t)
	sHandler := &testServerHandler{}
	server := NewServer(end.rt, end.r, sharedPool, sHandler)
	sHandler.server = server

	client := NewClient(end.rt, end.r, sharedPool, nil)

	if client.BytePool() != sharedPool {
		t.Fatalf("client.BytePool() != sharedPool")
	}
	if server.BytePool() != sharedPool {
		t.Fatalf("server.BytePool() != sharedPool")
	}

	end.r.SetEventHandler(func(ev reactor.Event) {
		if client.HandleCompletion(ev) {
			return
		}
		server.HandleCompletion(ev)
	})
	end.run(t)

	call(t, end, func() {
		if err := server.Listen("127.0.0.1:0"); err != nil {
			t.Fatalf("Listen: %v", err)
		}
	})

	const peerID node.NodeID = "peer-shared-pool"
	call(t, end, func() {
		if err := client.Dial(peerID, server.Addr()); err != nil {
			t.Fatalf("Dial: %v", err)
		}
	})

	call(t, end, func() {
		_ = client.Close()
		_ = server.Close()
	})
}

func reserveDeadAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}
