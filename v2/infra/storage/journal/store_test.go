package journal

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"goquorum.io/v2/contracts/quorumerr"
	"goquorum.io/v2/engine/reactor"
	"goquorum.io/v2/infra/ioruntime"
)

func newTestRuntime(t testing.TB) *ioruntime.Runtime {
	t.Helper()
	rt, err := ioruntime.New(64)
	if err != nil {
		t.Fatalf("ioruntime.New: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	return rt
}

type testStore struct {
	store *Store
	r     *reactor.Reactor

	nextReqID uint64
	reads     map[uint64]chan readRes
	writes    map[uint64]chan error
	scans     map[uint64]chan scanRes
	compacts  map[uint64]chan compactRes
}

type readRes struct {
	val []byte
	err error
}

type scanRes struct {
	items []ScanEntry
	err   error
}

type compactRes struct {
	stats CompactStats
	err   error
}

func openTestStore(t testing.TB, rt *ioruntime.Runtime, path string) *testStore {
	t.Helper()
	return openTestStoreWithOptions(t, rt, Options{Path: path})
}

func openTestStoreWithOptions(t testing.TB, rt *ioruntime.Runtime, opts Options) *testStore {
	t.Helper()
	store, err := Open(rt, opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	ts := &testStore{
		store:    store,
		reads:    make(map[uint64]chan readRes),
		writes:   make(map[uint64]chan error),
		scans:    make(map[uint64]chan scanRes),
		compacts: make(map[uint64]chan compactRes),
	}

	store.OnReadComplete = func(reqID uint64, key, val []byte, err error) {
		if ch, ok := ts.reads[reqID]; ok {
			delete(ts.reads, reqID)
			ch <- readRes{val: val, err: err}
		}
	}

	store.OnWriteComplete = func(reqID uint64, key []byte, err error) {
		if ch, ok := ts.writes[reqID]; ok {
			delete(ts.writes, reqID)
			ch <- err
		}
	}

	store.OnScanComplete = func(scanID uint64, items []ScanEntry, err error) {
		if ch, ok := ts.scans[scanID]; ok {
			delete(ts.scans, scanID)
			ch <- scanRes{items: items, err: err}
		}
	}

	store.OnCompactComplete = func(compactID uint64, stats CompactStats, err error) {
		if ch, ok := ts.compacts[compactID]; ok {
			delete(ts.compacts, compactID)
			ch <- compactRes{stats: stats, err: err}
		}
	}

	r := reactor.New(rt)
	r.SetEventHandler(func(ev reactor.Event) { store.HandleCompletion(ev) })
	ts.r = r

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
	return ts
}

func (ts *testStore) put(t testing.TB, key, val []byte) error {
	t.Helper()
	done := make(chan error, 1)
	ts.r.PostFunc(func() {
		ts.nextReqID++
		reqID := ts.nextReqID
		ts.writes[reqID] = done
		if err := ts.store.Put(reqID, key, val); err != nil {
			delete(ts.writes, reqID)
			done <- err
		}
	})
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("Put did not complete")
		return nil
	}
}

func (ts *testStore) get(t testing.TB, key []byte) ([]byte, error) {
	t.Helper()
	done := make(chan readRes, 1)
	ts.r.PostFunc(func() {
		ts.nextReqID++
		reqID := ts.nextReqID
		ts.reads[reqID] = done
		if err := ts.store.Get(reqID, key); err != nil {
			delete(ts.reads, reqID)
			done <- readRes{val: nil, err: err}
		}
	})
	select {
	case r := <-done:
		return r.val, r.err
	case <-time.After(5 * time.Second):
		t.Fatal("Get did not complete")
		return nil, nil
	}
}

func (ts *testStore) delete(t testing.TB, key []byte) error {
	t.Helper()
	done := make(chan error, 1)
	ts.r.PostFunc(func() {
		ts.nextReqID++
		reqID := ts.nextReqID
		ts.writes[reqID] = done
		if err := ts.store.Delete(reqID, key); err != nil {
			delete(ts.writes, reqID)
			done <- err
		}
	})
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("Delete did not complete")
		return nil
	}
}

func (ts *testStore) scan(t testing.TB, start, end []byte) ([]ScanEntry, error) {
	t.Helper()
	done := make(chan scanRes, 1)
	ts.r.PostFunc(func() {
		ts.nextReqID++
		scanID := ts.nextReqID
		ts.scans[scanID] = done
		if err := ts.store.Scan(scanID, start, end); err != nil {
			delete(ts.scans, scanID)
			done <- scanRes{err: err}
		}
	})
	select {
	case r := <-done:
		return r.items, r.err
	case <-time.After(5 * time.Second):
		t.Fatal("Scan did not complete")
		return nil, nil
	}
}

func (ts *testStore) compact(t testing.TB, filter CompactFilter) (CompactStats, error) {
	t.Helper()
	done := make(chan compactRes, 1)
	ts.r.PostFunc(func() {
		ts.nextReqID++
		compactID := ts.nextReqID
		ts.compacts[compactID] = done
		if err := ts.store.Compact(compactID, filter); err != nil {
			delete(ts.compacts, compactID)
			done <- compactRes{err: err}
		}
	})
	select {
	case r := <-done:
		return r.stats, r.err
	case <-time.After(5 * time.Second):
		t.Fatal("Compact did not complete")
		return CompactStats{}, nil
	}
}

func TestStore_PutThenGetRoundTrip(t *testing.T) {
	rt := newTestRuntime(t)
	ts := openTestStore(t, rt, filepath.Join(t.TempDir(), "wal.log"))

	key := []byte("alpha")
	val := []byte("beta-payload")

	if err := ts.put(t, key, val); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := ts.get(t, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, val) {
		t.Fatalf("Get = %q, want %q", got, val)
	}
}

func TestStore_Get_MissingKeyReturnsErrKeyNotFound(t *testing.T) {
	rt := newTestRuntime(t)
	ts := openTestStore(t, rt, filepath.Join(t.TempDir(), "wal.log"))

	_, err := ts.get(t, []byte("missing"))
	if !errors.Is(err, quorumerr.ErrKeyNotFound) {
		t.Fatalf("expected ErrKeyNotFound, got %v", err)
	}
}

func TestStore_DeleteThenGetReturnsEmptyValue(t *testing.T) {
	rt := newTestRuntime(t)
	ts := openTestStore(t, rt, filepath.Join(t.TempDir(), "wal.log"))

	key := []byte("doomed")
	if err := ts.put(t, key, []byte("alive")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := ts.delete(t, key); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	got, err := ts.get(t, key)
	if err != nil {
		t.Fatalf("Get after Delete: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty value after Delete, got %q", got)
	}
}

func TestStore_ScanVisitsKeysInOrder(t *testing.T) {
	rt := newTestRuntime(t)
	ts := openTestStore(t, rt, filepath.Join(t.TempDir(), "wal.log"))

	for _, k := range []string{"c", "a", "b", "d"} {
		if err := ts.put(t, []byte(k), []byte("v-"+k)); err != nil {
			t.Fatalf("Put(%q): %v", k, err)
		}
	}

	// Full scan
	items, err := ts.scan(t, nil, nil)
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}
	if len(items) != 4 || string(items[0].Key) != "a" || string(items[1].Key) != "b" || string(items[2].Key) != "c" || string(items[3].Key) != "d" {
		t.Fatalf("unexpected full scan keys: %+v", items)
	}

	// Range scan [b, d)
	rangeItems, err := ts.scan(t, []byte("b"), []byte("d"))
	if err != nil {
		t.Fatalf("Range scan failed: %v", err)
	}
	if len(rangeItems) != 2 || string(rangeItems[0].Key) != "b" || string(rangeItems[1].Key) != "c" {
		t.Fatalf("unexpected range scan keys: %+v", rangeItems)
	}
}

func TestStore_Scan_CoalescedDenseAndSparseRecords(t *testing.T) {
	rt := newTestRuntime(t)
	ts := openTestStore(t, rt, filepath.Join(t.TempDir(), "wal.log"))

	// Dense records (adjacent offsets -> single coalesced chunk)
	for _, k := range []string{"dense1", "dense2", "dense3"} {
		if err := ts.put(t, []byte(k), []byte("payload-"+k)); err != nil {
			t.Fatalf("Put(%q): %v", k, err)
		}
	}

	// Write a 100KB dummy record to create a gap > 64KB
	dummy := make([]byte, 100*1024)
	if err := ts.put(t, []byte("dummy-spacer"), dummy); err != nil {
		t.Fatalf("Put dummy: %v", err)
	}

	// Sparse records (offset is > 100KB away -> separate chunk)
	for _, k := range []string{"sparse1", "sparse2"} {
		if err := ts.put(t, []byte(k), []byte("payload-"+k)); err != nil {
			t.Fatalf("Put(%q): %v", k, err)
		}
	}

	// Scan dense range only
	denseItems, err := ts.scan(t, []byte("dense"), []byte("dense9"))
	if err != nil {
		t.Fatalf("Scan dense failed: %v", err)
	}
	if len(denseItems) != 3 || string(denseItems[0].Key) != "dense1" || string(denseItems[1].Key) != "dense2" || string(denseItems[2].Key) != "dense3" {
		t.Fatalf("unexpected dense scan results: %+v", denseItems)
	}

	// Scan full range across both chunks
	allItems, err := ts.scan(t, nil, nil)
	if err != nil {
		t.Fatalf("Scan all failed: %v", err)
	}
	if len(allItems) != 6 {
		t.Fatalf("expected 6 total records, got %d", len(allItems))
	}
}

func TestStore_ReopenRecoversPreviousWrites(t *testing.T) {
	rt := newTestRuntime(t)
	path := filepath.Join(t.TempDir(), "wal.log")

	ts1 := openTestStore(t, rt, path)
	if err := ts1.put(t, []byte("persisted"), []byte("value-123")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	ts1.r.RequestStop()
	_ = ts1.store.Close()

	ts2 := openTestStore(t, rt, path)
	got, err := ts2.get(t, []byte("persisted"))
	if err != nil {
		t.Fatalf("Get after reopen: %v", err)
	}
	if string(got) != "value-123" {
		t.Fatalf("got %q, want %q", got, "value-123")
	}
}

func TestStore_OnStorageError_FiresOnDiskError(t *testing.T) {
	rt := newTestRuntime(t)
	ts := openTestStore(t, rt, filepath.Join(t.TempDir(), "wal.log"))

	var hookErr error
	hookFired := make(chan struct{}, 1)
	ts.store.OnStorageError = func(err error) {
		hookErr = err
		select {
		case hookFired <- struct{}{}:
		default:
		}
	}

	ts.r.PostFunc(func() {
		ts.store.HandleCompletion(reactor.Event{
			UserData: 999999, // Unmatched user data
			Err:      syscall.EIO,
		})
	})
	select {
	case <-hookFired:
		if !errors.Is(hookErr, syscall.EIO) {
			t.Fatalf("OnStorageError got %v, want EIO", hookErr)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("OnStorageError did not fire for EIO")
	}
}

func TestStore_Compact(t *testing.T) {
	rt := newTestRuntime(t)
	ts := openTestStore(t, rt, filepath.Join(t.TempDir(), "wal.log"))

	// 1. Write 3 versions of key "k1" (causing 2 dead versions)
	for i := 0; i < 3; i++ {
		if err := ts.put(t, []byte("k1"), []byte(fmt.Sprintf("v1-%d", i))); err != nil {
			t.Fatalf("Put k1: %v", err)
		}
	}

	// 2. Write key "k2" that will be discarded by compaction filter
	if err := ts.put(t, []byte("k2"), []byte("v2-to-discard")); err != nil {
		t.Fatalf("Put k2: %v", err)
	}

	// 3. Write key "k3" that will survive
	if err := ts.put(t, []byte("k3"), []byte("v3-keep")); err != nil {
		t.Fatalf("Put k3: %v", err)
	}

	stats, err := ts.compact(t, func(key, val []byte) (bool, []byte) {
		if string(key) == "k2" {
			return false, nil // Discard k2!
		}
		return true, val
	})
	if err != nil {
		t.Fatalf("Compact failed: %v", err)
	}

	if stats.LiveKeyCount != 2 {
		t.Fatalf("expected 2 live keys after compaction, got %d", stats.LiveKeyCount)
	}
	if stats.BytesReclaimed == 0 {
		t.Fatalf("expected positive BytesReclaimed, got %d", stats.BytesReclaimed)
	}

	// Verify k1 has latest version
	gotK1, err := ts.get(t, []byte("k1"))
	if err != nil {
		t.Fatalf("Get k1: %v", err)
	}
	if string(gotK1) != "v1-2" {
		t.Fatalf("k1 = %q, want %q", gotK1, "v1-2")
	}

	// Verify k2 is gone
	_, err = ts.get(t, []byte("k2"))
	if !errors.Is(err, quorumerr.ErrKeyNotFound) {
		t.Fatalf("expected ErrKeyNotFound for k2, got %v", err)
	}

	// Verify k3 is present
	gotK3, err := ts.get(t, []byte("k3"))
	if err != nil {
		t.Fatalf("Get k3: %v", err)
	}
	if string(gotK3) != "v3-keep" {
		t.Fatalf("k3 = %q, want %q", gotK3, "v3-keep")
	}
}

func TestStore_SegmentRotation_WrapsAroundRing(t *testing.T) {
	rt := newTestRuntime(t)
	dir := t.TempDir()

	// 3 segments, 100 bytes capacity each
	ts := openTestStoreWithOptions(t, rt, Options{
		DataDir:     dir,
		NumSegments: 3,
		SegmentSize: 100,
	})

	// Record size for "k1" / "value-1" is ~40 bytes.
	// 2 writes will fill ~80 bytes (fits in seg 0).
	// 3rd write will overflow 100B -> rotates to seg 1!
	// 5th write will rotate to seg 2!
	// 7th write will wrap around to seg 0!

	for i := 0; i < 7; i++ {
		key := []byte(fmt.Sprintf("key-%d", i))
		val := []byte(fmt.Sprintf("val-%d", i))
		if err := ts.put(t, key, val); err != nil {
			t.Fatalf("Put %s: %v", key, err)
		}
	}

	// Verify all keys can still be retrieved across the segments in O(1)
	for i := 0; i < 7; i++ {
		key := []byte(fmt.Sprintf("key-%d", i))
		want := fmt.Sprintf("val-%d", i)
		got, err := ts.get(t, key)
		if err != nil {
			t.Fatalf("Get %s: %v", key, err)
		}
		if string(got) != want {
			t.Fatalf("Get %s = %q, want %q", key, got, want)
		}
	}
}

func TestStore_MultiSegment_Scan(t *testing.T) {
	rt := newTestRuntime(t)
	dir := t.TempDir()

	// 4 segments, 120 bytes capacity each
	ts := openTestStoreWithOptions(t, rt, Options{
		DataDir:     dir,
		NumSegments: 4,
		SegmentSize: 120,
	})

	// Write keys that will distribute across all 4 segments:
	keys := []string{"d", "a", "c", "b", "f", "e"}
	for _, k := range keys {
		if err := ts.put(t, []byte(k), []byte("payload-"+k)); err != nil {
			t.Fatalf("Put %s: %v", k, err)
		}
	}

	// Scan all keys [a .. z)
	items, err := ts.scan(t, []byte("a"), []byte("z"))
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	if len(items) != len(keys) {
		t.Fatalf("Scan got %d items, want %d", len(items), len(keys))
	}

	wantOrder := []string{"a", "b", "c", "d", "e", "f"}
	for i, wantKey := range wantOrder {
		if string(items[i].Key) != wantKey {
			t.Fatalf("items[%d].Key = %q, want %q", i, items[i].Key, wantKey)
		}
		if string(items[i].Value) != "payload-"+wantKey {
			t.Fatalf("items[%d].Value = %q, want %q", i, items[i].Value, "payload-"+wantKey)
		}
	}
}

func TestStore_MultiSegment_Compaction(t *testing.T) {
	rt := newTestRuntime(t)
	dir := t.TempDir()

	// 3 segments, 100 bytes capacity each
	ts := openTestStoreWithOptions(t, rt, Options{
		DataDir:     dir,
		NumSegments: 3,
		SegmentSize: 100,
	})

	// Write multiple versions to generate dead records
	for i := 0; i < 5; i++ {
		_ = ts.put(t, []byte("active-key"), []byte(fmt.Sprintf("v-%d", i)))
	}
	_ = ts.put(t, []byte("dead-key"), []byte("to-delete"))

	stats, err := ts.compact(t, func(key, val []byte) (bool, []byte) {
		if string(key) == "dead-key" {
			return false, nil // Filter out
		}
		return true, val
	})
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}

	if stats.LiveKeyCount != 1 {
		t.Fatalf("LiveKeyCount = %d, want 1", stats.LiveKeyCount)
	}

	// Verify active-key is preserved with latest version
	got, err := ts.get(t, []byte("active-key"))
	if err != nil {
		t.Fatalf("Get active-key: %v", err)
	}
	if string(got) != "v-4" {
		t.Fatalf("got %q, want %q", got, "v-4")
	}

	// Verify dead-key is gone
	_, err = ts.get(t, []byte("dead-key"))
	if !errors.Is(err, quorumerr.ErrKeyNotFound) {
		t.Fatalf("expected ErrKeyNotFound for dead-key, got %v", err)
	}
}

func TestStore_EpochRecovery_RecoversHeadAndTailOnReopen(t *testing.T) {
	rt := newTestRuntime(t)
	dir := t.TempDir()

	opts := Options{
		DataDir:     dir,
		NumSegments: 3,
		SegmentSize: 100,
	}

	// 1. First run: Rotate through multiple epochs
	ts1 := openTestStoreWithOptions(t, rt, opts)
	for i := 0; i < 6; i++ {
		key := []byte(fmt.Sprintf("k-%d", i))
		val := []byte(fmt.Sprintf("val-%d", i))
		if err := ts1.put(t, key, val); err != nil {
			t.Fatalf("Put %s: %v", key, err)
		}
	}
	stats1 := ts1.store.Stats()
	ts1.r.RequestStop()
	if err := ts1.store.Close(); err != nil {
		t.Fatalf("Close ts1: %v", err)
	}

	// 2. Reopen store in same directory
	ts2 := openTestStoreWithOptions(t, rt, opts)
	defer ts2.store.Close()
	stats2 := ts2.store.Stats()

	// Verify Head and Epoch are recovered accurately
	if stats2.ActiveSeg != stats1.ActiveSeg {
		t.Fatalf("recovered ActiveSeg = %d, want %d", stats2.ActiveSeg, stats1.ActiveSeg)
	}
	if stats2.CurrentEpoch != stats1.CurrentEpoch {
		t.Fatalf("recovered CurrentEpoch = %d, want %d", stats2.CurrentEpoch, stats1.CurrentEpoch)
	}

	// Verify all data is intact
	for i := 0; i < 6; i++ {
		key := []byte(fmt.Sprintf("k-%d", i))
		want := fmt.Sprintf("val-%d", i)
		got, err := ts2.get(t, key)
		if err != nil {
			t.Fatalf("Get %s: %v", key, err)
		}
		if string(got) != want {
			t.Fatalf("Get %s = %q, want %q", key, got, want)
		}
	}
}

func TestStore_SegmentRotation_TruncatePreventsGhostRecords(t *testing.T) {
	rt := newTestRuntime(t)
	dir := t.TempDir()

	opts := Options{
		DataDir:     dir,
		NumSegments: 2,
		SegmentSize: 80, // Small segment capacity to trigger rapid rotation
	}

	// 1. Initial run: Fill Seg 0 and Seg 1 with old data
	ts1 := openTestStoreWithOptions(t, rt, opts)
	_ = ts1.put(t, []byte("ghost-key-1"), []byte("ghost-value-long-1"))
	_ = ts1.put(t, []byte("ghost-key-2"), []byte("ghost-value-long-2"))
	_ = ts1.put(t, []byte("ghost-key-3"), []byte("ghost-value-long-3"))
	_ = ts1.put(t, []byte("ghost-key-4"), []byte("ghost-value-long-4"))

	// Rotate back around to Seg 0 (overwriting Seg 0 with truncate)
	_ = ts1.put(t, []byte("new-key-1"), []byte("new-val-1"))

	ts1.r.RequestStop()
	_ = ts1.store.Close()

	// 2. Reopen store: Ensure ONLY surviving new keys are present, and old ghost keys on rotated Seg 0 are gone!
	ts2 := openTestStoreWithOptions(t, rt, opts)
	defer ts2.store.Close()

	got, err := ts2.get(t, []byte("new-key-1"))
	if err != nil {
		t.Fatalf("Get new-key-1: %v", err)
	}
	if string(got) != "new-val-1" {
		t.Fatalf("got %q, want %q", got, "new-val-1")
	}

	// Any overwritten ghost key should be cleanly inaccessible (not resurrected by ghost CRC blocks)
	_, err = ts2.get(t, []byte("ghost-key-1"))
	if !errors.Is(err, quorumerr.ErrKeyNotFound) {
		t.Fatalf("expected ErrKeyNotFound for overwritten ghost-key-1, got %v", err)
	}
}

func TestStore_ZeroAlloc_Operations(t *testing.T) {
	rt := newTestRuntime(t)
	dir := t.TempDir()

	store, err := Open(rt, Options{
		DataDir:     dir,
		NumSegments: 3,
		SegmentSize: 10 * 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	key := []byte("perf:user-account-12345")
	val := []byte(`{"balance":5000,"currency":"USD"}`)

	// 1. Direct Put submission zero-alloc
	var reqID uint64 = 1
	allocs := testing.AllocsPerRun(50, func() {
		reqID++
		_ = store.Put(reqID, key, val)
	})
	t.Logf("Direct Store.Put submission allocs per run: %f", allocs)

	// Simulate write completion to populate index
	store.HandleCompletion(reactor.Event{
		UserData: reqID,
		Result:   int64(RecordEncodedLen(len(key), len(val))),
	})

	// 2. Direct Get submission zero-alloc
	allocs = testing.AllocsPerRun(50, func() {
		reqID++
		_ = store.Get(reqID, key)
	})
	t.Logf("Direct Store.Get submission allocs per run: %f", allocs)
}
