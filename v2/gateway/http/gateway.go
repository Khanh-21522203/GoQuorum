package http

import (
	"net/http"

	"goquorum.io/v2/contracts"
	"goquorum.io/v2/engine/coordinator"
)

// Gateway is the HTTP/JSON front door for the client and admin KV API. It
// wraps an engine/coordinator.Coordinator and will eventually decode
// HTTP/JSON requests into contracts/wire types, invoke the coordinator,
// and encode the wire response back to JSON.
//
// TODO(v2): once contracts/wire is proto-generated, replace hand-rolled
// JSON (de)serialization here with grpc-gateway-generated handlers
// (v1: api/goquorum.pb.gw.go, internal/server/server.go).
type Gateway struct {
	coord *coordinator.Coordinator
}

// New constructs a Gateway over the given coordinator. coord may be nil in
// this scaffold phase; a real construction requires a non-nil coordinator
// to serve any route.
//
// TODO(v2): accept additional dependencies as they materialize (e.g. an
// AdminAPI/InternalAPI once server/api is implemented, TLS/auth middleware
// config) (v1: internal/server/server.go NewServer).
func New(coord *coordinator.Coordinator) *Gateway {
	return &Gateway{coord: coord}
}

// Handler returns the HTTP handler serving the gateway's routes.
//
// Intended routes (v1: api/proto/goquorum.proto, internal/server/server.go):
//
//	Client API (grpc-gateway translated GoQuorum service):
//	  GET    /v1/keys/{key}   — Get
//	  PUT    /v1/keys/{key}   — Put
//	  DELETE /v1/keys/{key}   — Delete
//	  POST   /v1/batch/get    — BatchGet
//	  POST   /v1/batch/put    — BatchPut
//
//	Admin API (grpc-gateway translated GoQuorumAdmin service):
//	  GET /v1/admin/health    — Health
//	  GET /v1/admin/cluster   — ClusterInfo
//	  GET /v1/admin/metrics   — GetMetrics
//	  GET /v1/admin/keys/{key} — KeyInfo
//
//	Operational endpoints (plain net/http, not grpc-gateway):
//	  GET /health  — liveness/readiness probe
//	  GET /metrics — Prometheus scrape endpoint
//
// This scaffold returns a single stub handler that answers every route
// with 501 Not Implemented.
//
// TODO(v2): mount a grpc-gateway runtime.ServeMux registered against the
// GoQuorum/GoQuorumAdmin services for the /v1/* routes, and plain
// http.ServeMux handlers for /health and /metrics (v1:
// internal/server/server.go Server.Start, internal/server/health.go).
// TODO(v2): import grpc-gateway
func (g *Gateway) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, contracts.ErrNotImplemented.Error(), http.StatusNotImplemented)
	})
}
