package app

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"time"

	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/contracts/wire"
	"goquorum.io/v2/engine/coordinator"
	"goquorum.io/v2/engine/hashring"
	"goquorum.io/v2/engine/membership"
	"goquorum.io/v2/engine/reactor"
	enginestorage "goquorum.io/v2/engine/storage"
	enginetransport "goquorum.io/v2/engine/transport"
	gatewayhttp "goquorum.io/v2/gateway/http"
	"goquorum.io/v2/infra/affinity"
	"goquorum.io/v2/infra/config"
	"goquorum.io/v2/infra/ioruntime"
	"goquorum.io/v2/infra/pool"
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

	store         enginestorage.Storage
	transport     enginetransport.Transport
	iouringClient *iouring.Client
	iouringServer *iouring.Server

	ring        *hashring.HashRing
	membership  *membership.MembershipManager
	coordinator *coordinator.Coordinator

	clientAPI   *api.ClientAPI
	adminAPI    *api.AdminAPI
	internalAPI *api.InternalAPI

	gateway    *gatewayhttp.Gateway
	httpServer *http.Server
}

type appServerHandler struct {
	server *iouring.Server
}

func (h *appServerHandler) OnMessage(connFD int, hdr iouring.FrameHeader, body []byte) {
	switch wire.MessageID(hdr.MessageID) {
	case wire.MsgHeartbeatRequest:
		resp := wire.HeartbeatResponse{Status: wire.StatusOK}
		respBody, _ := resp.Marshal()
		_ = h.server.Send(connFD, uint16(wire.MsgHeartbeatResponse), hdr.CorrelationID, respBody)
	}
}

func (h *appServerHandler) OnConnected(connFD int, remoteAddr string) {}
func (h *appServerHandler) OnDisconnected(connFD int, err error)      {}
func (h *appServerHandler) OnConnectError(err error)                  {}

// New builds the full GoQuorum v2 dependency graph from cfg.
func New(cfg *config.Config) (*Server, error) {
	// 1. The single OS thread every engine subsystem below will run on.
	rt, err := ioruntime.New(ioURingQueueDepth)
	if err != nil {
		return nil, fmt.Errorf("open io_uring: %w", err)
	}
	r := reactor.New(rt)

	// 2. Storage port (infra/storage/journal raw WAL adapted to engine/storage.Storage).
	rawStore, err := journal.Open(rt, journal.Options{
		Path: filepath.Join(cfg.Node.DataDir, walFileName),
	})
	if err != nil {
		return nil, fmt.Errorf("open storage: %w", err)
	}
	store := enginestorage.NewAdapter(rawStore, cfg.Node.NodeID)

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
		}
	}
	if localHTTPAddr == "" {
		return nil, fmt.Errorf("node %s not found in cfg.Cluster.Members (needed to know its own internal-RPC listen address)", cfg.Node.NodeID)
	}

	// 5. Transport port (infra/transport/iouring implements engine/transport.Transport).
	bytePool := pool.NewDefaultArrayPool[byte]()
	tc := iouring.NewClient(rt, r, bytePool, nil)
	adapter := iouring.NewTransportAdapter(tc, r)
	for _, m := range cfg.Cluster.Members {
		if m.ID == cfg.Node.NodeID {
			continue
		}
		_ = adapter.Dial(m.ID, m.HTTPAddr)
	}

	sHandler := &appServerHandler{}
	ts := iouring.NewServer(rt, bytePool, sHandler)
	sHandler.server = ts

	if err := ts.Listen(localHTTPAddr); err != nil {
		return nil, fmt.Errorf("listen on %s: %w", localHTTPAddr, err)
	}

	r.SetEventHandler(func(ev reactor.Event) {
		if adapter.HandleCompletion(ev) {
			return
		}
		if ts.HandleCompletion(ev) {
			return
		}
		rawStore.HandleCompletion(ev)
	})

	// 6. Coordinator, composed over the storage/transport ports plus the
	// hash ring, membership view, and reactor.
	coord := coordinator.NewCoordinator(cfg.Node.NodeID, ring, store, adapter, mm, r, cfg.Quorum())

	// 7. Service-API implementations over the coordinator and ports.
	clientAPI := api.NewClientAPI(coord)
	adminAPI := api.NewAdminAPI(store, mm, cfg.Node.NodeID, version, time.Now())
	internalAPI := api.NewInternalAPI(store, mm, coord)

	// 8. Gateway, mounted over the coordinator.
	gw := gatewayhttp.New(coord)

	return &Server{
		cfg:           cfg,
		runtime:       rt,
		reactor:       r,
		store:         store,
		transport:     adapter,
		iouringClient: tc,
		iouringServer: ts,
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
	if s.iouringServer != nil {
		_ = s.iouringServer.Close()
	}
	if s.transport != nil {
		_ = s.transport.Close()
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
