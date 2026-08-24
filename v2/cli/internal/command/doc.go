// Package command implements quorumctl's subcommands. Each exported
// function has the signature Func and is invoked by cli/cmd/quorumctl's
// dispatch table with the parsed --addr flag value and the subcommand's
// positional arguments.
//
// Get, Put, and Delete talk to a GoQuorum node through the client library
// (goquorum.io/v2/client). Status, Ring, KeyInfo, and Compact are stubs
// until a client-side admin RPC surface exists over server/api.AdminAPI and
// server/api.InternalAPI.
//
// (v1: cmd/quorumctl/main.go)
package command
