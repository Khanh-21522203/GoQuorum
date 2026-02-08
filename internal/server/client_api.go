package server

import (
	"context"
	"fmt"
	"time"

	"GoQuorum/internal/cluster"
	"GoQuorum/internal/storage"
	"GoQuorum/internal/vclock"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ClientAPI implements the client-facing gRPC service
type ClientAPI struct {
	coordinator *cluster.Coordinator
	storage     *storage.Storage
}

// NewClientAPI creates a new client API service
func NewClientAPI(coordinator *cluster.Coordinator, store *storage.Storage) *ClientAPI {
	return &ClientAPI{
		coordinator: coordinator,
		storage:     store,
	}
}

// Get retrieves value(s) for a key
func (c *ClientAPI) Get(ctx context.Context, key []byte, rQuorum int, timeoutMs int) (*GetResult, error) {
	// Validate key
	if len(key) == 0 {
		return nil, status.Error(codes.InvalidArgument, "key cannot be empty")
	}
	if len(key) > 64*1024 {
		return nil, status.Error(codes.InvalidArgument, "key exceeds 64KB limit")
	}

	// Apply timeout if specified
	if timeoutMs > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
		defer cancel()
	}

	// Perform read via coordinator
	siblings, err := c.coordinator.Get(ctx, string(key))
	if err != nil {
		return nil, convertError(err)
	}

	// Convert to result
	result := &GetResult{
		Siblings: make([]SiblingResult, 0, len(siblings)),
		Found:    len(siblings) > 0,
	}

	for _, sib := range siblings {
		if !sib.Tombstone { // Filter tombstones
			result.Siblings = append(result.Siblings, SiblingResult{
				Value:     sib.Value,
				Context:   vclockToContext(sib.VClock),
				Tombstone: sib.Tombstone,
				Timestamp: sib.Timestamp,
			})
		}
	}

	return result, nil
}

// Put stores a value for a key
func (c *ClientAPI) Put(ctx context.Context, key, value []byte, vClockContext *VClockContext, wQuorum int, timeoutMs int) (*PutResult, error) {
	// Validate key
	if len(key) == 0 {
		return nil, status.Error(codes.InvalidArgument, "key cannot be empty")
	}
	if len(key) > 64*1024 {
		return nil, status.Error(codes.InvalidArgument, "key exceeds 64KB limit")
	}

	// Validate value
	if len(value) > 1024*1024 {
		return nil, status.Error(codes.InvalidArgument, "value exceeds 1MB limit")
	}

	// Apply timeout if specified
	if timeoutMs > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
		defer cancel()
	}

	// Convert vClockContext to vector clock
	vc := contextToVClock(vClockContext)

	// Perform write via coordinator
	newVC, err := c.coordinator.Put(ctx, string(key), value, vc)
	if err != nil {
		return nil, convertError(err)
	}

	return &PutResult{
		Context: vclockToContext(newVC),
	}, nil
}

// Delete removes a key by writing a tombstone
func (c *ClientAPI) Delete(ctx context.Context, key []byte, vClockContext *VClockContext, wQuorum int, timeoutMs int) error {
	// Validate key
	if len(key) == 0 {
		return status.Error(codes.InvalidArgument, "key cannot be empty")
	}
	if len(key) > 64*1024 {
		return status.Error(codes.InvalidArgument, "key exceeds 64KB limit")
	}

	// vClockContext is required for delete
	if vClockContext == nil || len(vClockContext.Entries) == 0 {
		return status.Error(codes.InvalidArgument, "context is required for delete")
	}

	// Apply timeout if specified
	if timeoutMs > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
		defer cancel()
	}

	// Convert context to vector clock
	vc := contextToVClock(vClockContext)

	// Perform delete via coordinator
	err := c.coordinator.Delete(ctx, string(key), vc)
	if err != nil {
		return convertError(err)
	}

	return nil
}

// GetResult represents the result of a Get operation
type GetResult struct {
	Siblings []SiblingResult
	Found    bool
}

// SiblingResult represents a single sibling value
type SiblingResult struct {
	Value     []byte
	Context   *VClockContext
	Tombstone bool
	Timestamp int64
}

// PutResult represents the result of a Put operation
type PutResult struct {
	Context *VClockContext
}

// VClockContext represents a vector clock context for the API
type VClockContext struct {
	Entries map[string]uint64
}

// vclockToContext converts internal VectorClock to API context
func vclockToContext(vc vclock.VectorClock) *VClockContext {
	entries := make(map[string]uint64)
	for nodeID, counter := range vc.Entries() {
		entries[nodeID] = counter
	}

	return &VClockContext{Entries: entries}
}

// contextToVClock converts API context to internal VectorClock
func contextToVClock(ctx *VClockContext) vclock.VectorClock {
	if ctx == nil || len(ctx.Entries) == 0 {
		return vclock.NewVectorClock()
	}

	vc := vclock.NewVectorClock()
	for nodeID, counter := range ctx.Entries {
		vc.SetString(nodeID, counter)
	}

	return vc
}

// convertError converts internal errors to gRPC status errors
func convertError(err error) error {
	if err == nil {
		return nil
	}

	errStr := err.Error()

	// Map common errors to gRPC codes
	switch {
	case contains(errStr, "quorum not reached"):
		return status.Error(codes.Unavailable, errStr)
	case contains(errStr, "timeout"):
		return status.Error(codes.DeadlineExceeded, errStr)
	case contains(errStr, "storage full"):
		return status.Error(codes.ResourceExhausted, errStr)
	case contains(errStr, "invalid"):
		return status.Error(codes.InvalidArgument, errStr)
	default:
		return status.Error(codes.Internal, errStr)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsImpl(s, substr))
}

func containsImpl(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// Placeholder for when proto is not yet generated
var _ = fmt.Sprintf
