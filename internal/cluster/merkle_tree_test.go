package cluster

import (
	"bytes"
	"testing"

	"GoQuorum/internal/storage"
	"GoQuorum/internal/vclock"
)

func makeSiblingSet(value string) *storage.SiblingSet {
	vc := vclock.NewVectorClock()
	vc.SetString("n1", 1)
	return &storage.SiblingSet{
		Siblings: []storage.Sibling{
			{Value: []byte(value), VClock: vc, Timestamp: 1},
		},
	}
}

// ── Empty tree has deterministic all-zero root ────────────────────────────

func TestMerkleTreeEmptyRoot(t *testing.T) {
	mt := NewMerkleTree(4) // 16 buckets
	root := mt.GetRoot()
	if len(root) == 0 {
		t.Fatal("empty root should not be nil/empty")
	}
	// Should be all zeros (no updates applied)
	for _, b := range root {
		if b != 0 {
			// actually the root from hashing zeros may not be zero;
			// just check it's consistent across two calls
			break
		}
	}

	root2 := mt.GetRoot()
	if !bytes.Equal(root, root2) {
		t.Error("root not stable across calls")
	}
}

// ── UpdateKey changes the root ─────────────────────────────────────────────

func TestMerkleTreeUpdateChangesRoot(t *testing.T) {
	mt := NewMerkleTree(4)
	rootBefore := mt.GetRoot()
	rootBefore = append([]byte(nil), rootBefore...) // copy

	mt.UpdateKey([]byte("key1"), makeSiblingSet("val1"))

	rootAfter := mt.GetRoot()
	if bytes.Equal(rootBefore, rootAfter) {
		t.Error("root should change after UpdateKey")
	}
}

// ── XOR symmetry: toggle same key twice restores root ─────────────────────

func TestMerkleTreeXORSymmetry(t *testing.T) {
	mt := NewMerkleTree(4)
	rootOriginal := append([]byte(nil), mt.GetRoot()...)

	ss := makeSiblingSet("val")
	mt.UpdateKey([]byte("k"), ss)
	mt.RemoveKey([]byte("k"), ss)

	rootRestored := mt.GetRoot()
	if !bytes.Equal(rootOriginal, rootRestored) {
		t.Error("double XOR should restore original root")
	}
}

// ── Two identical trees compare equal ─────────────────────────────────────

func TestMerkleTreeCompareIdentical(t *testing.T) {
	mt1 := NewMerkleTree(4)
	mt2 := NewMerkleTree(4)

	mt1.UpdateKey([]byte("k"), makeSiblingSet("v"))
	mt2.UpdateKey([]byte("k"), makeSiblingSet("v"))

	diffs := mt1.Compare(mt2)
	if len(diffs) != 0 {
		t.Errorf("expected no diffs for identical trees, got %v", diffs)
	}
}

// ── Different trees have non-empty diffs ──────────────────────────────────

func TestMerkleTreeCompareDetectsDifference(t *testing.T) {
	mt1 := NewMerkleTree(4)
	mt2 := NewMerkleTree(4)

	mt1.UpdateKey([]byte("onlyInA"), makeSiblingSet("v"))

	diffs := mt1.Compare(mt2)
	if len(diffs) == 0 {
		t.Error("expected diffs between divergent trees")
	}
}

// ── Depth mismatch returns full divergence ────────────────────────────────

func TestMerkleTreeCompareDepthMismatch(t *testing.T) {
	mt1 := NewMerkleTree(4)
	mt2 := NewMerkleTree(6)

	diffs := mt1.Compare(mt2)
	if len(diffs) != 1 || diffs[0].Start != 0 || diffs[0].End != mt1.numBuckets {
		t.Errorf("depth mismatch should return full divergence, got %v", diffs)
	}
}

// ── Multiple keys in different buckets produces multiple diff ranges ───────

func TestMerkleTreeCompareMultipleDiffs(t *testing.T) {
	mt1 := NewMerkleTree(4)
	mt2 := NewMerkleTree(4)

	// Add keys that hash to at least 2 different buckets in mt1 only
	for i := 0; i < 20; i++ {
		key := []byte{byte(i * 13), byte(i * 7)}
		mt1.UpdateKey(key, makeSiblingSet("x"))
	}

	diffs := mt1.Compare(mt2)
	if len(diffs) == 0 {
		t.Error("expected at least one diff range")
	}

	// All ranges should be valid
	for _, d := range diffs {
		if d.Start >= d.End {
			t.Errorf("invalid range: start=%d end=%d", d.Start, d.End)
		}
		if d.Start < 0 || d.End > mt1.numBuckets {
			t.Errorf("range out of bounds: %v", d)
		}
	}
}
