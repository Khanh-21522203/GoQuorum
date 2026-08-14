// Package client is the standalone Go client library for talking to a
// GoQuorum server. It wraps a transport connection (gRPC in v1) behind a
// small Get/Put/Delete surface, and provides a pluggable ConflictResolver
// for merging sibling values returned by concurrent writes.
//
// (v1: client/client.go)
package client
