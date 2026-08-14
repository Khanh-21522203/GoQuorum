package command

import (
	"context"
	"fmt"
	"io"

	"goquorum.io/v2/contracts"
)

// Status shows cluster health.
//
// TODO(v2): dial addr's admin RPC surface and report health checks, once a
// client-side admin RPC stub exists (v1: cmd/quorumctl/main.go runStatus
// called pb.GoQuorumAdminClient.Health; v2's equivalent server-side surface
// is server/api.AdminAPI.Health).
func Status(ctx context.Context, addr string, args []string, out io.Writer) error {
	return contracts.ErrNotImplemented
}

// Ring shows cluster/hash-ring info.
//
// TODO(v2): dial addr's admin RPC surface and report ring membership, once a
// client-side admin RPC stub exists (v1: cmd/quorumctl/main.go runRing
// called pb.GoQuorumAdminClient.ClusterInfo; v2's equivalent server-side
// surface is server/api.AdminAPI.ClusterInfo).
func Ring(ctx context.Context, addr string, args []string, out io.Writer) error {
	return contracts.ErrNotImplemented
}

// KeyInfo shows per-replica info for a key.
//
// TODO(v2): dial addr's admin RPC surface and report per-replica sibling
// info, once a client-side admin RPC stub exists (v1: cmd/quorumctl/main.go
// runKeyInfo called pb.GoQuorumAdminClient.KeyInfo; v2's equivalent
// server-side surface is server/api.InternalAPI).
func KeyInfo(ctx context.Context, addr string, args []string, out io.Writer) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: key-info <key>")
	}
	return contracts.ErrNotImplemented
}

// Compact triggers storage compaction.
//
// TODO(v2): dial addr's admin RPC surface and trigger
// engine/storage.Storage compaction, once a client-side admin RPC stub
// exists (v1: cmd/quorumctl/main.go runCompact called
// pb.GoQuorumAdminClient.TriggerCompaction; v2's equivalent server-side
// surface is server/api.AdminAPI).
func Compact(ctx context.Context, addr string, args []string, out io.Writer) error {
	return contracts.ErrNotImplemented
}
