// Package http implements GoQuorum v2's HTTP/JSON gateway: it translates
// REST requests into calls against the engine coordinator. v1 performed
// this translation via grpc-gateway, generated from
// api/proto/goquorum.proto, in front of a gRPC service implementation
// (internal/server/server.go, internal/server/grpc_adapters.go).
package http
