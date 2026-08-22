package journal

import (
	"bytes"
	"errors"
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
}

type readRes struct {
	val []byte
	err error
}

type scanRes struct {
	items []ScanEntry
	err   error
}

func openTestStore(t testing.TB, rt *ioruntime.Runtime, path string) *testStore {
	t.Helper()
	store, err := Open(rt, Options{Path: path})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	ts := &testStore{
		store:  store,
		reads:  make(map[uint64]chan readRes),
		writes: make(map[uint64]chan error),
		scans:  make(map[uint64]chan scanRes),
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
