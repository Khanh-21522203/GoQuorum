// Package httprpc implements engine/transport.Transport over HTTP/JSON, the
// wire protocol v1 actually used despite its RPCClient/GRPCClient naming
// (v1: internal/cluster/rpc_client.go). This is a scaffold: Client's
// methods are typed stubs; no net/http dependency is wired up yet.
package httprpc
