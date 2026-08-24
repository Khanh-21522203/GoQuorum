//go:build !linux

package affinity

import "goquorum.io/v2/contracts"

// LockToCore is unavailable on non-Linux platforms: CPU affinity pinning
// via sched_setaffinity is a Linux-specific facility with no portable
// stdlib equivalent.
func LockToCore(core int) error {
	return contracts.ErrNotImplemented
}
