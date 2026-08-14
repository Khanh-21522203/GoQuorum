package membership

// NodeStatus represents the membership status of a node as tracked by the
// MembershipManager.
//
// (v1: internal/cluster/membership.go NodeStatus)
type NodeStatus int

const (
	NodeStatusUnknown NodeStatus = iota
	NodeStatusJoining
	NodeStatusActive
	NodeStatusSuspect
	NodeStatusFailed
	NodeStatusLeaving
)

// String returns the human-readable name of the status.
func (s NodeStatus) String() string {
	switch s {
	case NodeStatusUnknown:
		return "UNKNOWN"
	case NodeStatusJoining:
		return "JOINING"
	case NodeStatusActive:
		return "ACTIVE"
	case NodeStatusSuspect:
		return "SUSPECT"
	case NodeStatusFailed:
		return "FAILED"
	case NodeStatusLeaving:
		return "LEAVING"
	default:
		return "INVALID"
	}
}
