package vclock

// Ordering represents the causal relationship between two vector clocks.
type Ordering int

const (
	Before     Ordering = iota // vc1 happens before vc2.
	After                      // vc1 happens after vc2.
	Equal                      // vc1 and vc2 are identical.
	Concurrent                 // Neither dominates (conflict).
)

// String returns the human-readable name of the ordering.
func (o Ordering) String() string {
	switch o {
	case Before:
		return "BEFORE"
	case After:
		return "AFTER"
	case Equal:
		return "EQUAL"
	case Concurrent:
		return "CONCURRENT"
	default:
		return "UNKNOWN"
	}
}
