package config

import (
	"GoQuorum/internal/common"
	"errors"
	"fmt"
	"net"
	"regexp"
	"time"
)

// ClusterConfig defines static cluster membership (Section 2)
type ClusterConfig struct {
	NodeID     common.NodeID  // Unique node identifier (Section 3.1)
	ListenAddr string         // gRPC listen address (Section 3.2)
	Members    []MemberConfig // All cluster members (Section 2.2)

	// Heartbeat configuration (Section 4.1)
	HeartbeatInterval time.Duration // Default: 1s
	HeartbeatTimeout  time.Duration // Default: 2s
	FailureThreshold  int           // Default: 5

	// Bootstrap configuration (Section 5)
	BootstrapTimeout time.Duration // Default: 60s
}

// MemberConfig defines a cluster member (Section 2.2)
type MemberConfig struct {
	ID   common.NodeID // Unique identifier
	Addr string        // <host>:<port> format
}

// DefaultClusterConfig returns default configuration
func DefaultClusterConfig(nodeID common.NodeID, listenAddr string) ClusterConfig {
	return ClusterConfig{
		NodeID:            nodeID,
		ListenAddr:        listenAddr,
		Members:           []MemberConfig{},
		HeartbeatInterval: 1 * time.Second,
		HeartbeatTimeout:  2 * time.Second,
		FailureThreshold:  5,
		BootstrapTimeout:  60 * time.Second,
	}
}

// Validate validates cluster configuration (Section 2.3 - Invariants)
func (c ClusterConfig) Validate() error {
	// Validate node ID (Section 3.1)
	if err := ValidateNodeID(c.NodeID); err != nil {
		return fmt.Errorf("invalid node_id: %w", err)
	}

	// Validate listen address (Section 3.2)
	if err := ValidateAddr(c.ListenAddr); err != nil {
		return fmt.Errorf("invalid listen_addr: %w", err)
	}

	// Validate members list
	if len(c.Members) == 0 {
		return errors.New("members list cannot be empty")
	}

	// INV-M4: Member list includes self (Section 2.3)
	selfFound := false
	for _, member := range c.Members {
		if member.ID == c.NodeID {
			selfFound = true
			break
		}
	}
	if !selfFound {
		return fmt.Errorf("node_id %s must be in members list", c.NodeID)
	}

	// INV-M2: Node IDs must be unique (Section 2.3)
	seen := make(map[common.NodeID]bool)
	for _, member := range c.Members {
		if err := ValidateNodeID(member.ID); err != nil {
			return fmt.Errorf("invalid member id %s: %w", member.ID, err)
		}

		if seen[member.ID] {
			return fmt.Errorf("duplicate node_id: %s", member.ID)
		}
		seen[member.ID] = true

		if err := ValidateAddr(member.Addr); err != nil {
			return fmt.Errorf("invalid member addr %s: %w", member.Addr, err)
		}
	}

	// Validate heartbeat config
	if c.HeartbeatInterval <= 0 {
		return errors.New("heartbeat_interval must be > 0")
	}
	if c.HeartbeatTimeout <= 0 {
		return errors.New("heartbeat_timeout must be > 0")
	}
	if c.FailureThreshold <= 0 {
		return errors.New("failure_threshold must be > 0")
	}
	if c.BootstrapTimeout <= 0 {
		return errors.New("bootstrap_timeout must be > 0")
	}

	return nil
}

// GetPeers returns all members except self
func (c ClusterConfig) GetPeers() []MemberConfig {
	peers := make([]MemberConfig, 0, len(c.Members)-1)
	for _, member := range c.Members {
		if member.ID != c.NodeID {
			peers = append(peers, member)
		}
	}
	return peers
}

// GetMemberByID returns member config by ID
func (c ClusterConfig) GetMemberByID(nodeID common.NodeID) (MemberConfig, bool) {
	for _, member := range c.Members {
		if member.ID == nodeID {
			return member, true
		}
	}
	return MemberConfig{}, false
}

// QuorumSize returns minimum nodes for majority (Section 5.1)
func (c ClusterConfig) QuorumSize() int {
	return (len(c.Members) / 2) + 1
}

// ValidateNodeID validates node identifier format (Section 3.1)
// Format: alphanumeric, hyphen, underscore (1-64 chars)
func ValidateNodeID(nodeID common.NodeID) error {
	if nodeID == "" {
		return errors.New("node_id cannot be empty")
	}

	if len(nodeID) > 64 {
		return fmt.Errorf("node_id too long: %d chars (max 64)", len(nodeID))
	}

	// Pattern: alphanumeric, hyphen, underscore only
	validPattern := regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
	if !validPattern.MatchString(string(nodeID)) {
		return fmt.Errorf("node_id contains invalid characters (allowed: a-z, A-Z, 0-9, _, -)")
	}

	return nil
}

// ValidateAddr validates address format (Section 3.2)
// Format: <host>:<port>
func ValidateAddr(addr string) error {
	if addr == "" {
		return errors.New("address cannot be empty")
	}

	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid address format (expected host:port): %w", err)
	}

	if host == "" {
		return errors.New("host cannot be empty")
	}

	if port == "" {
		return errors.New("port cannot be empty")
	}

	// Validate port range
	portNum, err := net.LookupPort("tcp", port)
	if err != nil {
		return fmt.Errorf("invalid port: %w", err)
	}

	if portNum < 1024 || portNum > 65535 {
		return fmt.Errorf("port out of range: %d (must be 1024-65535)", portNum)
	}

	return nil
}
