// Package coordinator implements the quorum orchestrator: it fans reads
// and writes out to a key's preference list of replicas, enforces N/R/W
// quorum, and drives read repair and anti-entropy.
//
// Coordinator depends only on the storage.Storage and transport.Transport
// PORTS, never on a concrete storage engine or network client — this
// substitution is the central point of the v2 rewrite (v1:
// internal/cluster/coordinator.go depended on a concrete *storage.Storage
// and an RPCClient implementation).
package coordinator
