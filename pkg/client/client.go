package client

import (
	"GoQuorum/internal/vclock"
)

type ConflictResolver interface {
	Resolve(siblings []Sibling) ([]byte, vclock.VectorClock, error)
}

type LWWResolver struct{}

func (r *LWWResolver) Resolve(siblings []Sibling) ([]byte, vclock.VectorClock, error) {
	// Implement last-write-wins
	// Merge all contexts
	mergedContext := vclock.NewVectorClock()
	for _, s := range siblings {
		mergedContext.Merge(s.Context)
	}

	// Pick latest by some criteria
	latest := findLatest(siblings)

	return latest.Value, mergedContext, nil
}
