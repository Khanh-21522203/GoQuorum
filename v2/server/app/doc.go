// Package app is GoQuorum v2's composition root. Server.New wires concrete
// infra adapters (infra/storage/pebble, infra/transport/httprpc) into the
// engine ports (storage.Storage, transport.Transport), builds the engine
// domain graph (hashring.HashRing, membership.MembershipManager,
// coordinator.Coordinator) on top of them, and mounts gateway/http in
// front of the coordinator.
//
// (v1: cmd/quorum/main.go, internal/server/server.go)
package app
