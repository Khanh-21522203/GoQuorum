package api

import (
	"context"

	"goquorum.io/v2/contracts"
	"goquorum.io/v2/contracts/vclock"
	"goquorum.io/v2/engine/coordinator"
)

// ClientAPI implements the client-facing KV service: Get, Put, Delete, and
// their batch variants, on top of an engine/coordinator.Coordinator.
//
// (v1: internal/server/client_api.go ClientAPI)
type ClientAPI struct {
	coordinator *coordinator.Coordinator
}

// NewClientAPI creates a client API service over the given coordinator.
//
// (v1: internal/server/client_api.go NewClientAPI)
func NewClientAPI(coord *coordinator.Coordinator) *ClientAPI {
	return &ClientAPI{coordinator: coord}
}

// GetResult is the outcome of a Get: the sibling set found for the key, if
// any.
//
// (v1: internal/server/client_api.go GetResult)
type GetResult struct {
	Siblings []SiblingResult
	Found    bool
}

// SiblingResult is a single conflicting version of a value returned by
// Get.
//
// (v1: internal/server/client_api.go SiblingResult)
type SiblingResult struct {
	Value     []byte
	Context   vclock.VectorClock
	Tombstone bool
	Timestamp int64
}

// PutResult is the outcome of a Put: the causal context after the write.
//
// (v1: internal/server/client_api.go PutResult)
type PutResult struct {
	Context vclock.VectorClock
}

// BatchGetResult is a single key's outcome within a batched Get.
//
// (v1: internal/server/client_api.go BatchGetResultAPI)
type BatchGetResult struct {
	Key      []byte
	Siblings []SiblingResult
	Error    string // empty if success.
}

// BatchPutItem is a single key/value/context triple within a batched Put
// request.
//
// (v1: internal/server/client_api.go BatchPutItemAPI)
type BatchPutItem struct {
	Key     []byte
	Value   []byte
	Context vclock.VectorClock
}

// BatchPutResult is a single key's outcome within a batched Put.
//
// (v1: internal/server/client_api.go BatchPutResultAPI)
type BatchPutResult struct {
	Key     []byte
	Context vclock.VectorClock
	Error   string // empty if success.
}

// Get retrieves the sibling set for key, applying rQuorum/timeoutMs
// overrides to the read.
//
// TODO(v2): validate key length, apply timeoutMs as a context deadline,
// call c.coordinator.Get(ctx, string(key)), and filter tombstones from the
// result (v1: internal/server/client_api.go ClientAPI.Get).
func (c *ClientAPI) Get(ctx context.Context, key []byte, rQuorum int, timeoutMs int) (*GetResult, error) {
	return nil, contracts.ErrNotImplemented
}

// Put stores value for key, causally ordered by causal, applying
// wQuorum/timeoutMs/ttlSeconds overrides to the write.
//
// TODO(v2): validate key/value size, apply timeoutMs as a context
// deadline, build coordinator.PutOptions{TTLSeconds: ttlSeconds}, and call
// c.coordinator.Put(ctx, string(key), value, causal, opts...) (v1:
// internal/server/client_api.go ClientAPI.Put). wQuorum has no home on
// coordinator.PutOptions yet (it carries only TTLSeconds); a real
// implementation needs either a PutOptions.W field or a per-call override
// on the coordinator.
func (c *ClientAPI) Put(ctx context.Context, key, value []byte, causal vclock.VectorClock, wQuorum int, timeoutMs int, ttlSeconds int64) (*PutResult, error) {
	return nil, contracts.ErrNotImplemented
}

// Delete removes key by writing a tombstone causally ordered by causal.
//
// TODO(v2): validate key and that causal is non-empty, apply timeoutMs as
// a context deadline, and call c.coordinator.Delete(ctx, string(key),
// causal) (v1: internal/server/client_api.go ClientAPI.Delete).
func (c *ClientAPI) Delete(ctx context.Context, key []byte, causal vclock.VectorClock, wQuorum int, timeoutMs int) error {
	return contracts.ErrNotImplemented
}

// BatchGet retrieves values for multiple keys concurrently.
//
// engine/coordinator.Coordinator's spec surface is Start/Stop/Put/Get/
// Delete/GetMerkleRoot only: there is no coordinator-level batch method
// (v1's ClientAPI.BatchGet called coordinator.BatchGet directly). This
// stub must NOT invent a delegation to a nonexistent method; a real
// implementation should fan out one c.coordinator.Get call per key (v1:
// internal/server/client_api.go ClientAPI.BatchGet).
func (c *ClientAPI) BatchGet(ctx context.Context, keys [][]byte) ([]BatchGetResult, error) {
	return nil, contracts.ErrNotImplemented
}

// BatchPut stores multiple key/value pairs concurrently.
//
// engine/coordinator.Coordinator's spec surface is Start/Stop/Put/Get/
// Delete/GetMerkleRoot only: there is no coordinator-level batch method
// (v1's ClientAPI.BatchPut called coordinator.BatchPut directly). This
// stub must NOT invent a delegation to a nonexistent method; a real
// implementation should fan out one c.coordinator.Put call per item (v1:
// internal/server/client_api.go ClientAPI.BatchPut).
func (c *ClientAPI) BatchPut(ctx context.Context, items []BatchPutItem) ([]BatchPutResult, error) {
	return nil, contracts.ErrNotImplemented
}
