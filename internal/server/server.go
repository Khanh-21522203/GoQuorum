package server

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/reflection"

	"GoQuorum/internal/cluster"
	"GoQuorum/internal/config"
	"GoQuorum/internal/storage"
)

// Server wraps gRPC and HTTP gateway servers
type Server struct {
	grpcServer   *grpc.Server
	httpServer   *http.Server
	grpcListener net.Listener
	httpListener net.Listener

	// Dependencies
	coordinator *cluster.Coordinator
	storage     *storage.Storage
	membership  *cluster.MembershipManager

	// Configuration
	config    config.ServerConfig
	nodeID    string
	version   string
	startTime time.Time

	// Lifecycle
	wg     sync.WaitGroup
	stopCh chan struct{}
}

// ServerConfig holds server configuration
type ServerConfig struct {
	GRPCAddr        string
	HTTPAddr        string
	MaxRecvMsgSize  int
	MaxSendMsgSize  int
	EnableReflection bool
}

// DefaultServerConfig returns default server configuration
func DefaultServerConfig() ServerConfig {
	return ServerConfig{
		GRPCAddr:        ":7070",
		HTTPAddr:        ":8080",
		MaxRecvMsgSize:  4 * 1024 * 1024,   // 4MB
		MaxSendMsgSize:  100 * 1024 * 1024, // 100MB
		EnableReflection: true,
	}
}

// NewServer creates a new server instance
func NewServer(
	coordinator *cluster.Coordinator,
	store *storage.Storage,
	membership *cluster.MembershipManager,
	cfg config.ServerConfig,
	nodeID string,
	version string,
) *Server {
	return &Server{
		coordinator: coordinator,
		storage:     store,
		membership:  membership,
		config:      cfg,
		nodeID:      nodeID,
		version:     version,
		startTime:   time.Now(),
		stopCh:      make(chan struct{}),
	}
}

// Start starts both gRPC and HTTP servers
func (s *Server) Start() error {
	// Create gRPC server with options
	grpcOpts := []grpc.ServerOption{
		grpc.MaxRecvMsgSize(4 * 1024 * 1024),
		grpc.MaxSendMsgSize(100 * 1024 * 1024),
	}
	s.grpcServer = grpc.NewServer(grpcOpts...)

	// Register services
	s.registerServices()

	// Enable reflection for debugging
	reflection.Register(s.grpcServer)

	// Start gRPC listener
	var err error
	s.grpcListener, err = net.Listen("tcp", s.config.GRPCAddr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", s.config.GRPCAddr, err)
	}

	// Start gRPC server
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		fmt.Printf("gRPC server listening on %s\n", s.config.GRPCAddr)
		if err := s.grpcServer.Serve(s.grpcListener); err != nil {
			fmt.Printf("gRPC server error: %v\n", err)
		}
	}()

	// Start HTTP gateway
	if err := s.startHTTPGateway(); err != nil {
		return fmt.Errorf("failed to start HTTP gateway: %w", err)
	}

	return nil
}

// startHTTPGateway starts the HTTP/JSON gateway
func (s *Server) startHTTPGateway() error {
	ctx := context.Background()

	// Create gRPC-Gateway mux
	gwMux := runtime.NewServeMux(
		runtime.WithMarshalerOption(runtime.MIMEWildcard, &runtime.JSONPb{}),
	)

	// Connect to local gRPC server
	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	}

	// Register gateway handlers (will be implemented when proto is generated)
	// For now, we'll add health and metrics endpoints directly

	// Create HTTP mux
	mux := http.NewServeMux()

	// Health endpoints
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/health/live", s.handleLive)
	mux.HandleFunc("/health/ready", s.handleReady)

	// Metrics endpoint
	mux.Handle("/metrics", promhttp.Handler())

	// Debug endpoints
	mux.HandleFunc("/debug/cluster", s.handleDebugCluster)
	mux.HandleFunc("/debug/ring", s.handleDebugRing)

	// gRPC-Gateway (API endpoints)
	mux.Handle("/v1/", gwMux)

	// Create HTTP server
	s.httpServer = &http.Server{
		Addr:         s.config.HTTPAddr,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Start HTTP listener
	var err error
	s.httpListener, err = net.Listen("tcp", s.config.HTTPAddr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", s.config.HTTPAddr, err)
	}

	// Start HTTP server
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		fmt.Printf("HTTP server listening on %s\n", s.config.HTTPAddr)
		if err := s.httpServer.Serve(s.httpListener); err != nil && err != http.ErrServerClosed {
			fmt.Printf("HTTP server error: %v\n", err)
		}
	}()

	_ = ctx
	_ = opts

	return nil
}

// registerServices registers gRPC services
func (s *Server) registerServices() {
	// Register client API service
	clientAPI := NewClientAPI(s.coordinator, s.storage)
	_ = clientAPI // Will register when proto is generated

	// Register admin API service
	adminAPI := NewAdminAPI(s.storage, s.membership, s.nodeID, s.version, s.startTime)
	_ = adminAPI // Will register when proto is generated

	// Register internal API service
	internalAPI := NewInternalAPI(s.storage, s.membership)
	_ = internalAPI // Will register when proto is generated
}

// Stop gracefully stops the server
func (s *Server) Stop() {
	close(s.stopCh)

	// Graceful shutdown with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Stop HTTP server
	if s.httpServer != nil {
		if err := s.httpServer.Shutdown(ctx); err != nil {
			fmt.Printf("HTTP server shutdown error: %v\n", err)
		}
	}

	// Stop gRPC server
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
