package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"GoQuorum/internal/common"
	"GoQuorum/internal/config"
	"GoQuorum/internal/security"
	"GoQuorum/internal/storage"
	"GoQuorum/internal/vclock"
)

// RPCClient defines the interface for node-to-node communication
type RPCClient interface {
	// RemotePut writes to a remote node
	RemotePut(ctx context.Context, nodeID common.NodeID, key []byte, siblings *storage.SiblingSet) error

	// RemoteGet reads from a remote node
	RemoteGet(ctx context.Context, nodeID common.NodeID, key []byte) (*storage.SiblingSet, error)

	// SendHeartbeat sends heartbeat to a remote node
	SendHeartbeat(ctx context.Context, nodeID common.NodeID) error

	// Heartbeat sends heartbeat to a remote node (alias for SendHeartbeat)
	Heartbeat(ctx context.Context, nodeID common.NodeID) error

	// GetMerkleRoot gets merkle root from a remote node
	GetMerkleRoot(ctx context.Context, nodeID common.NodeID) ([]byte, error)

	// NotifyLeaving sends a graceful-leave notification to a peer
	NotifyLeaving(ctx context.Context, nodeID common.NodeID) error

	// Close closes all connections
	Close() error
}

// GRPCClient implements RPCClient using HTTP JSON calls to the peer's internal API.
// Despite the name (kept for backward compatibility), transport is HTTP/JSON.
type GRPCClient struct {
	config     config.ConnectionConfig
	membership *MembershipManager
	httpClient *http.Client
	localID    common.NodeID
}

// NewGRPCClient creates a new HTTP-JSON-based inter-node client
func NewGRPCClient(cfg config.ConnectionConfig, membership *MembershipManager) *GRPCClient {
	transport := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     cfg.IdleTimeout,
	}
	if cfg.TLS.Enabled {
		tlsCfg, err := security.LoadClientTLSConfig(cfg.TLS)
		if err != nil {
			// Log and fall back to plain HTTP; callers will see TLS errors if certs are needed.
			fmt.Printf("WARNING: failed to load client TLS config: %v; proceeding without TLS\n", err)
		} else {
			transport.TLSClientConfig = tlsCfg
		}
	}
	return &GRPCClient{
		config:     cfg,
		membership: membership,
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   cfg.DialTimeout + 10*time.Second,
		},
		localID: membership.LocalNodeID(),
	}
}

// httpAddr returns the HTTP(S) base URL for a peer.
func (c *GRPCClient) httpAddr(nodeID common.NodeID) (string, error) {
	addr := c.membership.GetHTTPAddress(nodeID)
	if addr == "" {
		return "", fmt.Errorf("no HTTP address for node %s", nodeID)
	}
	scheme := "http"
	if c.config.TLS.Enabled {
		scheme = "https"
	}
	return scheme + "://" + addr, nil
}

// post sends a JSON POST request and decodes the response.
func (c *GRPCClient) post(ctx context.Context, url string, reqBody, respBody interface{}) error {
	data, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("http post %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("peer returned HTTP %d", resp.StatusCode)
	}

	if respBody != nil {
		if err := json.NewDecoder(resp.Body).Decode(respBody); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

// ---- wire types (must match server/internal_api.go) ----

type replicateReq struct {
	Key     []byte     `json:"key"`
	Sibling siblingDTO `json:"sibling"`
}

type siblingDTO struct {
	Value     []byte       `json:"value"`
	Context   []contextDTO `json:"context"`
	Tombstone bool         `json:"tombstone"`
	Timestamp int64        `json:"timestamp"`
}

type contextDTO struct {
	NodeID  string `json:"node_id"`
	Counter uint64 `json:"counter"`
}

type replicateResp struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

type internalReadReq struct {
	Key []byte `json:"key"`
}

type internalReadResp struct {
	Found    bool         `json:"found"`
	Siblings []siblingDTO `json:"siblings"`
}

type heartbeatReq struct {
	SenderID string `json:"sender_id"`
}

type heartbeatResp struct {
	ResponderID string `json:"responder_id"`
}

type merkleRootReq struct{}

type merkleRootResp struct {
	MerkleRoot []byte `json:"merkle_root"`
}

// ---- helpers ----

func vclockToDTO(vc vclock.VectorClock) []contextDTO {
	entries := make([]contextDTO, 0)
	for nodeID, counter := range vc.Entries() {
		entries = append(entries, contextDTO{NodeID: nodeID, Counter: counter})
	}
	return entries
}

func dtoToVclock(entries []contextDTO) vclock.VectorClock {
	vc := vclock.NewVectorClock()
	for _, e := range entries {
		vc.SetString(e.NodeID, e.Counter)
	}
	return vc
}

func siblingSetToDTO(ss *storage.SiblingSet) []siblingDTO {
	if ss == nil {
		return nil
	}
	out := make([]siblingDTO, 0, len(ss.Siblings))
	for _, sib := range ss.Siblings {
		out = append(out, siblingDTO{
			Value:     sib.Value,
			Context:   vclockToDTO(sib.VClock),
			Tombstone: sib.Tombstone,
			Timestamp: sib.Timestamp,
		})
	}
	return out
}

// RemotePut writes to a remote node via POST /internal/replicate
func (c *GRPCClient) RemotePut(ctx context.Context, nodeID common.NodeID, key []byte, siblings *storage.SiblingSet) error {
	base, err := c.httpAddr(nodeID)
	if err != nil {
		return err
	}

	// Send each sibling as a separate replicate request so the peer can merge them.
	for _, sib := range siblings.Siblings {
		req := replicateReq{
			Key: key,
			Sibling: siblingDTO{
				Value:     sib.Value,
				Context:   vclockToDTO(sib.VClock),
				Tombstone: sib.Tombstone,
				Timestamp: sib.Timestamp,
			},
		}
		var resp replicateResp
		if err := c.post(ctx, base+"/internal/replicate", req, &resp); err != nil {
			return fmt.Errorf("replicate to %s: %w", nodeID, err)
		}
		if !resp.Success {
			return fmt.Errorf("replicate to %s failed: %s", nodeID, resp.Error)
		}
	}
	return nil
}

// RemoteGet reads from a remote node via POST /internal/read
func (c *GRPCClient) RemoteGet(ctx context.Context, nodeID common.NodeID, key []byte) (*storage.SiblingSet, error) {
	base, err := c.httpAddr(nodeID)
	if err != nil {
		return nil, err
	}

	var resp internalReadResp
	if err := c.post(ctx, base+"/internal/read", internalReadReq{Key: key}, &resp); err != nil {
		return nil, fmt.Errorf("remote read from %s: %w", nodeID, err)
	}

	if !resp.Found {
		// Return nil,nil — "not found" is a successful replica response.
		// The coordinator decides ErrKeyNotFound from the merged empty result.
		return nil, nil
	}

	siblings := make([]storage.Sibling, 0, len(resp.Siblings))
	for _, dto := range resp.Siblings {
		siblings = append(siblings, storage.Sibling{
			Value:     dto.Value,
			VClock:    dtoToVclock(dto.Context),
			Tombstone: dto.Tombstone,
			Timestamp: dto.Timestamp,
		})
	}
	return &storage.SiblingSet{Siblings: siblings}, nil
}

// SendHeartbeat sends heartbeat to a remote node via POST /internal/heartbeat
func (c *GRPCClient) SendHeartbeat(ctx context.Context, nodeID common.NodeID) error {
	base, err := c.httpAddr(nodeID)
	if err != nil {
		return err
	}

	req := heartbeatReq{SenderID: string(c.localID)}
	var resp heartbeatResp
	if err := c.post(ctx, base+"/internal/heartbeat", req, &resp); err != nil {
		return fmt.Errorf("heartbeat to %s: %w", nodeID, err)
	}
	return nil
}

// Heartbeat sends heartbeat to a remote node (alias for SendHeartbeat)
func (c *GRPCClient) Heartbeat(ctx context.Context, nodeID common.NodeID) error {
	return c.SendHeartbeat(ctx, nodeID)
}

// GetMerkleRoot gets merkle root from a remote node via POST /internal/merkle-root
func (c *GRPCClient) GetMerkleRoot(ctx context.Context, nodeID common.NodeID) ([]byte, error) {
	base, err := c.httpAddr(nodeID)
	if err != nil {
		return nil, err
	}

	var resp merkleRootResp
	if err := c.post(ctx, base+"/internal/merkle-root", merkleRootReq{}, &resp); err != nil {
		return nil, fmt.Errorf("get merkle root from %s: %w", nodeID, err)
	}
	return resp.MerkleRoot, nil
}

// NotifyLeaving sends a graceful-leave notification to a peer via POST /internal/notify-leaving
func (c *GRPCClient) NotifyLeaving(ctx context.Context, nodeID common.NodeID) error {
	base, err := c.httpAddr(nodeID)
	if err != nil {
		return err
	}
	req := notifyLeavingReq{NodeID: string(c.localID)}
	var resp notifyLeavingResp
	if err := c.post(ctx, base+"/internal/notify-leaving", req, &resp); err != nil {
		return fmt.Errorf("notify leaving to %s: %w", nodeID, err)
	}
	return nil
}

// Close is a no-op; http.Client manages connection lifecycles internally.
func (c *GRPCClient) Close() error {
	c.httpClient.CloseIdleConnections()
	return nil
}

type notifyLeavingReq struct {
	NodeID string `json:"node_id"`
}

type notifyLeavingResp struct {
	Acknowledged bool `json:"acknowledged"`
}
