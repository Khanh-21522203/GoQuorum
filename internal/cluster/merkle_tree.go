package cluster

import (
	"GoQuorum/internal/storage"
	"crypto/sha256"
	"encoding/binary"
	"sync"
)

// MerkleTree implements Merkle tree for anti-entropy (Section 4.2)
type MerkleTree struct {
	mu         sync.Mutex // protects all fields below
	depth      int        // Tree depth (default: 10)
	numBuckets int        // Leaf buckets (2^depth)
	leafHashes [][]byte   // Leaf hashes
	nodeHashes [][]byte   // Internal node hashes
	dirty      []bool     // Dirty flags for incremental update
}

// NewMerkleTree creates a new Merkle tree
func NewMerkleTree(depth int) *MerkleTree {
	numBuckets := 1 << depth // 2^depth

	leafHashes := make([][]byte, numBuckets)
	for i := range leafHashes {
		leafHashes[i] = make([]byte, 32)
	}

	mt := &MerkleTree{
		depth:      depth,
		numBuckets: numBuckets,
		leafHashes: leafHashes,
		nodeHashes: make([][]byte, numBuckets-1), // Internal nodes
		dirty:      make([]bool, numBuckets),
	}
	mt.rebuildTree() // Establish consistent initial root
	return mt
}

// Build builds Merkle tree from storage (Section 4.3)
func (mt *MerkleTree) Build(store *storage.Storage) error {
	mt.mu.Lock()
	// Initialize leaf hashes
	for i := 0; i < mt.numBuckets; i++ {
		mt.leafHashes[i] = make([]byte, 32)
		mt.dirty[i] = true
	}
	mt.mu.Unlock()

	// Scan all keys using Storage.Scan
	err := store.Scan(nil, nil, func(key []byte, siblings *storage.SiblingSet) bool {
		if siblings != nil {
			mt.UpdateKey(key, siblings)
		}
		return true // Continue scanning
	})

	if err != nil {
		return err
	}

	// Build tree bottom-up
	mt.rebuildTree()

	return nil
}

func (mt *MerkleTree) toggleKey(key []byte, siblings *storage.SiblingSet) {
	if siblings == nil {
		return
	}

	bucket := mt.keyToBucket(key)
	h := sha256.New()
	h.Write(key)

	for _, sib := range siblings.Siblings {
		h.Write([]byte(sib.VClock.String()))
		h.Write(sib.Value)
	}

	hash := h.Sum(nil)
	for i := 0; i < 32; i++ {
		mt.leafHashes[bucket][i] ^= hash[i]
	}

	mt.dirty[bucket] = true
}

func (mt *MerkleTree) UpdateKey(key []byte, siblings *storage.SiblingSet) {
	mt.mu.Lock()
	defer mt.mu.Unlock()
	mt.toggleKey(key, siblings)
}

func (mt *MerkleTree) RemoveKey(key []byte, oldSiblings *storage.SiblingSet) {
	mt.mu.Lock()
	defer mt.mu.Unlock()
	mt.toggleKey(key, oldSiblings)
}

// GetRoot returns root hash
func (mt *MerkleTree) GetRoot() []byte {
	mt.mu.Lock()
	defer mt.mu.Unlock()
	mt.rebuildIfNeeded()

	if len(mt.nodeHashes) == 0 {
		return make([]byte, 32)
	}

	return mt.nodeHashes[0] // Root is first internal node
}

// GetLevel returns hashes at specific tree level
func (mt *MerkleTree) GetLevel(level int) [][]byte {
	mt.mu.Lock()
	defer mt.mu.Unlock()
	mt.rebuildIfNeeded()

	if level == mt.depth {
		// Leaf level
		return mt.leafHashes
	}

	// Internal level
	nodesAtLevel := 1 << level
	startIdx := (1 << level) - 1

	result := make([][]byte, nodesAtLevel)
	for i := 0; i < nodesAtLevel; i++ {
		result[i] = mt.nodeHashes[startIdx+i]
	}

	return result
}

// keyToBucket maps key to bucket index using SHA-256 for uniform distribution.
// Must be called with mt.mu held.
func (mt *MerkleTree) keyToBucket(key []byte) int {
	h := sha256.Sum256(key)
	hash := binary.BigEndian.Uint64(h[:8])
	return int(hash % uint64(mt.numBuckets))
}

// rebuildIfNeeded rebuilds tree if any bucket is dirty
func (mt *MerkleTree) rebuildIfNeeded() {
	for _, d := range mt.dirty {
		if d {
			mt.rebuildTree()
			return
		}
	}
}

// rebuildTree rebuilds internal nodes bottom-up (Section 4.3)
func (mt *MerkleTree) rebuildTree() {
	// Build tree level by level from leaves to root
	for level := mt.depth - 1; level >= 0; level-- {
		nodesAtLevel := 1 << level
		nodesAtNextLevel := 1 << (level + 1)

		for i := 0; i < nodesAtLevel; i++ {
			leftIdx := 2 * i
			rightIdx := 2*i + 1

			var leftHash, rightHash []byte

			if level == mt.depth-1 {
				// Children are leaves
				leftHash = mt.leafHashes[leftIdx]
				rightHash = mt.leafHashes[rightIdx]
			} else {
				// Children are internal nodes
				childStartIdx := nodesAtNextLevel - 1
				leftHash = mt.nodeHashes[childStartIdx+leftIdx]
				rightHash = mt.nodeHashes[childStartIdx+rightIdx]
			}

			// Hash parent
			h := sha256.New()
			h.Write(leftHash)
			h.Write(rightHash)

			nodeIdx := (1 << level) - 1 + i
			mt.nodeHashes[nodeIdx] = h.Sum(nil)
		}
	}

	// Clear dirty flags
	for i := range mt.dirty {
		mt.dirty[i] = false
	}
}

// Compare compares two Merkle trees and returns differing bucket ranges
func (mt *MerkleTree) Compare(other *MerkleTree) []BucketRange {
	mt.mu.Lock()
	defer mt.mu.Unlock()
	if mt.depth != other.depth {
		// Full divergence if depths differ
		return []BucketRange{{Start: 0, End: mt.numBuckets}}
	}

	// Compare root — call rebuildIfNeeded directly (no lock re-acquisition)
	mt.rebuildIfNeeded()
	other.mu.Lock()
	other.rebuildIfNeeded()
	other.mu.Unlock()

	myRoot := mt.nodeHashes[0]
	otherRoot := other.nodeHashes[0]
	if len(mt.nodeHashes) == 0 {
		myRoot = make([]byte, 32)
	}
	if len(other.nodeHashes) == 0 {
		otherRoot = make([]byte, 32)
	}

	if bytesEqual(myRoot, otherRoot) {
		return nil // Trees identical
	}

	// Traverse to find differing ranges
	return mt.findDifferences(other, 0, 0, mt.numBuckets)
}

// findDifferences recursively finds differing bucket ranges.
// At each level we compare the LEFT and RIGHT children of the current range.
func (mt *MerkleTree) findDifferences(
	other *MerkleTree,
	level int,
	start int,
	end int) []BucketRange {

	if level == mt.depth {
		// Leaf level — this bucket range is the divergent unit.
		return []BucketRange{{Start: start, End: end}}
	}

	mid := (start + end) / 2

	var leftHash, otherLeftHash, rightHash, otherRightHash []byte

	if level == mt.depth-1 {
		// Children are individual leaf buckets — compare leafHashes directly.
		leftHash = mt.leafHashes[start]
		otherLeftHash = other.leafHashes[start]
		rightHash = mt.leafHashes[mid]
		otherRightHash = other.leafHashes[mid]
	} else {
		// Children are internal nodes.
		// The left child covers [start, mid) and lives at nodeHashes level (level+1).
		// Child node index within level (level+1): start / (numBuckets >> (level+1))
		leftIdx := (1 << (level + 1)) - 1 + start/(mt.numBuckets>>(level+1))
		rightIdx := leftIdx + 1
		leftHash = mt.nodeHashes[leftIdx]
		otherLeftHash = other.nodeHashes[leftIdx]
		rightHash = mt.nodeHashes[rightIdx]
		otherRightHash = other.nodeHashes[rightIdx]
	}

	var diffs []BucketRange
	if !bytesEqual(leftHash, otherLeftHash) {
		diffs = append(diffs, mt.findDifferences(other, level+1, start, mid)...)
	}
	if !bytesEqual(rightHash, otherRightHash) {
		diffs = append(diffs, mt.findDifferences(other, level+1, mid, end)...)
	}
	return diffs
}

// BucketRange represents a range of buckets
type BucketRange struct {
	Start int
	End   int
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
