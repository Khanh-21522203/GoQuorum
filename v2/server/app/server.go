package app

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"time"

	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/engine/coordinator"
	"goquorum.io/v2/engine/hashring"
	"goquorum.io/v2/engine/membership"
	"goquorum.io/v2/engine/reactor"
	enginestorage "goquorum.io/v2/engine/storage"
	enginetransport "goquorum.io/v2/engine/transport"
	"goquorum.io/v2/infra/affinity"
	"goquorum.io/v2/infra/config"
	"goquorum.io/v2/infra/ioruntime"
	"goquorum.io/v2/infra/storage/journal"
	"goquorum.io/v2/infra/transport/iouring"
	"goquorum.io/v2/server/api"

	gatewayhttp "goquorum.io/v2/gateway/http"
)

// version is reported to AdminAPI.Health and the membership manager.
//
// (v1: cmd/quorum/main.go Version)
const version = "0.0.0-v2-scaffold"

// defaultVNodeCount is the number of virtual nodes per physical node on
// the hash ring.
//
// v1 hardcoded this same value directly in cmd/quorum/main.go rather than
// exposing it as a config field; v2 keeps the hardcode for now (see
// INTERFACES.md's engine/hashring.NewHashRing: "default 256 if <= 0") but
// a real deployment likely wants this promoted to infra/config.
//
// (v1: cmd/quorum/main.go main, "256 virtual nodes per physical node")
const defaultVNodeCount = 256

// ioURingQueueDepth is the submission queue depth for the process-wide
// io_uring instance backing both storage and transport.
const ioURingQueueDepth = 256

// walFileName is the journal storage engine's on-disk log file, created
// under cfg.Node.DataDir.
const walFileName = "data.log"

// Server is GoQuorum v2's composition root. It owns the full dependency
// graph and the single-threaded engine reactor: one *ioruntime.Runtime and
// one *reactor.Reactor drive the journal-backed storage.Storage port and
// the io_uring-backed transport.Transport port, both real (not typed
// stubs), so every engine subsystem built on top of them (coordinator,
// anti-entropy) runs on exactly one goroutine with no locking. The
// Pebble/HTTP-JSON adapters (infra/storage/pebble, infra/transport/httprpc)
// remain in the tree as untouched, non-reactor-based fallbacks but are not
// wired in here — this composition root deliberately always uses the real
// io_uring-driven path.
//
// The client-facing HTTP gateway keeps its own goroutine-per-request
// net/http model (out of scope for the single-thread rework) and reaches
// the reactor-confined coordinator through reactor.PostFunc, the one place
// a blocking wait is deliberately reintroduced, at the outer boundary only.
//
// (v1: internal/server/server.go Server, cmd/quorum/main.go)
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

// New builds the full GoQuorum v2 dependency graph from cfg: it opens an
// io_uring instance and the reactor it drives, the journal-backed
// storage.Storage port and the io_uring-backed transport.Transport port on
// top of it, builds the hash ring and membership view, and composes the
// coordinator over the ports and engine domain objects. Finally it builds
// the server/api service implementations and mounts gateway/http over the
// coordinator.
//
// Boot order mirrors v1's cmd/quorum/main.go: storage, membership, hash
// ring (populated from cfg.Cluster.Members), transport, coordinator,
// server/gateway. v1 additionally wired a failure detector, gossip, hinted
// handoff, and a TTL sweeper around the coordinator; those subsystems are
// out of this package's spec surface (see engine/coordinator's doc
// comment) and are left for a later phase.
//
// (v1: cmd/quorum/main.go main, internal/server/server.go NewServer)
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

	// 4. Hash ring, populated from the statically-configured cluster
	// members (v1: cmd/quorum/main.go looped cfg.Cluster.Members the
	// same way).
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

	// 5. Transport port (infra/transport/iouring implements
	// engine/transport.Transport), listening for peers and eagerly dialing
	// every other configured member. A Dial failure here is not fatal:
	// Client redials lazily the next time that peer is addressed, so a
	// peer that happens to be down at boot does not block startup.
	tc := iouring.NewClient(rt, r)
	for _, m := range cfg.Cluster.Members {
		if m.ID == cfg.Node.NodeID {
			continue
		}
		_ = tc.Dial(m.ID, m.HTTPAddr)
	}

	ts := iouring.NewServer(rt)
	ts.OnMessage = func(connFD int, hdr iouring.FrameHeader, body []byte) {
		switch hdr.MessageID {
		case iouring.MsgHeartbeatRequest:
			resp := iouring.HeartbeatResponse{Status: iouring.StatusOK}
			respBody, _ := resp.Marshal()
			_ = ts.Send(connFD, iouring.MsgHeartbeatResponse, hdr.CorrelationID, respBody)
		}
	}
	if err := ts.Listen(localHTTPAddr); err != nil {
		return nil, fmt.Errorf("listen on %s: %w", localHTTPAddr, err)
	}

	r.SetEventHandler(func(ev reactor.Event) {
		if tc.HandleCompletion(ev) {
			return
		}
		if ts.HandleCompletion(ev) {
			return
		}
		rawStore.HandleCompletion(ev)
	})

	// 6. Coordinator, composed over the storage/transport ports plus the
	// hash ring, membership view, and reactor.
	coord := coordinator.NewCoordinator(cfg.Node.NodeID, ring, store, tc, mm, r, cfg.Quorum())

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
		transport:     tc,
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
// server hosting the gateway. It does not block: the reactor driving
// storage/transport/coordinator only actually processes work once Run is
// called.
//
// TODO(v2): import google.golang.org/grpc; construct a *grpc.Server here,
// register ClientAPI/AdminAPI/InternalAPI-backed service implementations
// (v1's goQuorumGRPCServer/goQuorumAdminGRPCServer/
// goQuorumInternalGRPCServer), and listen on cfg.Server.GRPCAddr alongside
// the HTTP listener below (v1: internal/server/server.go Server.Start).
//
// (v1: internal/server/server.go Server.Start, cmd/quorum/main.go)
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
		// TODO(v2): route this error through infra/observability
		// logging once it exists, rather than discarding it (v1:
		// internal/server/server.go startHTTP printed it to stdout).
		_ = s.httpServer.Serve(ln)
	}()

	return nil
}

// Run blocks the calling goroutine, which becomes the single thread of
// execution for storage, transport, and every reactor-driven engine
// subsystem, until RequestStop is called. Call Run in its own goroutine
// (or as main's final call) after Start.
//
// If cfg.Node.ReactorCPUCore is set, Run first pins this goroutine's OS
// thread to that CPU core (see infra/affinity) before entering the
// reactor loop, isolating it from scheduling jitter caused by GC or other
// goroutines (e.g. the HTTP gateway) sharing the same core.
func (s *Server) Run() error {
	if core := s.cfg.Node.ReactorCPUCore; core != nil {
		if err := affinity.LockToCore(*core); err != nil {
			return fmt.Errorf("pin reactor to CPU core %d: %w", *core, err)
		}
	}
	return s.reactor.Run()
}

// RequestStop asks Run to return once pending reactor work drains, and
// begins a graceful HTTP shutdown. Safe to call from any goroutine,
// including a signal handler — this is the intended way to stop a running
// Server (Stop, below, only releases resources and must not be called
// until Run has actually returned).
func (s *Server) RequestStop() {
	if s.httpServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = s.httpServer.Shutdown(ctx)
	}
	s.reactor.RequestStop()
}

// Stop releases every resource this Server holds: the coordinator's
// background subsystems, the transport's connections, the storage engine,
// and the io_uring instance underlying both. Call it only after Run has
// returned — closing the io_uring instance while Run is still polling it
// would be a use-after-close.
//
// (v1: internal/server/server.go Server.Stop, cmd/quorum/main.go deferred
// store.Close/gossip.Stop/ttlSweeper.Stop around the same shutdown point.)
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

// membershipConfig converts the loaded cluster configuration into
// engine/membership.Config. infra/config has no ready-made Membership()
// conversion method (unlike Quorum/ReadRepair/AntiEntropy/
// FailureDetector/Timeout), so the composition root performs it directly.
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
