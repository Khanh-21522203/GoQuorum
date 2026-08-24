package app

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"time"

	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/engine/adapter"
	"goquorum.io/v2/engine/coordinator"
	"goquorum.io/v2/engine/hashring"
	"goquorum.io/v2/engine/membership"
	gatewayhttp "goquorum.io/v2/gateway/http"
	"goquorum.io/v2/infra/affinity"
	"goquorum.io/v2/infra/config"
	"goquorum.io/v2/infra/ioruntime"
	"goquorum.io/v2/infra/pool"
	"goquorum.io/v2/infra/reactor"
	"goquorum.io/v2/infra/storage/journal"
	"goquorum.io/v2/infra/transport/iouring"
	"goquorum.io/v2/server/api"
)

// defaultVNodeCount is the number of virtual tokens each physical node
// claims on the consistent hash ring (mirrors v1).
const defaultVNodeCount = 256

// ioURingQueueDepth is the fixed capacity of the io_uring submission
// and completion queues driven by engine/reactor.
const ioURingQueueDepth = 256

// version is the GoQuorum software release version reported in admin Health.
const version = "2.0.0"

// walFileName is the on-disk filename of the journal file inside
// Config.Node.DataDir.
const walFileName = "data.log"

// Server is GoQuorum v2's composition root.
type Server struct {
	cfg *config.Config

	runtime *ioruntime.Runtime
	reactor *reactor.Reactor

	store         adapter.Storage
	serverAdapter *adapter.ServerAdapter

	ring        *hashring.HashRing
	membership  *membership.MembershipManager
	coordinator *coordinator.Coordinator

	clientAPI   *api.ClientAPI
	adminAPI    *api.AdminAPI
	internalAPI *api.InternalAPI

	gateway    *gatewayhttp.Gateway
	httpServer *http.Server
}

// New builds the full GoQuorum v2 dependency graph from cfg.
func New(cfg *config.Config) (*Server, error) {
	// 1. The single OS thread every engine subsystem below will run on.
	rt, err := ioruntime.New(ioURingQueueDepth)
	if err != nil {
		return nil, fmt.Errorf("open io_uring: %w", err)
	}
	r := reactor.New(rt)

	// 2. Storage port (infra/storage/journal raw WAL adapted to engine/adapter.Storage).
	rawStore, err := journal.Open(rt, journal.Options{
		Path:    filepath.Join(cfg.Node.DataDir, walFileName),
		Reactor: r,
	})
	if err != nil {
		return nil, fmt.Errorf("open storage: %w", err)
	}
	store := adapter.NewStorageAdapter(rawStore, cfg.Node.NodeID)

	// 3. Membership view.
	mm := membership.NewMembershipManager(membershipConfig(cfg.Cluster), version)

	// 4. Hash ring, populated from the statically-configured cluster members.
	ring := hashring.NewHashRing(defaultVNodeCount)
	localHTTPAddr := ""
	for _, m := range cfg.Cluster.Members {
		n := &node.Node{
			ID:               m.ID,
			Addr:             m.Addr,
			State:            node.NodeStateActive,
			VirtualNodeCount: defaultVNodeCount,
		}
		if err := ring.AddNode(n); err != nil {
			return nil, fmt.Errorf("add node %s to ring: %w", m.ID, err)
		}
		if m.ID == cfg.Node.NodeID {
			localHTTPAddr = m.HTTPAddr
		} else {
			mm.AddPeer(m.ID, m.Addr, m.HTTPAddr)
		}
	}
	if localHTTPAddr == "" {
		return nil, fmt.Errorf("node %s not found in cfg.Cluster.Members (needed to know its own internal-RPC listen address)", cfg.Node.NodeID)
	}

	// 5. Outbound transport port (engine/adapter.ClientAdapter wraps iouring.Client).
	bytePool := pool.NewDefaultArrayPool[byte]()
	tc := iouring.NewClient(rt, r, bytePool, nil)
	clientAdapter := adapter.NewClientAdapter(tc, r)

	// 6. Coordinator, composed over the storage/transport ports plus the
	// hash ring, membership view, and reactor.
	coord := coordinator.NewCoordinator(cfg.Node.NodeID, ring, store, clientAdapter, mm, r, cfg.Quorum())

	// 7. Service-API implementations over the coordinator and ports.
	clientAPI := api.NewClientAPI(coord)
	adminAPI := api.NewAdminAPI(store, mm, cfg.Node.NodeID, version, time.Now())
	internalAPI := api.NewInternalAPI(store, mm, coord)

	// 8. Inbound transport port (engine/adapter.ServerAdapter wraps iouring.Server).
	ts := iouring.NewServer(rt, r, bytePool, nil)
	serverAdapter := adapter.NewServerAdapter(ts, internalAPI)
	if err := serverAdapter.Listen(localHTTPAddr); err != nil {
		return nil, fmt.Errorf("listen on %s: %w", localHTTPAddr, err)
	}

	// 9. Gateway, mounted over the coordinator.
	gw := gatewayhttp.New(coord)

	return &Server{
		cfg:           cfg,
		runtime:       rt,
		reactor:       r,
		store:         store,
		serverAdapter: serverAdapter,
		ring:          ring,
		membership:    mm,
		coordinator:   coord,
		clientAPI:     clientAPI,
		adminAPI:      adminAPI,
		internalAPI:   internalAPI,
		gateway:       gw,
	}, nil
}

// Start starts the coordinator's background subsystems and the HTTP
// server hosting the gateway.
func (s *Server) Start() error {
	if err := s.coordinator.Start(); err != nil {
		return fmt.Errorf("start coordinator: %w", err)
	}

	s.httpServer = &http.Server{
		Addr:         s.cfg.Server.HTTPAddr,
		Handler:      s.gateway.Handler(),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	ln, err := net.Listen("tcp", s.httpServer.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.httpServer.Addr, err)
	}

	go func() {
		_ = s.httpServer.Serve(ln)
	}()

	return nil
}

// Run blocks the calling goroutine.
func (s *Server) Run() error {
	if core := s.cfg.Node.ReactorCPUCore; core != nil {
		if err := affinity.LockToCore(*core); err != nil {
			return fmt.Errorf("pin reactor to CPU core %d: %w", *core, err)
		}
	}
	return s.reactor.Run()
}

// RequestStop asks Run to return once pending reactor work drains.
func (s *Server) RequestStop() {
	if s.httpServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = s.httpServer.Shutdown(ctx)
	}
	s.reactor.RequestStop()
}

// Stop releases every resource this Server holds.
func (s *Server) Stop() {
	if s.coordinator != nil {
		s.coordinator.Stop()
	}
	if s.serverAdapter != nil {
		_ = s.serverAdapter.Close()
	}
	if s.store != nil {
		_ = s.store.Close()
	}
	if s.runtime != nil {
		_ = s.runtime.Close()
	}
}

// membershipConfig converts the loaded cluster configuration into engine/membership.Config.
func membershipConfig(c config.ClusterConfig) membership.Config {
	members := make([]membership.MemberConfig, len(c.Members))
	for i, m := range c.Members {
		members[i] = membership.MemberConfig{
			ID:       m.ID,
			Addr:     m.Addr,
			HTTPAddr: m.HTTPAddr,
		}
	}
	return membership.Config{
		NodeID:            c.NodeID,
		ListenAddr:        c.ListenAddr,
		Members:           members,
		HeartbeatInterval: c.HeartbeatInterval,
		HeartbeatTimeout:  c.HeartbeatTimeout,
		FailureThreshold:  c.FailureThreshold,
		BootstrapTimeout:  c.BootstrapTimeout,
	}
}
