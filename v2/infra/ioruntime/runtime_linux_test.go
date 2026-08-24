//go:build linux

package ioruntime

import (
	"os"
	"syscall"
	"testing"
	"time"

	"goquorum.io/v2/infra/reactor"
)

func newTestRuntime(t *testing.T) *Runtime {
	t.Helper()
	rt, err := New(64)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	return rt
}

func runReactorInBackground(t *testing.T, r *reactor.Reactor) {
	t.Helper()
	errCh := make(chan error, 1)
	go func() { errCh <- r.Run() }()
	t.Cleanup(func() {
		r.RequestStop()
		select {
		case err := <-errCh:
			if err != nil {
				t.Errorf("Run returned error: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Run did not return after RequestStop")
		}
	})
}

// TestRuntime_FileWriteRead proves a real io_uring write followed by a real
// io_uring read round-trips through this host's actual kernel, with both
// completions delivered as reactor.Events on the reactor goroutine.
func TestRuntime_FileWriteRead(t *testing.T) {
	rt := newTestRuntime(t)
	r := reactor.New(rt)

	f, err := os.CreateTemp(t.TempDir(), "ioruntime-*.dat")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	defer f.Close()
	fd := int(f.Fd())

	const writeUserData uint64 = 1
	const readUserData uint64 = 2
	payload := []byte("hello io_uring")

	writeDone := make(chan reactor.Event, 1)
	readDone := make(chan reactor.Event, 1)
	readBuf := make([]byte, len(payload))

	r.SetEventHandler(func(ev reactor.Event) {
		switch ev.UserData {
		case writeUserData:
			writeDone <- ev
			if err := rt.SubmitPread(fd, readBuf, 0, readUserData); err != nil {
				t.Errorf("SubmitPread: %v", err)
			}
		case readUserData:
			readDone <- ev
		}
	})
	runReactorInBackground(t, r)

	if err := rt.SubmitPwrite(fd, payload, 0, writeUserData); err != nil {
		t.Fatalf("SubmitPwrite: %v", err)
	}

	select {
	case ev := <-writeDone:
		if ev.Err != nil {
			t.Fatalf("write completion error: %v", ev.Err)
		}
		if int(ev.Result) != len(payload) {
			t.Fatalf("expected to write %d bytes, wrote %d", len(payload), ev.Result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("write never completed")
	}

	select {
	case ev := <-readDone:
		if ev.Err != nil {
			t.Fatalf("read completion error: %v", ev.Err)
		}
		if int(ev.Result) != len(payload) {
			t.Fatalf("expected to read %d bytes, read %d", len(payload), ev.Result)
		}
		if string(readBuf) != string(payload) {
			t.Fatalf("round trip mismatch: got %q, want %q", readBuf, payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("read never completed")
	}
}

// TestRuntime_TCPLoopback proves a real io_uring accept + send + recv over
// a real localhost TCP connection, entirely driven by one reactor.Reactor
// on one goroutine.
func TestRuntime_TCPLoopback(t *testing.T) {
	rt := newTestRuntime(t)
	r := reactor.New(rt)

	listenFD, listenAddr := mustListen(t)
	defer syscall.Close(listenFD)

	const (
		acceptUserData uint64 = 100
		serverRecvData uint64 = 101
		clientSendData uint64 = 102
	)

	message := []byte("ping")
	serverRecvBuf := make([]byte, len(message))
	serverGotConn := make(chan int, 1)
	serverGotMessage := make(chan string, 1)

	clientFD := mustConnect(t, listenAddr)
	defer syscall.Close(clientFD)

	r.SetEventHandler(func(ev reactor.Event) {
		switch ev.UserData {
		case acceptUserData:
			if ev.Err != nil {
				t.Errorf("accept completion error: %v", ev.Err)
				return
			}
			connFD := int(ev.Result)
			serverGotConn <- connFD
			if err := rt.SubmitRecv(connFD, serverRecvBuf, serverRecvData); err != nil {
				t.Errorf("SubmitRecv: %v", err)
			}
		case serverRecvData:
			if ev.Err != nil {
				t.Errorf("recv completion error: %v", ev.Err)
				return
			}
			serverGotMessage <- string(serverRecvBuf[:ev.Result])
		case clientSendData:
			if ev.Err != nil {
				t.Errorf("send completion error: %v", ev.Err)
			}
		}
	})
	runReactorInBackground(t, r)

	if err := rt.SubmitAccept(listenFD, acceptUserData); err != nil {
		t.Fatalf("SubmitAccept: %v", err)
	}

	var connFD int
	select {
	case connFD = <-serverGotConn:
		defer syscall.Close(connFD)
	case <-time.After(5 * time.Second):
		t.Fatal("accept never completed")
	}

	if err := rt.SubmitSend(clientFD, message, clientSendData); err != nil {
		t.Fatalf("SubmitSend: %v", err)
	}

	select {
	case got := <-serverGotMessage:
		if got != string(message) {
			t.Fatalf("got %q, want %q", got, message)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server never received the message")
	}
}

func mustListen(t *testing.T) (fd int, addr syscall.SockaddrInet4) {
	t.Helper()
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatalf("socket: %v", err)
	}
	sa := &syscall.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}}
	if err := syscall.Bind(fd, sa); err != nil {
		t.Fatalf("bind: %v", err)
	}
	if err := syscall.Listen(fd, 1); err != nil {
		t.Fatalf("listen: %v", err)
	}
	bound, err := syscall.Getsockname(fd)
	if err != nil {
		t.Fatalf("getsockname: %v", err)
	}
	return fd, *bound.(*syscall.SockaddrInet4)
}

func mustConnect(t *testing.T, addr syscall.SockaddrInet4) int {
	t.Helper()
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatalf("socket: %v", err)
	}
	if err := syscall.Connect(fd, &addr); err != nil {
		t.Fatalf("connect: %v", err)
	}
	return fd
}
