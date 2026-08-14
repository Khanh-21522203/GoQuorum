//go:build linux

package affinity

import (
	"fmt"
	"runtime"

	"golang.org/x/sys/unix"
)

// LockToCore locks the calling goroutine to its current OS thread (via
// runtime.LockOSThread) and pins that thread's CPU affinity to exactly
// core. The caller must run whatever work should stay on that core in the
// same goroutine, after LockToCore returns — typically the goroutine that
// then calls (*engine/reactor.Reactor).Run.
func LockToCore(core int) error {
	if core < 0 || core >= runtime.NumCPU() {
		return fmt.Errorf("affinity: core %d out of range [0, %d)", core, runtime.NumCPU())
	}

	runtime.LockOSThread()

	var set unix.CPUSet
	set.Zero()
	set.Set(core)
	if err := unix.SchedSetaffinity(0, &set); err != nil {
		return fmt.Errorf("affinity: sched_setaffinity core %d: %w", core, err)
	}
	return nil
}
