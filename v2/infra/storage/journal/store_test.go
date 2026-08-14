package journal

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"goquorum.io/v2/contracts/quorumerr"
	"goquorum.io/v2/contracts/vclock"
	"goquorum.io/v2/engine/reactor"
	"goquorum.io/v2/engine/storage"
	"goquorum.io/v2/infra/ioruntime"
)

// newTestRuntime opens a real io_uring-backed Runtime, mirroring
// infra/ioruntime's own test helper.
func newTestRuntime(t *testing.T) *ioruntime.Runtime {
	t.Helper()
	rt, err := ioruntime.New(64)
	if err != nil {
		t.Fatalf("ioruntime.New: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	return rt
}

// testStore wires a real Store to a real reactor.Reactor driven by a real
// ioruntime.Runtime, exactly per doc.go's ownership contract: the
// reactor's event handler is store.HandleCompletion, and every Store
// method call below is dispatched via PostFunc so it actually runs on the
// reactor's own goroutine, never on the calling test goroutine.
type testStore struct {
	store *Store
	r     *reactor.Reactor
}

// openTestStore opens a Store at path and starts its reactor in the
// background, registering cleanup that stops the reactor and closes the
// store in the correct order.
func openTestStore(t *testing.T, rt *ioruntime.Runtime, path string) *testStore {
	t.Helper()
	store, err := Open(rt, Options{Path: path, NodeID: "node-a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	r := reactor.New(rt)
	r.SetEventHandler(store.HandleCompletion)

	errCh := make(chan error, 1)
	go func() { errCh <- r.Run() }()
	t.Cleanup(func() {
		r.RequestStop()
		select {
		case err := <-errCh:
			if err != nil {
				t.Errorf("reactor Run returned error: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("reactor Run did not return after RequestStop")
		}
		if err := store.Close(); err != nil {
			t.Errorf("Store.Close: %v", err)
		}
	})
	return &testStore{store: store, r: r}
}

func (ts *testStore) put(t *testing.T, key []byte, ss *storage.SiblingSet) error {
	t.Helper()
	done := make(chan error, 1)
	ts.r.PostFunc(func() {
		ts.store.Put(key, ss, func(err error) { done <- err })
	})
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("Put did not complete")
		return nil
	}
}

func (ts *testStore) delete(t *testing.T, key []byte, ctx vclock.VectorClock) error {
	t.Helper()
	done := make(chan error, 1)
	ts.r.PostFunc(func() {
		ts.store.Delete(key, ctx, func(err error) { done <- err })
	})
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("Delete did not complete")
		return nil
	}
}

type getResult struct {
	ss  *storage.SiblingSet
	err error
}

func (ts *testStore) get(t *testing.T, key []byte) getResult {
	t.Helper()
	done := make(chan getResult, 1)
	ts.r.PostFunc(func() {
		ts.store.Get(key, func(ss *storage.SiblingSet, err error) { done <- getResult{ss, err} })
	})
	select {
	case r := <-done:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("Get did not complete")
		return getResult{}
	}
}

func (ts *testStore) getRaw(t *testing.T, key []byte) getResult {
	t.Helper()
	done := make(chan getResult, 1)
	ts.r.PostFunc(func() {
		ts.store.GetRaw(key, func(ss *storage.SiblingSet, err error) { done <- getResult{ss, err} })
	})
	select {
	case r := <-done:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("GetRaw did not complete")
		return getResult{}
	}
}

func (ts *testStore) scan(t *testing.T, start, end []byte, fn storage.ScanFunc) error {
	t.Helper()
	done := make(chan error, 1)
	ts.r.PostFunc(func() {
		ts.store.Scan(start, end, fn, func(err error) { done <- err })
	})
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("Scan did not complete")
		return nil
	}
}

func (ts *testStore) stats(t *testing.T) storage.Stats {
	t.Helper()
	done := make(chan storage.Stats, 1)
	ts.r.PostFunc(func() { done <- ts.store.Stats() })
	select {
	case s := <-done:
		return s
	case <-time.After(5 * time.Second):
		t.Fatal("Stats did not complete")
		return storage.Stats{}
	}
}

func siblingSetOf(value string) *storage.SiblingSet {
	vc := vclock.NewVectorClock()
	vc.Set("node-a", 1)
	return &storage.SiblingSet{Siblings: []storage.Sibling{
		{Value: []byte(value), VClock: vc, Timestamp: time.Now().Unix()},
	}}
}

func TestStore_PutThenGetRoundTrip(t *testing.T) {
	dir := t.TempDir()
	rt := newTestRuntime(t)
	ts := openTestStore(t, rt, filepath.Join(dir, "wal.log"))

	if err := ts.put(t, []byte("key1"), siblingSetOf("v1")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got := ts.get(t, []byte("key1"))
	if got.err != nil {
		t.Fatalf("Get: %v", got.err)
	}
	if len(got.ss.Siblings) != 1 || string(got.ss.Siblings[0].Value) != "v1" {
		t.Fatalf("unexpected siblings: %+v", got.ss.Siblings)
	}
}

func TestStore_Get_MissingKeyReturnsErrKeyNotFound(t *testing.T) {
	dir := t.TempDir()
	rt := newTestRuntime(t)
	ts := openTestStore(t, rt, filepath.Join(dir, "wal.log"))

	got := ts.get(t, []byte("nope"))
	if !errors.Is(got.err, quorumerr.ErrKeyNotFound) {
		t.Fatalf("expected ErrKeyNotFound, got %v", got.err)
	}
}

func TestStore_DeleteThenGetReturnsErrKeyNotFound(t *testing.T) {
	dir := t.TempDir()
	rt := newTestRuntime(t)
	ts := openTestStore(t, rt, filepath.Join(dir, "wal.log"))

	if err := ts.put(t, []byte("key1"), siblingSetOf("v1")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	ctx := vclock.NewVectorClock()
	ctx.Set("node-a", 2)
	if err := ts.delete(t, []byte("key1"), ctx); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	got := ts.get(t, []byte("key1"))
	if !errors.Is(got.err, quorumerr.ErrKeyNotFound) {
		t.Fatalf("expected ErrKeyNotFound after Delete, got ss=%+v err=%v", got.ss, got.err)
	}

	// GetRaw must still surface the tombstoned record for read-repair /
	// anti-entropy, per storage.Storage's doc comment.
	raw := ts.getRaw(t, []byte("key1"))
	if raw.err != nil {
		t.Fatalf("GetRaw: %v", raw.err)
	}
	if len(raw.ss.Siblings) != 2 {
		t.Fatalf("expected the original sibling plus the tombstone to both be visible via GetRaw, got %+v", raw.ss.Siblings)
	}
	if !raw.ss.Siblings[len(raw.ss.Siblings)-1].Tombstone {
		t.Fatal("expected the last sibling to be the tombstone")
	}
}

func TestStore_Put_ReconcilesAsAppendUnion(t *testing.T) {
	dir := t.TempDir()
	rt := newTestRuntime(t)
	ts := openTestStore(t, rt, filepath.Join(dir, "wal.log"))

	if err := ts.put(t, []byte("key1"), siblingSetOf("v1")); err != nil {
		t.Fatalf("Put 1: %v", err)
	}
	if err := ts.put(t, []byte("key1"), siblingSetOf("v2")); err != nil {
		t.Fatalf("Put 2: %v", err)
	}

	raw := ts.getRaw(t, []byte("key1"))
	if raw.err != nil {
		t.Fatalf("GetRaw: %v", raw.err)
	}
	if len(raw.ss.Siblings) != 2 {
		t.Fatalf("expected a concurrent write's sibling to be preserved (union), got %+v", raw.ss.Siblings)
	}
	if string(raw.ss.Siblings[0].Value) != "v1" || string(raw.ss.Siblings[1].Value) != "v2" {
		t.Fatalf("unexpected sibling contents: %+v", raw.ss.Siblings)
	}
}

func TestStore_ScanVisitsKeysInOrderAndHonorsEarlyStop(t *testing.T) {
	dir := t.TempDir()
	rt := newTestRuntime(t)
	ts := openTestStore(t, rt, filepath.Join(dir, "wal.log"))

	for _, k := range []string{"delta", "bravo", "charlie", "alpha"} {
		if err := ts.put(t, []byte(k), siblingSetOf(k)); err != nil {
			t.Fatalf("Put %q: %v", k, err)
		}
	}

	var visited []string
	if err := ts.scan(t, nil, nil, func(key []byte, ss *storage.SiblingSet) bool {
		visited = append(visited, string(key))
		return true
	}); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	want := []string{"alpha", "bravo", "charlie", "delta"}
	if len(visited) != len(want) {
		t.Fatalf("visited = %v, want %v", visited, want)
	}
	for i := range want {
		if visited[i] != want[i] {
			t.Fatalf("visited = %v, want %v", visited, want)
		}
	}

	visited = nil
	if err := ts.scan(t, nil, nil, func(key []byte, ss *storage.SiblingSet) bool {
		visited = append(visited, string(key))
		return len(visited) < 2
	}); err != nil {
		t.Fatalf("Scan (early stop): %v", err)
	}
	if len(visited) != 2 || visited[0] != "alpha" || visited[1] != "bravo" {
		t.Fatalf("expected scan to stop after 2 keys (alpha, bravo), got %v", visited)
	}

	visited = nil
	if err := ts.scan(t, []byte("bravo"), []byte("delta"), func(key []byte, ss *storage.SiblingSet) bool {
		visited = append(visited, string(key))
		return true
	}); err != nil {
		t.Fatalf("Scan (bounded): %v", err)
	}
	if len(visited) != 2 || visited[0] != "bravo" || visited[1] != "charlie" {
		t.Fatalf("expected [bravo charlie] for range [bravo, delta), got %v", visited)
	}
}

func TestStore_ReopenRecoversPreviousWrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wal.log")
	rt := newTestRuntime(t)

	// Phase 1: write data through one Store/reactor pair, then fully tear
	// it down (as if the process restarted).
	store1, err := Open(rt, Options{Path: path, NodeID: "node-a"})
	if err != nil {
		t.Fatalf("Open (1st): %v", err)
	}
	r1 := reactor.New(rt)
	r1.SetEventHandler(store1.HandleCompletion)
	errCh1 := make(chan error, 1)
	go func() { errCh1 <- r1.Run() }()

	putDone := make(chan error, 1)
	r1.PostFunc(func() {
		store1.Put([]byte("durable-key"), siblingSetOf("persisted"), func(err error) { putDone <- err })
	})
	select {
	case err := <-putDone:
		if err != nil {
			t.Fatalf("Put: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Put did not complete")
	}

	r1.RequestStop()
	select {
	case err := <-errCh1:
		if err != nil {
			t.Fatalf("reactor1 Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reactor1 did not stop")
	}
	if err := store1.Close(); err != nil {
		t.Fatalf("store1.Close: %v", err)
	}

	// Phase 2: reopen the same file (a fresh Open call), proving Replay
	// recovers the previously written data.
	ts2 := openTestStore(t, rt, path)
	got := ts2.get(t, []byte("durable-key"))
	if got.err != nil {
		t.Fatalf("Get after reopen: %v", got.err)
	}
	if len(got.ss.Siblings) != 1 || string(got.ss.Siblings[0].Value) != "persisted" {
		t.Fatalf("unexpected recovered siblings: %+v", got.ss.Siblings)
	}
}

func TestStore_TruncatedFileRecoversValidPrefixOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wal.log")
	rt := newTestRuntime(t)

	store1, err := Open(rt, Options{Path: path, NodeID: "node-a"})
	if err != nil {
		t.Fatalf("Open (1st): %v", err)
	}
	r1 := reactor.New(rt)
	r1.SetEventHandler(store1.HandleCompletion)
	errCh1 := make(chan error, 1)
	go func() { errCh1 <- r1.Run() }()

	put := func(key, value string) {
		done := make(chan error, 1)
		r1.PostFunc(func() {
			store1.Put([]byte(key), siblingSetOf(value), func(err error) { done <- err })
		})
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("Put %q: %v", key, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("Put %q did not complete", key)
		}
	}
	put("a", "v1")
	statsAfterA := func() storage.Stats {
		done := make(chan storage.Stats, 1)
		r1.PostFunc(func() { done <- store1.Stats() })
		return <-done
	}()
	put("b", "v2")
	statsAfterB := func() storage.Stats {
		done := make(chan storage.Stats, 1)
		r1.PostFunc(func() { done <- store1.Stats() })
		return <-done
	}()

	r1.RequestStop()
	select {
	case err := <-errCh1:
		if err != nil {
			t.Fatalf("reactor1 Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reactor1 did not stop")
	}
	if err := store1.Close(); err != nil {
		t.Fatalf("store1.Close: %v", err)
	}

	// Tear the second record in half, simulating a crash mid-write.
	validTail := int64(statsAfterA.WALBytesWritten)
	secondRecordLen := int64(statsAfterB.WALBytesWritten) - validTail
	if secondRecordLen <= 1 {
		t.Fatalf("expected the second record to be more than 1 byte, got %d", secondRecordLen)
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	if err := f.Truncate(validTail + secondRecordLen/2); err != nil {
		t.Fatalf("Truncate: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopen: replay must recover key "a" and silently discard the torn
	// write for key "b", without returning an error.
	ts2 := openTestStore(t, rt, path)
	gotA := ts2.get(t, []byte("a"))
	if gotA.err != nil {
		t.Fatalf("Get(a) after reopen: %v", gotA.err)
	}
	if len(gotA.ss.Siblings) != 1 || string(gotA.ss.Siblings[0].Value) != "v1" {
		t.Fatalf("unexpected siblings for a: %+v", gotA.ss.Siblings)
	}

	gotB := ts2.get(t, []byte("b"))
	if !errors.Is(gotB.err, quorumerr.ErrKeyNotFound) {
		t.Fatalf("expected b (torn write) to be absent, got ss=%+v err=%v", gotB.ss, gotB.err)
	}
}
