// Package integration holds cross-module integration test SKELETONS for
// GoQuorum v2: a put/get round trip, quorum reads under partial replica
// unavailability, sibling conflict resolution, and anti-entropy
// convergence, all driven through test/harness's in-process node/cluster
// helpers.
//
// Every test in this package currently calls t.Skip (via the shared
// newTestCluster helper) once harness.StartCluster reports
// contracts.ErrNotImplemented, surfaced transitively from
// infra/storage/pebble.NewStore. Each test's intended assertions are
// sketched as comments below that point, so the shape of the eventual
// test is documented even though it does not run yet.
package integration
