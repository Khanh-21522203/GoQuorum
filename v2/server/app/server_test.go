package app

import (
	"fmt"
	"net"
	"path/filepath"
	"testing"
	"time"

	"goquorum.io/v2/contracts/vclock"
	"goquorum.io/v2/engine/adapter"
	"goquorum.io/v2/infra/config"
)

func getFreePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func TestServer_TwoNode_ReplicationAndRead(t *testing.T) {
	port1 := getFreePort(t)
	port2 := getFreePort(t)
	for port2 == port1 {
		port2 = getFreePort(t)
	}
	httpPort1 := getFreePort(t)
	httpPort2 := getFreePort(t)

	addr1 := fmt.Sprintf("127.0.0.1:%d", port1)
	addr2 := fmt.Sprintf("127.0.0.1:%d", port2)

	tmpDir := t.TempDir()

	cfg1 := &config.Config{
		Node: config.NodeConfig{
			NodeID:  "node-1",
			DataDir: filepath.Join(tmpDir, "node-1"),
		},
		Cluster: config.ClusterConfig{
			NodeID: "node-1",
			Members: []config.MemberConfig{
				{ID: "node-1", Addr: addr1, HTTPAddr: addr1},
				{ID: "node-2", Addr: addr2, HTTPAddr: addr2},
			},
			HeartbeatInterval: 100 * time.Millisecond,
			HeartbeatTimeout:  500 * time.Millisecond,
		},
		Server: config.ServerConfig{
			HTTPAddr: fmt.Sprintf("127.0.0.1:%d", httpPort1),
		},
		QuorumConfig: config.QuorumConfig{
			N: 2,
			R: 2,
			W: 2,
		},
	}

	cfg2 := &config.Config{
		Node: config.NodeConfig{
			NodeID:  "node-2",
			DataDir: filepath.Join(tmpDir, "node-2"),
		},
		Cluster: config.ClusterConfig{
			NodeID: "node-2",
			Members: []config.MemberConfig{
				{ID: "node-1", Addr: addr1, HTTPAddr: addr1},
				{ID: "node-2", Addr: addr2, HTTPAddr: addr2},
			},
			HeartbeatInterval: 100 * time.Millisecond,
			HeartbeatTimeout:  500 * time.Millisecond,
		},
		Server: config.ServerConfig{
			HTTPAddr: fmt.Sprintf("127.0.0.1:%d", httpPort2),
		},
		QuorumConfig: config.QuorumConfig{
			N: 2,
			R: 2,
			W: 2,
		},
	}

	s2, err := New(cfg2)
	if err != nil {
		t.Fatalf("New s2: %v", err)
	}

	s1, err := New(cfg1)
	if err != nil {
		t.Fatalf("New s1: %v", err)
	}

	done1 := make(chan error, 1)
	go func() { done1 <- s1.Run() }()

	done2 := make(chan error, 1)
	go func() { done2 <- s2.Run() }()

	defer func() {
		s1.RequestStop()
		<-done1
		s1.Stop()

		s2.RequestStop()
		<-done2
		s2.Stop()
	}()

	// Wait for sockets to connect and establish
	time.Sleep(100 * time.Millisecond)

	// Perform a quorum write from Node 1 (N=2, W=2)
	doneCh := make(chan error, 1)
	vc := vclock.NewVectorClock()
	s1.coordinator.Put("my-key", []byte("hello-cluster"), vc, func(clock vclock.VectorClock, err error) {
		doneCh <- err
	})

	select {
	case err := <-doneCh:
		if err != nil {
			t.Fatalf("coordinator.Put failed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("coordinator.Put timed out waiting for quorum (2/2)")
	}

	// Perform a quorum read from Node 1 (N=2, R=2)
	type readRes struct {
		val string
		err error
	}
	readCh := make(chan readRes, 1)
	s1.coordinator.Get("my-key", func(siblings []adapter.Sibling, err error) {
		if err != nil {
			readCh <- readRes{err: err}
			return
		}
		if len(siblings) == 0 {
			readCh <- readRes{err: fmt.Errorf("no siblings returned")}
			return
		}
		readCh <- readRes{val: string(siblings[0].Value)}
	})

	select {
	case res := <-readCh:
		if res.err != nil {
			t.Fatalf("coordinator.Get failed: %v", res.err)
		}
		if res.val != "hello-cluster" {
			t.Fatalf("expected 'hello-cluster', got %s", res.val)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("coordinator.Get timed out waiting for quorum (2/2)")
	}
}
