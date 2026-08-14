// Package transport defines the Transport port: the interface engine uses
// to talk to peer nodes, implemented by concrete adapters in infra/transport.
//
// v1 called this RPCClient (implemented by a GRPCClient that was, despite
// the name, an HTTP/JSON client). v2 gives it an honest name and keeps it a
// pure port: no net/http, no gRPC, no external types appear in the method
// set below (v1: internal/cluster/rpc_client.go).
package transport
