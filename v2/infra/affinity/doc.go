// Package affinity pins a goroutine's underlying OS thread to a specific
// CPU core, so latency-sensitive single-threaded work (e.g. the reactor
// goroutine driving engine/reactor.Reactor.Run) is not migrated or
// preempted by unrelated goroutines competing for the same core.
//
// LockToCore guarantees only that its caller's thread runs on the given
// core, not that the given core runs only its caller's thread — the OS
// scheduler may still place other threads there unless the deployment also
// excludes the core from everything else (e.g. Linux isolcpus= or a cgroup
// cpuset). That stronger guarantee is a deployment concern outside this
// package's scope.
package affinity
