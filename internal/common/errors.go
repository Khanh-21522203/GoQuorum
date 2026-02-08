package common

import (
	"bytes"
	"errors"
	"fmt"
)

type QuorumErrorType int

const (
	QuorumNotReached       QuorumErrorType = iota // Section 9.1
	AllReplicasUnavailable                        // Section 9.2
)

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

// QuorumError represents a quorum failure (Section 9.1)
type QuorumError struct {
	Type          QuorumErrorType
	Required      int            // R or W quorum required
	Achieved      int            // Actual successful responses
	Operation     string         // "read" or "write"
	ReplicaErrors []ReplicaError // Individual replica errors (Section 9.1)
}

// ReplicaError represents an error from a single replica
type ReplicaError struct {
	NodeID NodeID
	Addr   string // Optional: node address for debugging
	Error  error
}

// Error implements error interface (Section 9.1)
func (e *QuorumError) Error() string {
	return fmt.Sprintf("%s: %s quorum %d/%d (required: %d)",
		e.Type.String(),
		e.Operation,
		e.Achieved,
		e.Required,
		e.Required)
}

// Details returns detailed error information (Section 9.1)
// Response includes:
// - Number of successful responses
// - Expected quorum
// - Individual replica errors (timeout, unavailable, etc.)
func (e *QuorumError) Details() map[string]interface{} {
	replicaDetails := make([]map[string]interface{}, len(e.ReplicaErrors))
	for i, re := range e.ReplicaErrors {
		replicaDetails[i] = map[string]interface{}{
			"node_id": string(re.NodeID),
			"addr":    re.Addr,
			"error":   re.Error.Error(),
		}
	}

	return map[string]interface{}{
		"type":           e.Type.String(),
		"operation":      e.Operation,
		"required":       e.Required,
		"achieved":       e.Achieved,
		"replica_errors": replicaDetails,
	}
}

// String returns a human-readable error message with replica details
func (e *QuorumError) String() string {
	var buf bytes.Buffer

	buf.WriteString(e.Error())
	buf.WriteString("\n\nReplica Errors:\n")

	if len(e.ReplicaErrors) == 0 {
		buf.WriteString("  (no replica errors recorded)\n")
	} else {
		for _, re := range e.ReplicaErrors {
			buf.WriteString(fmt.Sprintf("  - %s: %v\n", re.NodeID, re.Error))
		}
	}

	return buf.String()
}

var (
	ErrKeyNotFound   = errors.New("key not found")
	ErrStorageClosed = errors.New("storage is closed")
	ErrStorageFull   = fmt.Errorf("storage full (disk space exhausted)")
	ErrStorageIO     = fmt.Errorf("storage I/O error")
)
