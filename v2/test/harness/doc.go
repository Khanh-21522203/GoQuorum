// Package harness provides helpers for spinning up in-process GoQuorum v2
// nodes and clusters for integration tests and benchmarks. StartNode and
// StartCluster wrap server/app.Server directly — the real composition
// root — so callers exercise the same boot path a production node does,
// rather than a parallel test-only implementation.
//
// This is a scaffold: StartNode calls through to server/app.New, which in
// turn opens infra/storage/pebble.Store — itself a typed stub returning
// contracts.ErrNotImplemented until Pebble is wired in (CONVENTIONS.md's
// scaffold rules). Until then, StartNode/StartCluster always return an
// error wrapping contracts.ErrNotImplemented; callers (see
// test/integration and test/benchmarks) should treat that as "not
// implemented yet", not as a harness bug.
//
// (v1: no equivalent package existed; v1's tests either drove
// internal/cluster types directly or shelled out to a built cmd/quorum
// binary against a temp dir and free ports.)
package harness
