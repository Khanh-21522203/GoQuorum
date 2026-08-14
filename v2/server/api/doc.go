// Package api implements GoQuorum v2's service-API layer: ClientAPI (the
// client-facing KV surface), AdminAPI (health/cluster/metrics/key
// introspection), and InternalAPI (node-to-node replication, reads,
// heartbeats, and Merkle-root exchange). Each wraps an
// engine/coordinator.Coordinator and/or the engine ports (storage.Storage,
// transport.Transport) directly, using engine's domain types (vclock,
// storage.Sibling, node.NodeID) rather than contracts/wire: the wire
// boundary belongs to whatever HTTP/JSON or gRPC front door eventually
// calls into this package (see gateway/http).
//
// (v1: internal/server/client_api.go, admin_api.go, internal_api.go)
package api
