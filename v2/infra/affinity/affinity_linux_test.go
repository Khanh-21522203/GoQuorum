//go:build linux

package affinity

import (
	"runtime"
	"sync"
	"testing"

	"golang.org/x/sys/unix"
)

func TestLockToCore_PinsAffinityToExactlyOneCore(t *testing.T) {
	if runtime.NumCPU() < 1 {
		t.Skip("no CPUs reported")
	}

	var (
		wg     sync.WaitGroup
		gotErr error
		gotSet unix.CPUSet
		getErr error
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer runtime.UnlockOSThread()

		gotErr = LockToCore(0)
		if gotErr != nil {
			return
		}
		getErr = unix.SchedGetaffinity(0, &gotSet)
	}()
	wg.Wait()

	if gotErr != nil {
		t.Fatalf("LockToCore(0): %v", gotErr)
	}
	if getErr != nil {
		t.Fatalf("SchedGetaffinity: %v", getErr)
	}
	if !gotSet.IsSet(0) {
		t.Fatal("expected core 0 to be set in the affinity mask")
	}
	if count := gotSet.Count(); count != 1 {
		t.Fatalf("expected exactly 1 core in the affinity mask, got %d", count)
	}
}

func TestLockToCore_RejectsOutOfRangeCore(t *testing.T) {
	var (
		wg  sync.WaitGroup
		err error
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer runtime.UnlockOSThread()
		err = LockToCore(runtime.NumCPU() + 1)
	}()
	wg.Wait()

	if err == nil {
		t.Fatal("expected an error for an out-of-range core")
	}
}
