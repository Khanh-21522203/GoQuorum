package client

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	pb "GoQuorum/api"
	"GoQuorum/internal/vclock"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// ClientConfig holds configuration for the GoQuorum client.
type ClientConfig struct {
	Addr           string        // gRPC server address, e.g. "localhost:7070"
	DialTimeout    time.Duration // Default: 5s
	RequestTimeout time.Duration // Default: 5s
	MaxRetries     int           // Default: 3 (0 = no retry)
	RetryBaseDelay time.Duration // Default: 100ms
}

// DefaultClientConfig returns a ClientConfig with sensible defaults.
func DefaultClientConfig(addr string) ClientConfig {
	return ClientConfig{
		Addr:           addr,
		DialTimeout:    5 * time.Second,
		RequestTimeout: 5 * time.Second,
		MaxRetries:     3,
		RetryBaseDelay: 100 * time.Millisecond,
	}
}

// Client is a high-level GoQuorum client that wraps the gRPC stub.
type Client struct {
	conn   *grpc.ClientConn
	stub   pb.GoQuorumClient
	config ClientConfig
}

// NewClient dials the gRPC server and returns a ready-to-use Client.
func NewClient(cfg ClientConfig) (*Client, error) {
	if cfg.DialTimeout == 0 {
		cfg.DialTimeout = 5 * time.Second
	}
	if cfg.RequestTimeout == 0 {
		cfg.RequestTimeout = 5 * time.Second
	}
	if cfg.RetryBaseDelay == 0 {
		cfg.RetryBaseDelay = 100 * time.Millisecond
	}

	dialCtx, cancel := context.WithTimeout(context.Background(), cfg.DialTimeout)
	defer cancel()

	conn, err := grpc.DialContext(dialCtx, cfg.Addr, //nolint:staticcheck // DialContext deprecated in grpc v2 but still works
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", cfg.Addr, err)
	}

	return &Client{
		conn:   conn,
		stub:   pb.NewGoQuorumClient(conn),
		config: cfg,
	}, nil
}

// Get retrieves all sibling values for key.
// Returns ErrKeyNotFound (codes.NotFound) when the key does not exist.
// The returned siblings may include concurrent versions; use a ConflictResolver to merge them.
func (c *Client) Get(ctx context.Context, key []byte) ([]Sibling, error) {
	var resp *pb.GetResponse
	err := c.withRetry(ctx, func(ctx context.Context) error {
		var e error
		resp, e = c.stub.Get(ctx, &pb.GetRequest{Key: key})
		return e
	})
	if err != nil {
		return nil, convertError(err)
	}

	siblings := make([]Sibling, 0, len(resp.GetSiblings()))
	for _, s := range resp.GetSiblings() {
		siblings = append(siblings, Sibling{
			Value:     s.GetValue(),
			Context:   protoToVClock(s.GetContext()),
			Timestamp: s.GetTimestamp(),
			Tombstone: s.GetTombstone(),
		})
	}
	return siblings, nil
}

// Put stores value for key. causalCtx should be the context returned by a prior Get
// (pass vclock.NewVectorClock() for a blind write). Returns the new causal context.
func (c *Client) Put(ctx context.Context, key, value []byte, causalCtx vclock.VectorClock) (vclock.VectorClock, error) {
	var resp *pb.PutResponse
	err := c.withRetry(ctx, func(ctx context.Context) error {
		var e error
		resp, e = c.stub.Put(ctx, &pb.PutRequest{
			Key:     key,
			Value:   value,
			Context: vclockToProto(causalCtx),
		})
		return e
	})
	if err != nil {
		return vclock.VectorClock{}, convertError(err)
	}
	return protoToVClock(resp.GetContext()), nil
}

// Delete removes key by writing a tombstone. causalCtx must come from a prior Get.
func (c *Client) Delete(ctx context.Context, key []byte, causalCtx vclock.VectorClock) error {
	err := c.withRetry(ctx, func(ctx context.Context) error {
		_, e := c.stub.Delete(ctx, &pb.DeleteRequest{
			Key:     key,
			Context: vclockToProto(causalCtx),
		})
		return e
	})
	return convertError(err)
}

// Close closes the underlying gRPC connection.
func (c *Client) Close() error {
	return c.conn.Close()
}

// withRetry executes fn with a per-call timeout, retrying on transient errors.
func (c *Client) withRetry(parent context.Context, fn func(ctx context.Context) error) error {
	maxAttempts := c.config.MaxRetries + 1
	if maxAttempts < 1 {
		maxAttempts = 1
	}

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		callCtx, cancel := context.WithTimeout(parent, c.config.RequestTimeout)
		lastErr = fn(callCtx)
		cancel()

		if lastErr == nil {
			return nil
		}

		if !isRetryable(lastErr) {
			return lastErr
		}

		if attempt+1 < maxAttempts {
			// Exponential backoff with jitter
			delay := c.config.RetryBaseDelay * (1 << uint(attempt))
			var jitter time.Duration
			if jitterRange := int64(delay) / 2; jitterRange > 0 {
				jitter = time.Duration(rand.Int63n(jitterRange)) //nolint:gosec
			}
			select {
			case <-time.After(delay + jitter):
			case <-parent.Done():
				return parent.Err()
			}
		}
	}
	return lastErr
}

// isRetryable returns true for transient gRPC errors that are safe to retry.
func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	code := status.Code(err)
	return code == codes.Unavailable || code == codes.DeadlineExceeded
}

// convertError maps gRPC status errors to friendlier sentinel errors where applicable.
func convertError(err error) error {
	if err == nil {
		return nil
	}
	code := status.Code(err)
	if code == codes.NotFound {
		return ErrKeyNotFound
	}
	return err
}

// ErrKeyNotFound is returned by Get when the key does not exist.
var ErrKeyNotFound = fmt.Errorf("key not found")

// ── proto ↔ vclock conversions ──────────────────────────────────────────────

func vclockToProto(vc vclock.VectorClock) *pb.Context {
	entries := make([]*pb.ContextEntry, 0)
	for nodeID, counter := range vc.Entries() {
		entries = append(entries, &pb.ContextEntry{
			NodeId:  nodeID,
			Counter: counter,
		})
	}
	return &pb.Context{Entries: entries}
}

func protoToVClock(ctx *pb.Context) vclock.VectorClock {
	vc := vclock.NewVectorClock()
	if ctx == nil {
		return vc
	}
	for _, e := range ctx.GetEntries() {
		vc.SetString(e.GetNodeId(), e.GetCounter())
	}
	return vc
}

// ── Sibling and ConflictResolver (kept from original stub) ──────────────────

// Sibling represents one version of a value returned by a Get.
type Sibling struct {
	Value     []byte
	Context   vclock.VectorClock
	Timestamp int64
	Tombstone bool
}

// ConflictResolver resolves concurrent sibling values into a single value.
type ConflictResolver interface {
	Resolve(siblings []Sibling) ([]byte, vclock.VectorClock, error)
}

// LWWResolver implements last-write-wins conflict resolution.
type LWWResolver struct{}

func (r *LWWResolver) Resolve(siblings []Sibling) ([]byte, vclock.VectorClock, error) {
	mergedContext := vclock.NewVectorClock()
	for _, s := range siblings {
		mergedContext.Merge(s.Context)
	}
	latest := findLatest(siblings)
	return latest.Value, mergedContext, nil
}

func findLatest(siblings []Sibling) Sibling {
	if len(siblings) == 0 {
		return Sibling{}
	}
	best := siblings[0]
	for _, s := range siblings[1:] {
		if s.Timestamp > best.Timestamp {
			best = s
		}
	}
	return best
}
