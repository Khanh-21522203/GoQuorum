package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"GoQuorum/internal/cluster"
	"GoQuorum/internal/common"
	"GoQuorum/internal/config"
	"GoQuorum/internal/server"
	"GoQuorum/internal/storage"
)

const Version = "0.1.0-mvp"

func main() {
	// Parse command line flags
	configPath := flag.String("config", "config.yaml", "Path to configuration file")
	flag.Parse()

	fmt.Printf("GoQuorum %s starting...\n", Version)

	// Load configuration
	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		fmt.Printf("Failed to load config: %v\n", err)
		os.Exit(1)
	}

	cfg.PrintSummary()

	// Initialize storage
	storageOpts := storage.StorageOptions{
		DataDir:              cfg.Node.DataDir,
		NodeID:               cfg.Node.NodeID,
		SyncWrites:           cfg.Storage.SyncWrites,
		BlockCacheSize:       cfg.Storage.BlockCacheSize(),
		MemTableSize:         cfg.Storage.MemTableSize(),
		MaxOpenFiles:         cfg.Storage.MaxOpenFiles,
		MaxKeySize:           cfg.Storage.MaxKeySize(),
		MaxValueSize:         cfg.Storage.MaxValueSize(),
		MaxSiblings:          cfg.Storage.MaxSiblings,
		TombstoneGCEnabled:   cfg.Storage.TombstoneGCEnabled,
		TombstoneTTL:         cfg.Storage.TombstoneTTL(),
		TombstoneGCInterval:  cfg.Storage.TombstoneGCInterval,
		VClockPruneThreshold: cfg.Storage.VClockPruneThreshold(),
		VClockMaxEntries:     cfg.Storage.VClockMaxEntries,
	}

	store, err := storage.NewStorage(storageOpts)
	if err != nil {
		fmt.Printf("Failed to open storage: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()
	fmt.Println("Storage initialized")

	// Initialize membership manager
	membership := cluster.NewMembershipManager(cfg.Cluster, Version)
	fmt.Println("Membership manager initialized")

	// Initialize hash ring
	ring := cluster.NewHashRing(256) // 256 virtual nodes per physical node
	for _, member := range cfg.Cluster.Members {
		node := &common.Node{
			ID:               member.ID,
			Addr:             member.Addr,
			State:            common.NodeStateActive,
			VirtualNodeCount: 256,
		}
		if err := ring.AddNode(node); err != nil {
			fmt.Printf("Failed to add node %s to ring: %v\n", member.ID, err)
			os.Exit(1)
		}
	}
	fmt.Printf("Hash ring initialized with %d nodes\n", len(cfg.Cluster.Members))

	// Initialize RPC client
	rpcClient := cluster.NewGRPCClient(cfg.Connection, membership)
	fmt.Println("RPC client initialized")

	// Initialize coordinator
	coordinator := cluster.NewCoordinator(
		cfg.Node.NodeID,
		ring,
		store,
		rpcClient,
		membership,
		cfg.Quorum,
		cfg.ReadRepair,
		cfg.AntiEntropy,
	)
	fmt.Println("Coordinator initialized")

	// Initialize failure detector
	fdConfig := config.DefaultFailureDetectorConfig()
	failureDetector := cluster.NewFailureDetector(fdConfig, membership, rpcClient)
	fmt.Println("Failure detector initialized")

	// Initialize server
	srv := server.NewServer(
		coordinator,
		store,
		membership,
		cfg.Server,
		string(cfg.Node.NodeID),
		Version,
	)

	// Start server
	if err := srv.Start(); err != nil {
		fmt.Printf("Failed to start server: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Server started")

	// Start coordinator (anti-entropy)
	if err := coordinator.Start(); err != nil {
		fmt.Printf("Failed to start coordinator: %v\n", err)
		os.Exit(1)
	}

	// Bootstrap cluster
	if err := cluster.Bootstrap(cfg.Cluster, membership, failureDetector); err != nil {
		fmt.Printf("Bootstrap failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Cluster bootstrap complete")

	// Initialize graceful shutdown
	shutdown := cluster.NewGracefulShutdown(store, nil, failureDetector)

	// Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	fmt.Println("GoQuorum is ready to serve requests")
	fmt.Printf("  gRPC: %s\n", cfg.Server.GRPCAddr)
	fmt.Printf("  HTTP: %s\n", cfg.Server.HTTPAddr)

	<-sigCh

	fmt.Println("\nReceived shutdown signal, shutting down gracefully...")

	// Stop server
	srv.Stop()

	// Stop coordinator
	coordinator.Stop()

	// Graceful shutdown
	shutdown.Shutdown(context.Background())

	fmt.Println("GoQuorum shutdown complete")
}
