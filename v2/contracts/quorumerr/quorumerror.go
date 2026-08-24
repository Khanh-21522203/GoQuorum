package quorumerr

import (
	"fmt"

	"goquorum.io/v2/contracts/node"
)

// QuorumErrorType classifies why a quorum operation failed.
type QuorumErrorType int

const (
	QuorumNotReached       QuorumErrorType = iota // Fewer than the required replicas responded.
	AllReplicasUnavailable                        // No replica in the preference list responded.
)

// String returns the human-readable name of the error type.
func (t QuorumErrorType) String() string {
	switch t {
	case QuorumNotReached:
		return "QUORUM_NOT_REACHED"
	case AllReplicasUnavailable:
		return "ALL_REPLICAS_UNAVAILABLE"
	default:
		return "UNKNOWN"
	}
}

// ReplicaError records the error returned by a single replica during a
// quorum operation.
type ReplicaError struct {
	NodeID node.NodeID
	Addr   string // Optional: node address for debugging.
	Error  error
}

// QuorumError represents a quorum failure: fewer than the required number of
// replicas responded successfully to a read or write.
type QuorumError struct {
	Type          QuorumErrorType
	Required      int    // R or W quorum required.
	Achieved      int    // Actual successful responses.
	Operation     string // "read" or "write".
	ReplicaErrors []ReplicaError
}

// Error implements the error interface.
func (e *QuorumError) Error() string {
	return fmt.Sprintf("%s: %s quorum %d/%d (required: %d)",
		e.Type.String(),
		e.Operation,
		e.Achieved,
		e.Required,
		e.Required)
}

// Details returns detailed error information for API responses: the
// successful-response count, the expected quorum, and the individual
// replica errors (timeout, unavailable, etc.).
//
// TODO(v2): build a map with keys "type", "operation", "required",
// "achieved", and "replica_errors" (a per-replica breakdown of node_id, addr,
// and error) (v1: api/common/errors.go QuorumError.Details).
func (e *QuorumError) Details() map[string]interface{} {
	return nil
}
