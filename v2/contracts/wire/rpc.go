package wire

import (
	"encoding/binary"
	"fmt"
	"math"
	"unsafe"

	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/contracts/quorumerr"
)

// MessageID identifies the kind of RPC body a frame carries.
type MessageID uint16

// Message IDs for the request/response pair of every engine/transport.Transport RPC.
const (
	MsgRemotePutRequest       MessageID = 1
	MsgRemotePutResponse      MessageID = 2
	MsgRemoteGetRequest       MessageID = 3
	MsgRemoteGetResponse      MessageID = 4
	MsgHeartbeatRequest       MessageID = 5
	MsgHeartbeatResponse      MessageID = 6
	MsgGetMerkleRootRequest   MessageID = 7
	MsgGetMerkleRootResponse  MessageID = 8
	MsgNotifyLeavingRequest   MessageID = 9
	MsgNotifyLeavingResponse  MessageID = 10
	MsgGossipExchangeRequest  MessageID = 11
	MsgGossipExchangeResponse MessageID = 12
)

// String returns the human-readable name of the message ID.
func (m MessageID) String() string {
	switch m {
	case MsgRemotePutRequest:
		return "RemotePutRequest"
	case MsgRemotePutResponse:
		return "RemotePutResponse"
	case MsgRemoteGetRequest:
		return "RemoteGetRequest"
	case MsgRemoteGetResponse:
		return "RemoteGetResponse"
	case MsgHeartbeatRequest:
		return "HeartbeatRequest"
	case MsgHeartbeatResponse:
		return "HeartbeatResponse"
	case MsgGetMerkleRootRequest:
		return "GetMerkleRootRequest"
	case MsgGetMerkleRootResponse:
		return "GetMerkleRootResponse"
	case MsgNotifyLeavingRequest:
		return "NotifyLeavingRequest"
	case MsgNotifyLeavingResponse:
		return "NotifyLeavingResponse"
	case MsgGossipExchangeRequest:
		return "GossipExchangeRequest"
	case MsgGossipExchangeResponse:
		return "GossipExchangeResponse"
	default:
		return "UNKNOWN"
	}
}

func appendUint32Prefixed(dst []byte, data []byte) []byte {
	dst = binary.BigEndian.AppendUint32(dst, uint32(len(data)))
	return append(dst, data...)
}

func decodeUint32Prefixed(data []byte) (value, rest []byte, err error) {
	if len(data) < 4 {
		return nil, nil, fmt.Errorf("%w: length prefix truncated", quorumerr.ErrCorruptedData)
	}
	n := binary.BigEndian.Uint32(data)
	data = data[4:]
	if uint64(n) > uint64(len(data)) {
		return nil, nil, fmt.Errorf("%w: length prefix exceeds remaining data", quorumerr.ErrCorruptedData)
	}
	return data[:n], data[n:], nil
}

func appendSiblingSet(dst []byte, ss *SiblingSet) ([]byte, error) {
	vcOffset := len(dst)
	dst = append(dst, 0, 0, 0, 0)
	var err error
	dst, err = ss.AppendMarshalBinary(dst)
	if err != nil {
		return nil, err
	}
	sibLen := uint32(len(dst) - (vcOffset + 4))
	binary.BigEndian.PutUint32(dst[vcOffset:], sibLen)
	return dst, nil
}

// RemotePutRequest carries a key and its full SiblingSet across the wire.
type RemotePutRequest struct {
	Key      []byte
	Siblings *SiblingSet
}

func (r RemotePutRequest) AppendMarshalBinary(dst []byte) ([]byte, error) {
	dst = appendUint32Prefixed(dst, r.Key)
	if r.Siblings == nil {
		var emptySib SiblingSet
		return appendSiblingSet(dst, &emptySib)
	}
	return appendSiblingSet(dst, r.Siblings)
}

func (r RemotePutRequest) Marshal() ([]byte, error) {
	dst := make([]byte, 0, len(r.Key)+64)
	return r.AppendMarshalBinary(dst)
}

func (r *RemotePutRequest) Unmarshal(data []byte) error {
	key, rest, err := decodeUint32Prefixed(data)
	if err != nil {
		return fmt.Errorf("%w: decoding key: %v", quorumerr.ErrCorruptedData, err)
	}
	sibBytes, _, err := decodeUint32Prefixed(rest)
	if err != nil {
		return fmt.Errorf("%w: decoding sibling bytes: %v", quorumerr.ErrCorruptedData, err)
	}
	r.Key = key
	if r.Siblings == nil {
		r.Siblings = &SiblingSet{}
	}
	return r.Siblings.UnmarshalBinary(sibBytes)
}

// RemotePutResponse carries the outcome of a RemotePut.
type RemotePutResponse struct {
	Status StatusCode
}

func (r RemotePutResponse) AppendMarshalBinary(dst []byte) ([]byte, error) {
	return binary.BigEndian.AppendUint16(dst, uint16(r.Status)), nil
}

func (r RemotePutResponse) Marshal() ([]byte, error) {
	return r.AppendMarshalBinary(make([]byte, 0, 2))
}

func (r *RemotePutResponse) Unmarshal(data []byte) error {
	if len(data) < 2 {
		return fmt.Errorf("%w: RemotePutResponse body truncated", quorumerr.ErrCorruptedData)
	}
	r.Status = StatusCode(binary.BigEndian.Uint16(data))
	return nil
}

// RemoteGetRequest carries the key to look up on a remote peer.
type RemoteGetRequest struct {
	Key []byte
}

func (r RemoteGetRequest) AppendMarshalBinary(dst []byte) ([]byte, error) {
	return appendUint32Prefixed(dst, r.Key), nil
}

func (r RemoteGetRequest) Marshal() ([]byte, error) {
	return r.AppendMarshalBinary(make([]byte, 0, len(r.Key)+4))
}

func (r *RemoteGetRequest) Unmarshal(data []byte) error {
	key, _, err := decodeUint32Prefixed(data)
	if err != nil {
		return fmt.Errorf("%w: decoding key: %v", quorumerr.ErrCorruptedData, err)
	}
	r.Key = key
	return nil
}

// RemoteGetResponse carries the status code and optional SiblingSet for a RemoteGet.
type RemoteGetResponse struct {
	Status   StatusCode
	Siblings *SiblingSet
}

func (r RemoteGetResponse) AppendMarshalBinary(dst []byte) ([]byte, error) {
	dst = binary.BigEndian.AppendUint16(dst, uint16(r.Status))
	if r.Status != StatusOK {
		return dst, nil
	}
	if r.Siblings == nil {
		var emptySib SiblingSet
		return appendSiblingSet(dst, &emptySib)
	}
	return appendSiblingSet(dst, r.Siblings)
}

func (r RemoteGetResponse) Marshal() ([]byte, error) {
	dst := make([]byte, 0, 64)
	return r.AppendMarshalBinary(dst)
}

func (r *RemoteGetResponse) Unmarshal(data []byte) error {
	if len(data) < 2 {
		return fmt.Errorf("%w: RemoteGetResponse body truncated", quorumerr.ErrCorruptedData)
	}
	r.Status = StatusCode(binary.BigEndian.Uint16(data))
	if r.Status != StatusOK {
		return nil
	}
	data = data[2:]
	if len(data) == 0 {
		return nil
	}

	sibBytes, _, err := decodeUint32Prefixed(data)
	if err != nil {
		return fmt.Errorf("%w: decoding sibling bytes: %v", quorumerr.ErrCorruptedData, err)
	}
	if r.Siblings == nil {
		r.Siblings = &SiblingSet{}
	}
	return r.Siblings.UnmarshalBinary(sibBytes)
}

// HeartbeatRequest carries an empty payload.
type HeartbeatRequest struct{}

func (HeartbeatRequest) AppendMarshalBinary(dst []byte) ([]byte, error) {
	return dst, nil
}

func (HeartbeatRequest) Marshal() ([]byte, error) {
	return nil, nil
}

func (*HeartbeatRequest) Unmarshal(data []byte) error {
	return nil
}

// HeartbeatResponse carries the node's health status.
type HeartbeatResponse struct {
	Status StatusCode
}

func (r HeartbeatResponse) AppendMarshalBinary(dst []byte) ([]byte, error) {
	return binary.BigEndian.AppendUint16(dst, uint16(r.Status)), nil
}

func (r HeartbeatResponse) Marshal() ([]byte, error) {
	return r.AppendMarshalBinary(make([]byte, 0, 2))
}

func (r *HeartbeatResponse) Unmarshal(data []byte) error {
	if len(data) < 2 {
		return fmt.Errorf("%w: HeartbeatResponse body truncated", quorumerr.ErrCorruptedData)
	}
	r.Status = StatusCode(binary.BigEndian.Uint16(data))
	return nil
}

// GetMerkleRootRequest carries an empty payload.
type GetMerkleRootRequest struct{}

func (GetMerkleRootRequest) AppendMarshalBinary(dst []byte) ([]byte, error) {
	return dst, nil
}

func (GetMerkleRootRequest) Marshal() ([]byte, error) {
	return nil, nil
}

func (*GetMerkleRootRequest) Unmarshal(data []byte) error {
	return nil
}

// GetMerkleRootResponse carries status and root hash.
type GetMerkleRootResponse struct {
	Status StatusCode
	Root   []byte
}

func (r GetMerkleRootResponse) AppendMarshalBinary(dst []byte) ([]byte, error) {
	dst = binary.BigEndian.AppendUint16(dst, uint16(r.Status))
	return appendUint32Prefixed(dst, r.Root), nil
}

func (r GetMerkleRootResponse) Marshal() ([]byte, error) {
	dst := make([]byte, 0, 2+len(r.Root)+4)
	return r.AppendMarshalBinary(dst)
}

func (r *GetMerkleRootResponse) Unmarshal(data []byte) error {
	if len(data) < 2 {
		return fmt.Errorf("%w: GetMerkleRootResponse body truncated", quorumerr.ErrCorruptedData)
	}
	r.Status = StatusCode(binary.BigEndian.Uint16(data))
	data = data[2:]
	root, _, err := decodeUint32Prefixed(data)
	if err != nil {
		return fmt.Errorf("%w: decoding root bytes: %v", quorumerr.ErrCorruptedData, err)
	}
	r.Root = root
	return nil
}

// NotifyLeavingRequest carries an empty payload.
type NotifyLeavingRequest struct{}

func (NotifyLeavingRequest) AppendMarshalBinary(dst []byte) ([]byte, error) {
	return dst, nil
}

func (NotifyLeavingRequest) Marshal() ([]byte, error) {
	return nil, nil
}

func (*NotifyLeavingRequest) Unmarshal(data []byte) error {
	return nil
}

// NotifyLeavingResponse carries an acknowledgment status.
type NotifyLeavingResponse struct {
	Status StatusCode
}

func (r NotifyLeavingResponse) AppendMarshalBinary(dst []byte) ([]byte, error) {
	return binary.BigEndian.AppendUint16(dst, uint16(r.Status)), nil
}

func (r NotifyLeavingResponse) Marshal() ([]byte, error) {
	return r.AppendMarshalBinary(make([]byte, 0, 2))
}

func (r *NotifyLeavingResponse) Unmarshal(data []byte) error {
	if len(data) < 2 {
		return fmt.Errorf("%w: NotifyLeavingResponse body truncated", quorumerr.ErrCorruptedData)
	}
	r.Status = StatusCode(binary.BigEndian.Uint16(data))
	return nil
}

// GossipExchangeRequest carries a slice of GossipEntry.
type GossipExchangeRequest struct {
	Entries []GossipEntry
}

func (r GossipExchangeRequest) AppendMarshalBinary(dst []byte) ([]byte, error) {
	return appendGossipEntries(dst, r.Entries)
}

func (r GossipExchangeRequest) Marshal() ([]byte, error) {
	dst := make([]byte, 0, 32*len(r.Entries)+2)
	return r.AppendMarshalBinary(dst)
}

func (r *GossipExchangeRequest) Unmarshal(data []byte) error {
	entries, err := decodeGossipEntries(data, r.Entries)
	if err != nil {
		return err
	}
	r.Entries = entries
	return nil
}

// GossipExchangeResponse carries a slice of GossipEntry.
type GossipExchangeResponse struct {
	Entries []GossipEntry
}

func (r GossipExchangeResponse) AppendMarshalBinary(dst []byte) ([]byte, error) {
	return appendGossipEntries(dst, r.Entries)
}

func (r GossipExchangeResponse) Marshal() ([]byte, error) {
	dst := make([]byte, 0, 32*len(r.Entries)+2)
	return r.AppendMarshalBinary(dst)
}

func (r *GossipExchangeResponse) Unmarshal(data []byte) error {
	entries, err := decodeGossipEntries(data, r.Entries)
	if err != nil {
		return err
	}
	r.Entries = entries
	return nil
}

func appendGossipEntries(dst []byte, entries []GossipEntry) ([]byte, error) {
	if len(entries) > math.MaxUint16 {
		return nil, fmt.Errorf("wire: too many gossip entries to encode: %d", len(entries))
	}
	dst = binary.BigEndian.AppendUint16(dst, uint16(len(entries)))
	for _, e := range entries {
		dst = appendUint32Prefixed(dst, []byte(e.NodeID))
		dst = appendUint32Prefixed(dst, []byte(e.Addr))
		dst = append(dst, e.Status)
		dst = binary.BigEndian.AppendUint64(dst, e.Version)
		dst = binary.BigEndian.AppendUint64(dst, uint64(e.UpdatedAt))
	}
	return dst, nil
}

func decodeGossipEntries(data []byte, reuse []GossipEntry) ([]GossipEntry, error) {
	if len(data) < 2 {
		return nil, fmt.Errorf("%w: gossip entries header truncated", quorumerr.ErrCorruptedData)
	}
	count := int(binary.BigEndian.Uint16(data))
	data = data[2:]

	var entries []GossipEntry
	if cap(reuse) >= count {
		entries = reuse[:0]
	} else {
		entries = make([]GossipEntry, 0, count)
	}

	for i := 0; i < count; i++ {
		idBytes, rest, err := decodeUint32Prefixed(data)
		if err != nil {
			return nil, fmt.Errorf("%w: decoding gossip nodeID: %v", quorumerr.ErrCorruptedData, err)
		}
		addrBytes, rest, err := decodeUint32Prefixed(rest)
		if err != nil {
			return nil, fmt.Errorf("%w: decoding gossip addr: %v", quorumerr.ErrCorruptedData, err)
		}
		if len(rest) < 1+8+8 {
			return nil, fmt.Errorf("%w: decoding gossip payload truncated", quorumerr.ErrCorruptedData)
		}
		status := rest[0]
		version := binary.BigEndian.Uint64(rest[1:9])
		updatedAt := int64(binary.BigEndian.Uint64(rest[9:17]))
		data = rest[17:]

		var nodeID node.NodeID
		if len(idBytes) > 0 {
			nodeID = node.NodeID(unsafe.String(unsafe.SliceData(idBytes), len(idBytes)))
		}
		var addr string
		if len(addrBytes) > 0 {
			addr = unsafe.String(unsafe.SliceData(addrBytes), len(addrBytes))
		}

		entries = append(entries, GossipEntry{
			NodeID:    nodeID,
			Addr:      addr,
			Status:    status,
			Version:   version,
			UpdatedAt: updatedAt,
		})
	}
	return entries, nil
}
