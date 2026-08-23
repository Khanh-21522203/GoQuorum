package antientropy

import (
	"bytes"
	"errors"
	"testing"

	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/contracts/vclock"
	"goquorum.io/v2/engine/adapter"
)

// sibling builds a single-sibling SiblingSet for a given node/counter/value,
// which is all toggleKey needs to fold a key into the tree.
func sibling(nodeID node.NodeID, counter uint64, value []byte) *adapter.SiblingSet {
	vc := vclock.NewVectorClock()
	vc.Set(nodeID, counter)
	return &adapter.SiblingSet{
		Siblings: []adapter.Sibling{{Value: value, VClock: vc}},
	}
}

func TestNewMerkleTree_EmptyRootStable(t *testing.T) {
	tests := []struct {
		name  string
		depth int
	}{
		{"depth-1", 1},
		{"depth-3", 3},
		{"depth-6", 6},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := NewMerkleTree(tt.depth)
			b := NewMerkleTree(tt.depth)

			rootA := a.GetRoot()
			rootB := b.GetRoot()

			if len(rootA) != hashSize {
				t.Fatalf("root length = %d, want %d", len(rootA), hashSize)
			}
			if !bytes.Equal(rootA, rootB) {
				t.Fatalf("two freshly created trees of the same depth produced different roots")
			}
			// Reading again must not change anything.
			if !bytes.Equal(rootA, a.GetRoot()) {
				t.Fatalf("GetRoot is not stable across repeated calls")
			}
		})
	}
}

func TestUpdateKey_ChangesRoot(t *testing.T) {
	mt := NewMerkleTree(4)
	before := append([]byte(nil), mt.GetRoot()...)

	mt.UpdateKey([]byte("key-1"), sibling("n1", 1, []byte("value-1")))
	after := mt.GetRoot()

	if bytes.Equal(before, after) {
		t.Fatalf("root did not change after UpdateKey")
	}
}

func TestUpdateKey_RemoveKey_RestoresRoot(t *testing.T) {
	mt := NewMerkleTree(4)
	original := append([]byte(nil), mt.GetRoot()...)

	ss := sibling("n1", 1, []byte("value-1"))
	key := []byte("key-1")

	mt.UpdateKey(key, ss)
	if bytes.Equal(original, mt.GetRoot()) {
		t.Fatalf("UpdateKey should have changed the root before RemoveKey runs")
	}

	mt.RemoveKey(key, ss)
	restored := mt.GetRoot()

	if !bytes.Equal(original, restored) {
		t.Fatalf("RemoveKey with the same siblings did not restore the original root:\n got  %x\n want %x", restored, original)
	}
}

func TestCompare_IdenticalTreesHaveNoDiff(t *testing.T) {
	depth := 4
	a := NewMerkleTree(depth)
	b := NewMerkleTree(depth)

	keys := [][]byte{[]byte("alpha"), []byte("bravo"), []byte("charlie")}
	for i, k := range keys {
		ss := sibling(node.NodeID("n1"), uint64(i+1), []byte("v"))
		a.UpdateKey(k, ss)
		b.UpdateKey(k, ss)
	}

	if diffs := a.Compare(b); diffs != nil {
		t.Fatalf("Compare on identical trees returned %v, want none", diffs)
	}
}

func TestCompare_SingleBucketDiff(t *testing.T) {
	depth := 3 // 8 buckets, small enough that findDifferences is easy to reason about.
	a := NewMerkleTree(depth)
	b := NewMerkleTree(depth)

	shared := []byte("shared-key")
	sharedSS := sibling(node.NodeID("n1"), 1, []byte("v"))
	a.UpdateKey(shared, sharedSS)
	b.UpdateKey(shared, sharedSS)

	// Only tree a receives this extra key, so exactly its bucket should diverge.
	extra := []byte("extra-key")
	extraSS := sibling(node.NodeID("n1"), 1, []byte("v"))
	a.UpdateKey(extra, extraSS)

	wantBucket := a.keyToBucket(extra)

	diffs := a.Compare(b)
	if len(diffs) != 1 {
		t.Fatalf("Compare returned %d bucket ranges, want exactly 1: %v", len(diffs), diffs)
	}
	got := diffs[0]
	if got.Start != wantBucket || got.End != wantBucket+1 {
		t.Fatalf("Compare returned range %+v, want {Start:%d End:%d}", got, wantBucket, wantBucket+1)
	}
}

func TestGetLevel_RootLevelHasExactlyOneHash(t *testing.T) {
	mt := NewMerkleTree(5)
	mt.UpdateKey([]byte("k"), sibling("n1", 1, []byte("v")))

	level0 := mt.GetLevel(0)
	if len(level0) != 1 {
		t.Fatalf("GetLevel(0) returned %d hashes, want 1", len(level0))
	}
	if !bytes.Equal(level0[0], mt.GetRoot()) {
		t.Fatalf("GetLevel(0)[0] does not match GetRoot()")
	}
}

// fakeStorage is a minimal adapter.Storage double whose only interesting
// method is Scan, driven synchronously by a fixed key/sibling-set fixture.
type fakeStorage struct {
	keys     [][]byte
	siblings []*adapter.SiblingSet
	scanErr  error
}

func (f *fakeStorage) Get(key []byte, done func(*adapter.SiblingSet, error))          { done(nil, nil) }
func (f *fakeStorage) GetRaw(key []byte, done func(*adapter.SiblingSet, error))       { done(nil, nil) }
func (f *fakeStorage) Put(key []byte, siblings *adapter.SiblingSet, done func(error)) { done(nil) }
func (f *fakeStorage) Delete(key []byte, ctx vclock.VectorClock, done func(error))    { done(nil) }
func (f *fakeStorage) LocalNodeID() node.NodeID                                       { return node.NodeID("fake-node") }
func (f *fakeStorage) Stats() adapter.StorageStats                                    { return adapter.StorageStats{} }
func (f *fakeStorage) Close() error                                                   { return nil }

func (f *fakeStorage) Scan(start, end []byte, fn adapter.ScanFunc, done func(error)) {
	for i, k := range f.keys {
		if !fn(k, f.siblings[i]) {
			break
		}
	}
	done(f.scanErr)
}

func TestBuild_MatchesManualUpdateKey(t *testing.T) {
	depth := 4
	fixtureKeys := [][]byte{
		[]byte("key-a"),
		[]byte("key-b"),
		[]byte("key-c"),
		[]byte("key-d"),
		[]byte("key-e"),
	}
	fixtureSiblings := []*adapter.SiblingSet{
		sibling("n1", 1, []byte("va")),
		sibling("n2", 1, []byte("vb")),
		sibling("n1", 2, []byte("vc")),
		sibling("n3", 5, []byte("vd")),
		sibling("n2", 3, []byte("ve")),
	}

	store := &fakeStorage{keys: fixtureKeys, siblings: fixtureSiblings}

	built := NewMerkleTree(depth)
	if err := built.Build(store); err != nil {
		t.Fatalf("Build returned unexpected error: %v", err)
	}

	manual := NewMerkleTree(depth)
	for i, k := range fixtureKeys {
		manual.UpdateKey(k, fixtureSiblings[i])
	}

	if !bytes.Equal(built.GetRoot(), manual.GetRoot()) {
		t.Fatalf("Build's root does not match manually applying the same UpdateKey calls:\n built  %x\n manual %x", built.GetRoot(), manual.GetRoot())
	}
}

func TestBuild_PropagatesScanError(t *testing.T) {
	wantErr := errors.New("scan failed")
	store := &fakeStorage{scanErr: wantErr}

	mt := NewMerkleTree(2)
	err := mt.Build(store)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Build error = %v, want %v", err, wantErr)
	}
}
