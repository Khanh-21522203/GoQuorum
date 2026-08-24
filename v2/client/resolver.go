package client

import (
	"goquorum.io/v2/contracts"
	"goquorum.io/v2/contracts/vclock"
)

// ConflictResolver resolves the sibling values returned by a Get into a
// single value and merged causal context.
//
// (v1: client/client.go ConflictResolver)
type ConflictResolver interface {
	Resolve(siblings []Sibling) ([]byte, vclock.VectorClock, error)
}

// LWWResolver implements last-write-wins conflict resolution: it merges the
// causal context of every sibling and returns the value of the sibling
// with the highest Timestamp.
//
// (v1: client/client.go LWWResolver)
type LWWResolver struct{}

var _ ConflictResolver = (*LWWResolver)(nil)

// Resolve merges every sibling's causal context via vclock.Merge and
// returns the Value of the sibling with the highest Timestamp.
//
// TODO(v2): merge siblings' Context fields with VectorClock.Merge and
// select the Value of the sibling with the highest Timestamp (v1:
// client/client.go LWWResolver.Resolve / findLatest).
func (r *LWWResolver) Resolve(siblings []Sibling) ([]byte, vclock.VectorClock, error) {
	return nil, vclock.VectorClock{}, contracts.ErrNotImplemented
}
