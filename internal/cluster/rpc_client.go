package cluster

import (
	"context"
	"fmt"
	"sync"
	"time"

	"GoQuorum/internal/common"
	"GoQuorum/internal/config"
	"GoQuorum/internal/storage"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// RPCClient defines the interface for node-to-node communication
type RPCClient interface {
	// RemotePut writes to a remote node
	RemotePut(ctx context.Context, nodeID common.NodeID, key []byte, siblings *storage.SiblingSet) error

	// RemoteGet reads from a remote node
	RemoteGet(ctx context.Context, nodeID common.NodeID, key []byte) (*storage.SiblingSet, error)

	// SendHeartbeat sends heartbeat to a remote node
	SendHeartbeat(ctx context.Context, nodeID common.NodeID) error

	// GetMerkleRoot gets merkle root from a remote node
	GetMerkleRoot(ctx context.Context, nodeID common.NodeID) ([]byte, error)

	// Close closes all connections
	Close() error
}

// GRPCClient implements RPCClient using gRPC
type GRPCClient struct {
	config     config.ConnectionConfig
	membership *MembershipManager

	// Connection pool
	mu    sync.RWMutex
	conns map[common.NodeID]*grpc.ClientConn
}

// NewGRPCClient creates a new gRPC client
func NewGRPCClient(cfg config.ConnectionConfig, membership *MembershipManager) *GRPCClient {
	return &GRPCClient{
		config:     cfg,
		membership: membership,
		conns:      make(map[common.NodeID]*grpc.ClientConn),
	}
}

// getConn gets or creates a connection to a node
func (c *GRPCClient) getConn(nodeID common.NodeID) (*grpc.ClientConn, error) {
	c.mu.RLock()
	conn, exists := c.conns[nodeID]
	c.mu.RUnlock()

	if exists && conn != nil {
		return conn, nil
	}

	// Get address from membership
	addr := c.membership.GetAddress(nodeID)
	if addr == "" {
		return nil, fmt.Errorf("unknown node: %s", nodeID)
	}

	// Create new connection
	c.mu.Lock()
	defer c.mu.Unlock()

	// Double-check
	if conn, exists := c.conns[nodeID]; exists && conn != nil {
		return conn, nil
	}

	// Dial options
	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), c.config.DialTimeout)
	defer cancel()

	conn, err := grpc.DialContext(ctx, addr, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to %s (%s): %w", nodeID, addr, err)
	}

	c.conns[nodeID] = conn
	return conn, nil
}

// RemotePut writes to a remote node
func (c *GRPCClient) RemotePut(ctx context.Context, nodeID common.NodeID, key []byte, siblings *storage.SiblingSet) error {
	conn, err := c.getConn(nodeID)
	if err != nil {
		return err
	}

	// In production, would use generated gRPC client
	// For now, implement using raw connection
	_ = conn

	// Placeholder - will be implemented when proto is generated
	return nil
}

// RemoteGet reads from a remote node
func (c *GRPCClient) RemoteGet(ctx context.Context, nodeID common.NodeID, key []byte) (*storage.SiblingSet, error) {
	conn, err := c.getConn(nodeID)
	if err != nil {
		return nil, err
	}

	// In production, would use generated gRPC client
	_ = conn

	// Placeholder - will be implemented when proto is generated
	return nil, nil
}

// SendHeartbeat sends heartbeat to a remote node
func (c *GRPCClient) SendHeartbeat(ctx context.Context, nodeID common.NodeID) error {
	conn, err := c.getConn(nodeID)
	if err != nil {
		return err
	}

	// In production, would use generated gRPC client
	_ = conn

	// Placeholder - will be implemented when proto is generated
	return nil
}

// GetMerkleRoot gets merkle root from a remote node
func (c *GRPCClient) GetMerkleRoot(ctx context.Context, nodeID common.NodeID) ([]byte, error) {
	conn, err := c.getConn(nodeID)
	if err != nil {
		return nil, err
	}

	// In production, would use generated gRPC client
	_ = conn

	// Placeholder - will be implemented when proto is generated
	return []byte{}, nil
}

// Close closes all connections
func (c *GRPCClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	var errs []error
	for nodeID, conn := range c.conns {
		if conn != nil {
			if err := conn.Close(); err != nil {
				errs = append(errs, fmt.Errorf("failed to close connection to %s: %w", nodeID, err))
			}
		}
	}
	c.conns = make(map[common.NodeID]*grpc.ClientConn)

	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}

// ConnectionStats returns connection statistics
type ConnectionStats struct {
	ActiveConnections int
	TotalConnections  int64
	FailedConnections int64
}

// Stats returns connection statistics
func (c *GRPCClient) Stats() ConnectionStats {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return ConnectionStats{
		ActiveConnections: len(c.conns),
	}
}

// HealthCheck performs health check on all connections
func (c *GRPCClient) HealthCheck() map[common.NodeID]bool {
	c.mu.RLock()
	nodes := make([]common.NodeID, 0, len(c.conns))
	for nodeID := range c.conns {
		nodes = append(nodes, nodeID)
	}
	c.mu.RUnlock()

	results := make(map[common.NodeID]bool)
	for _, nodeID := range nodes {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		err := c.SendHeartbeat(ctx, nodeID)
		cancel()
		results[nodeID] = err == nil
	}

	return results
}
