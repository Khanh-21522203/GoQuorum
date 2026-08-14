package antientropy

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"

	"goquorum.io/v2/engine/storage"
)

// hashSize is the width of every leaf and internal node hash (SHA-256).
const hashSize = sha256.Size

// BucketRange is a contiguous range of diverging leaf buckets found by
// comparing two Merkle trees.
type BucketRange struct {
	Start int
	End   int
}

// MerkleTree is a binary hash tree over the local keyspace, partitioned
// into 2^depth leaf buckets, used to find diverging key ranges between
// replicas without transferring the whole keyspace.
//
// nodeHashes stores every internal node in a single flat, breadth-first
// array: the node at tree level L, position i, lives at index
// (1<<L)-1+i. This avoids pointer-chasing a tree of *node structs and
// keeps rebuildTree a simple bottom-up sweep over slices.
//
// A MerkleTree is not safe for concurrent use. Callers must confine it to
// a single goroutine.
type MerkleTree struct {
	depth      int
	numBuckets int
	leafHashes [][]byte
	nodeHashes [][]byte
	dirty      []bool
}

// NewMerkleTree allocates a tree with 2^depth leaf buckets, all initially
// hashing to the zero value.
func NewMerkleTree(depth int) *MerkleTree {
	numBuckets := 1 << depth

	leafHashes := make([][]byte, numBuckets)
	for i := range leafHashes {
		leafHashes[i] = make([]byte, hashSize)
	}

	mt := &MerkleTree{
		depth:      depth,
		numBuckets: numBuckets,
		leafHashes: leafHashes,
		nodeHashes: make([][]byte, numBuckets-1),
		dirty:      make([]bool, numBuckets),
	}
	mt.rebuildTree()
	return mt
}

// Build resets every leaf bucket and repopulates the tree from a full
// keyspace scan. store.Scan is callback-based, but Build's own contract is
// synchronous: the done callback below only ever captures the terminal
// error into scanErr, which Build reads immediately after Scan returns
// (Scan does not return until it has invoked done exactly once).
func (mt *MerkleTree) Build(store storage.Storage) error {
	for i := 0; i < mt.numBuckets; i++ {
		mt.leafHashes[i] = make([]byte, hashSize)
		mt.dirty[i] = true
	}

	var scanErr error
	store.Scan(nil, nil, func(key []byte, siblings *storage.SiblingSet) bool {
		mt.toggleKey(key, siblings)
		return true
	}, func(err error) {
		scanErr = err
	})
	if scanErr != nil {
		return scanErr
	}

	mt.rebuildTree()
	return nil
}

// toggleKey folds (or, applied twice with the same siblings, un-folds) a
// key's contribution into its bucket. XOR is its own inverse, so applying
// the identical hash twice cancels out — this is what lets RemoveKey undo
// a prior UpdateKey without knowing which other keys share the bucket.
func (mt *MerkleTree) toggleKey(key []byte, siblings *storage.SiblingSet) {
	if siblings == nil {
		return
	}

	bucket := mt.keyToBucket(key)
	h := sha256.New()
	h.Write(key)
	for _, sib := range siblings.Siblings {
		// MarshalBinary gives a deterministic, sorted-by-node-ID encoding
		// of the vector clock, so two siblings with the same causal state
		// always fold in identical bytes regardless of map iteration order.
		vcBytes, _ := sib.VClock.MarshalBinary()
		h.Write(vcBytes)
		h.Write(sib.Value)
	}
	hash := h.Sum(nil)

	leaf := mt.leafHashes[bucket]
	for i := 0; i < hashSize; i++ {
		leaf[i] ^= hash[i]
	}
	mt.dirty[bucket] = true
}

// UpdateKey incrementally folds key's current siblings into its bucket,
// marking the bucket dirty so the next read rebuilds ancestor hashes.
func (mt *MerkleTree) UpdateKey(key []byte, siblings *storage.SiblingSet) {
	mt.toggleKey(key, siblings)
}

// RemoveKey removes key's prior contribution from its bucket. oldSiblings
// must be exactly what was last passed to UpdateKey for this key, since
// the toggle is a symmetric XOR, not a true delete.
func (mt *MerkleTree) RemoveKey(key []byte, oldSiblings *storage.SiblingSet) {
	mt.toggleKey(key, oldSiblings)
}

// GetRoot returns the tree's current root hash, rebuilding first if any
// bucket is dirty.
func (mt *MerkleTree) GetRoot() []byte {
	mt.rebuildIfNeeded()
	return mt.rootHash()
}

// rootHash returns the current root without checking dirty state. A
// depth-0 tree has no internal nodes at all, so its root is defined as the
// zero hash.
func (mt *MerkleTree) rootHash() []byte {
	if len(mt.nodeHashes) == 0 {
		return make([]byte, hashSize)
	}
	return mt.nodeHashes[0]
}

// GetLevel returns all node hashes at the given tree level, where level 0
// is the root and level == depth is the leaf level.
func (mt *MerkleTree) GetLevel(level int) [][]byte {
	mt.rebuildIfNeeded()

	if level == mt.depth {
		return mt.leafHashes
	}

	nodesAtLevel := 1 << level
	startIdx := nodesAtLevel - 1

	result := make([][]byte, nodesAtLevel)
	for i := 0; i < nodesAtLevel; i++ {
		result[i] = mt.nodeHashes[startIdx+i]
	}
	return result
}

// keyToBucket maps a key to one of the 2^depth leaf buckets by hashing it
// and reducing the top 64 bits modulo numBuckets, giving a near-uniform
// distribution independent of any structure in the raw key bytes.
func (mt *MerkleTree) keyToBucket(key []byte) int {
	h := sha256.Sum256(key)
	hash := binary.BigEndian.Uint64(h[:8])
	return int(hash % uint64(mt.numBuckets))
}

// rebuildIfNeeded recomputes internal nodes only if some bucket changed
// since the last rebuild, so repeated reads between writes are free.
func (mt *MerkleTree) rebuildIfNeeded() {
	for _, d := range mt.dirty {
		if d {
			mt.rebuildTree()
			return
		}
	}
}

// rebuildTree recomputes every internal node hash bottom-up: each parent
// is SHA-256(left child || right child), starting from the leaves and
// finishing at the root.
func (mt *MerkleTree) rebuildTree() {
	for level := mt.depth - 1; level >= 0; level-- {
		nodesAtLevel := 1 << level
		nodesAtNextLevel := 1 << (level + 1)

		for i := 0; i < nodesAtLevel; i++ {
			leftIdx := 2 * i
			rightIdx := 2*i + 1

			var leftHash, rightHash []byte
			if level == mt.depth-1 {
				leftHash = mt.leafHashes[leftIdx]
				rightHash = mt.leafHashes[rightIdx]
			} else {
				childStartIdx := nodesAtNextLevel - 1
				leftHash = mt.nodeHashes[childStartIdx+leftIdx]
				rightHash = mt.nodeHashes[childStartIdx+rightIdx]
			}

			h := sha256.New()
			h.Write(leftHash)
			h.Write(rightHash)

			nodeIdx := nodesAtLevel - 1 + i
			mt.nodeHashes[nodeIdx] = h.Sum(nil)
		}
	}

	for i := range mt.dirty {
		mt.dirty[i] = false
	}
}

// Compare returns the bucket ranges where mt and other diverge. Trees of
// different depth are treated as fully diverged, since their bucket
// boundaries are not comparable.
func (mt *MerkleTree) Compare(other *MerkleTree) []BucketRange {
	if mt.depth != other.depth {
		return []BucketRange{{Start: 0, End: mt.numBuckets}}
	}

	mt.rebuildIfNeeded()
	other.rebuildIfNeeded()

	if bytes.Equal(mt.rootHash(), other.rootHash()) {
		return nil
	}

	return mt.findDifferences(other, 0, 0, mt.numBuckets)
}

// findDifferences recursively descends both trees in lockstep, comparing
// the left and right child of the current [start, end) range and only
// recursing into children whose hashes disagree. This prunes whole
// matching subtrees in O(log numBuckets) per divergence instead of
// walking every bucket.
func (mt *MerkleTree) findDifferences(other *MerkleTree, level, start, end int) []BucketRange {
	if level == mt.depth {
		return []BucketRange{{Start: start, End: end}}
	}

	mid := (start + end) / 2

	var leftHash, otherLeftHash, rightHash, otherRightHash []byte
	if level == mt.depth-1 {
		leftHash = mt.leafHashes[start]
		otherLeftHash = other.leafHashes[start]
		rightHash = mt.leafHashes[mid]
		otherRightHash = other.leafHashes[mid]
	} else {
		// The left child of the current node covers [start, mid) and lives
		// one level deeper; its flat index is derived from how many
		// buckets each node at that level spans.
		leftIdx := (1 << (level + 1)) - 1 + start/(mt.numBuckets>>(level+1))
		rightIdx := leftIdx + 1
		leftHash = mt.nodeHashes[leftIdx]
		otherLeftHash = other.nodeHashes[leftIdx]
		rightHash = mt.nodeHashes[rightIdx]
		otherRightHash = other.nodeHashes[rightIdx]
	}

	var diffs []BucketRange
	if !bytes.Equal(leftHash, otherLeftHash) {
		diffs = append(diffs, mt.findDifferences(other, level+1, start, mid)...)
	}
	if !bytes.Equal(rightHash, otherRightHash) {
		diffs = append(diffs, mt.findDifferences(other, level+1, mid, end)...)
	}
	return diffs
}
