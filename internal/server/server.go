package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/reflection"

	pb "GoQuorum/api"
	internalpb "GoQuorum/api/cluster"
	"GoQuorum/internal/cluster"
	"GoQuorum/internal/common"
	"GoQuorum/internal/config"
	"GoQuorum/internal/security"
	"GoQuorum/internal/storage"
)

// Server wraps gRPC and HTTP servers
type Server struct {
	grpcServer   *grpc.Server
	httpServer   *http.Server
	grpcListener net.Listener
	httpListener net.Listener

	// Dependencies
	coordinator *cluster.Coordinator
	storage     *storage.Storage
	membership  *cluster.MembershipManager
	ring        *cluster.HashRing

	// API handlers
	clientAPI   *ClientAPI
	adminAPI    *AdminAPI
	internalAPI *InternalAPI

	// Optional gossip instance; attached before Start() via SetGossip.
	gossip *cluster.Gossip

	// Configuration
	config    config.ServerConfig
	dataDir   string // storage data directory for disk health checks
	nodeID    string
	version   string
	startTime time.Time

	// Lifecycle
	wg     sync.WaitGroup
	stopCh chan struct{}
}

// DefaultServerConfig returns default server configuration
func DefaultServerConfig() config.ServerConfig {
	return config.ServerConfig{
		GRPCAddr: ":7070",
		HTTPAddr: ":8080",
	}
}

// NewServer creates a new server instance
func NewServer(
	coordinator *cluster.Coordinator,
	store *storage.Storage,
	membership *cluster.MembershipManager,
	ring *cluster.HashRing,
	cfg config.ServerConfig,
	dataDir string,
	nodeID string,
	version string,
) *Server {
	return &Server{
		coordinator: coordinator,
		storage:     store,
		membership:  membership,
		ring:        ring,
		config:      cfg,
		dataDir:     dataDir,
		nodeID:      nodeID,
		version:     version,
		startTime:   time.Now(),
		stopCh:      make(chan struct{}),
	}
}

// SetGossip attaches a Gossip instance; must be called before Start().
func (s *Server) SetGossip(g *cluster.Gossip) {
	s.gossip = g
}

// Start starts both gRPC and HTTP servers
func (s *Server) Start() error {
	// Build API handlers
	s.clientAPI = NewClientAPI(s.coordinator, s.storage)
	s.adminAPI = NewAdminAPI(s.storage, s.membership, s.nodeID, s.version, s.startTime)
	s.internalAPI = NewInternalAPI(s.storage, s.membership)

	// Wire Merkle root function after coordinator is started
	if s.coordinator != nil {
		s.internalAPI.SetMerkleRootFn(s.coordinator.GetMerkleRoot)
	}

	// Wire gossip if provided
	if s.gossip != nil {
		s.internalAPI.SetGossip(s.gossip)
	}

	// Create gRPC server and register all services
	grpcOpts := []grpc.ServerOption{
		grpc.MaxRecvMsgSize(4 * 1024 * 1024),
		grpc.MaxSendMsgSize(100 * 1024 * 1024),
	}
	if s.config.TLS.Enabled {
		tlsCfg, err := security.LoadServerTLSConfig(s.config.TLS)
		if err != nil {
			return fmt.Errorf("load server TLS config: %w", err)
		}
		grpcOpts = append(grpcOpts, grpc.Creds(credentials.NewTLS(tlsCfg)))
	}
	if s.config.RateLimit.GlobalRPS > 0 {
		rl := NewRateLimiter(s.config.RateLimit)
		grpcOpts = append(grpcOpts, grpc.UnaryInterceptor(rl.UnaryInterceptor()))
	}
	s.grpcServer = grpc.NewServer(grpcOpts...)
	pb.RegisterGoQuorumServer(s.grpcServer, &goQuorumGRPCServer{api: s.clientAPI})
	pb.RegisterGoQuorumAdminServer(s.grpcServer, &goQuorumAdminGRPCServer{api: s.adminAPI})
	internalpb.RegisterGoQuorumInternalServer(s.grpcServer, &goQuorumInternalGRPCServer{api: s.internalAPI})
	reflection.Register(s.grpcServer)

	var err error
	s.grpcListener, err = net.Listen("tcp", s.config.GRPCAddr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", s.config.GRPCAddr, err)
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		fmt.Printf("gRPC server listening on %s\n", s.config.GRPCAddr)
		if err := s.grpcServer.Serve(s.grpcListener); err != nil {
			fmt.Printf("gRPC server error: %v\n", err)
		}
	}()

	// Start HTTP server
	if err := s.startHTTP(); err != nil {
		return fmt.Errorf("failed to start HTTP server: %w", err)
	}

	return nil
}

// startHTTP wires all HTTP routes and starts the listener.
func (s *Server) startHTTP() error {
	// grpc-gateway mux handles all /v1/* routes, translating HTTP/JSON ↔ proto
	gwMux := runtime.NewServeMux()
	ctx := context.Background()
	if err := pb.RegisterGoQuorumHandlerServer(ctx, gwMux, &goQuorumGRPCServer{api: s.clientAPI}); err != nil {
		return fmt.Errorf("register gateway client handler: %w", err)
	}
	if err := pb.RegisterGoQuorumAdminHandlerServer(ctx, gwMux, &goQuorumAdminGRPCServer{api: s.adminAPI}); err != nil {
		return fmt.Errorf("register gateway admin handler: %w", err)
	}

	mux := http.NewServeMux()

	// ── Health ──────────────────────────────────────────────────────────────
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/health/live", s.handleLive)
	mux.HandleFunc("/health/ready", s.handleReady)

	// ── Metrics ─────────────────────────────────────────────────────────────
	mux.Handle("/metrics", promhttp.Handler())

	// ── Debug ───────────────────────────────────────────────────────────────
	mux.HandleFunc("/debug/cluster", s.handleDebugCluster)
	mux.HandleFunc("/debug/ring", s.handleDebugRing)

	// ── Internal RPC (node-to-node) ──────────────────────────────────────────
	mux.HandleFunc("/internal/replicate", s.handleInternalReplicate)
	mux.HandleFunc("/internal/read", s.handleInternalRead)
	mux.HandleFunc("/internal/heartbeat", s.handleInternalHeartbeat)
	mux.HandleFunc("/internal/merkle-root", s.handleInternalMerkleRoot)
	mux.HandleFunc("/internal/notify-leaving", s.handleInternalNotifyLeaving)
	if s.internalAPI.gossip != nil {
		mux.HandleFunc("/internal/gossip/exchange", s.internalAPI.handleGossipExchange)
	}

	// ── Ring rebalancing ─────────────────────────────────────────────────────
	mux.HandleFunc("/admin/ring/nodes", s.handleAdminRingNodes)

	// ── Backup / Restore ─────────────────────────────────────────────────────
	mux.HandleFunc("/admin/backup", s.handleAdminBackup)
	mux.HandleFunc("/admin/restore", s.handleAdminRestore)

	// ── Client + Admin API via grpc-gateway ──────────────────────────────────
	mux.Handle("/v1/", gwMux)

	s.httpServer = &http.Server{
		Addr:         s.config.HTTPAddr,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	var err error
	s.httpListener, err = net.Listen("tcp", s.config.HTTPAddr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", s.config.HTTPAddr, err)
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		fmt.Printf("HTTP server listening on %s\n", s.config.HTTPAddr)
		if err := s.httpServer.Serve(s.httpListener); err != nil && err != http.ErrServerClosed {
			fmt.Printf("HTTP server error: %v\n", err)
		}
	}()

	return nil
}

// ── Internal API handlers ────────────────────────────────────────────────────

func (s *Server) handleInternalReplicate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req ReplicateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	resp, err := s.internalAPI.Replicate(r.Context(), &req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, resp)
}

func (s *Server) handleInternalRead(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req InternalReadReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	resp, err := s.internalAPI.Read(r.Context(), &req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, resp)
}

func (s *Server) handleInternalHeartbeat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req HeartbeatReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	resp, err := s.internalAPI.Heartbeat(r.Context(), &req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, resp)
}

func (s *Server) handleInternalMerkleRoot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req GetMerkleRootReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	resp, err := s.internalAPI.GetMerkleRoot(r.Context(), &req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, resp)
}

func (s *Server) handleInternalNotifyLeaving(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req NotifyLeavingReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	resp, err := s.internalAPI.NotifyLeaving(r.Context(), &req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, resp)
}

func (s *Server) handleAdminBackup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		DestDir string `json:"dest_dir"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	archivePath, err := s.adminAPI.BackupDB(req.DestDir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"archive": archivePath})
}

func (s *Server) handleAdminRestore(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ArchiveFile string `json:"archive_file"`
		DestDataDir string `json:"dest_data_dir"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.adminAPI.RestoreDB(req.ArchiveFile, req.DestDataDir); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

// handleAdminRingNodes handles ring membership changes.
//
//	POST   /admin/ring/nodes        — add a node and trigger rebalance
//	DELETE /admin/ring/nodes/{id}   — drain then remove a node
//	GET    /admin/ring/nodes        — list current ring members
func (s *Server) handleAdminRingNodes(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		type nodeInfo struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		}
		peers := s.membership.GetPeers()
		result := make([]nodeInfo, 0, len(peers)+1)
		result = append(result, nodeInfo{
			ID:     string(s.membership.LocalNodeID()),
			Status: s.membership.GetLocalStatus().String(),
		})
		for _, p := range peers {
			result = append(result, nodeInfo{
				ID:     string(p.ID),
				Status: p.Status.String(),
			})
		}
		writeJSON(w, result)

	case http.MethodPost:
		var req struct {
			ID       string `json:"id"`
			GRPCAddr string `json:"grpc_addr"`
			HTTPAddr string `json:"http_addr"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.ID == "" || req.GRPCAddr == "" || req.HTTPAddr == "" {
			http.Error(w, "id, grpc_addr, and http_addr are required", http.StatusBadRequest)
			return
		}
		node := &common.Node{
			ID:               common.NodeID(req.ID),
			Addr:             req.GRPCAddr,
			State:            common.NodeStateActive,
			VirtualNodeCount: s.ring.VNodeCount(),
		}
		if err := s.coordinator.JoinNode(node, req.HTTPAddr); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]string{"status": "ok", "message": req.ID + " joined ring"})

	case http.MethodDelete:
		// Extract node ID from path: /admin/ring/nodes/{id}
		id := r.URL.Path[len("/admin/ring/nodes/"):]
		if id == "" {
			http.Error(w, "node id required in path", http.StatusBadRequest)
			return
		}
		if err := s.coordinator.LeaveNode(common.NodeID(id)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]string{"status": "ok", "message": id + " removed from ring"})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		fmt.Printf("writeJSON encode error: %v\n", err)
	}
}

// Stop gracefully stops the server
func (s *Server) Stop() {
	close(s.stopCh)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if s.httpServer != nil {
		if err := s.httpServer.Shutdown(ctx); err != nil {
			fmt.Printf("HTTP server shutdown error: %v\n", err)
		}
	}

	if s.grpcServer != nil {
		s.grpcServer.GracefulStop()
	}

	s.wg.Wait()
	fmt.Println("Server stopped")
}

// GRPCServer returns the underlying gRPC server
func (s *Server) GRPCServer() *grpc.Server {
	return s.grpcServer
}
