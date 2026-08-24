// Package vclock implements vector clocks used to track causality between
// writes in GoQuorum v2.
//
// # Value semantics
//
// VectorClock is a struct wrapping an unexported map of per-node entries.
// Because Go maps are reference types, a plain assignment (vc2 := vc1)
// copies the VectorClock struct header but NOT the underlying map: both
// values alias the same entries, so mutating one through Tick, Set, or
// Merge is visible through the other. v1 (api/vclock) had exactly this
// footgun.
//
// v2 fixes this by contract: Copy() returns a VectorClock backed by a new,
// independent map. Any caller that needs an isolated snapshot before
// mutating a clock (e.g. before Tick or Merge) MUST call Copy() explicitly.
// Plain assignment remains a shallow, aliasing copy; do not rely on it for
// isolation.
package vclock
