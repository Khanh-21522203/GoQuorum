package iouring

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"

	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/contracts/quorumerr"
	"goquorum.io/v2/engine/storage"
	"goquorum.io/v2/engine/transport"
)

// MessageID identifies the kind of RPC body a frame carries.
type MessageID uint16

// Message IDs for the request/response pair of every engine/transport.Transport
// RPC.
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

// StatusCode is a small, wire-stable numeric encoding of the error (if
// any) a response carries, so a peer on a different schema version does
// not need to understand Go error values.
type StatusCode uint16

const (
	StatusOK            StatusCode = 0
	StatusKeyNotFound   StatusCode = 1
	StatusCorruptedData StatusCode = 2
	StatusStorageClosed StatusCode = 3
	StatusStorageFull   StatusCode = 4
	StatusStorageIO     StatusCode = 5
	// StatusUnknownError is a forward-compatible fallback for any error
	// that does not map to one of the sentinels above, including error
	// kinds introduced by a future schema version.
	StatusUnknownError StatusCode = 65535
)

// String returns the human-readable name of the status code.
func (s StatusCode) String() string {
	switch s {
	case StatusOK:
		return "OK"
	case StatusKeyNotFound:
		return "KeyNotFound"
	case StatusCorruptedData:
		return "CorruptedData"
	case StatusStorageClosed:
		return "StorageClosed"
	case StatusStorageFull:
		return "StorageFull"
	case StatusStorageIO:
		return "StorageIO"
	case StatusUnknownError:
		return "UnknownError"
	default:
		return "UNKNOWN"
	}
}

// StatusCodeFromError maps an engine/storage error to its wire status
// code. nil maps to StatusOK; any error not recognized via errors.Is
// against a quorumerr sentinel maps to StatusUnknownError.
func StatusCodeFromError(err error) StatusCode {
	switch {
	case err == nil:
		return StatusOK
	case errors.Is(err, quorumerr.ErrKeyNotFound):
		return StatusKeyNotFound
	case errors.Is(err, quorumerr.ErrCorruptedData):
		return StatusCorruptedData
	case errors.Is(err, quorumerr.ErrStorageClosed):
		return StatusStorageClosed
	case errors.Is(err, quorumerr.ErrStorageFull):
		return StatusStorageFull
	case errors.Is(err, quorumerr.ErrStorageIO):
		return StatusStorageIO
	default:
		return StatusUnknownError
	}
}

// StatusCodeToError is the inverse of StatusCodeFromError: it returns the
// exact quorumerr sentinel value for a recognized code, nil for
// StatusOK, and a generic error describing the numeric code for
// StatusUnknownError or any code this build does not recognize (e.g. one
// introduced by a newer schema version).
func StatusCodeToError(code StatusCode) error {
	switch code {
	case StatusOK:
		return nil
	case StatusKeyNotFound:
		return quorumerr.ErrKeyNotFound
	case StatusCorruptedData:
		return quorumerr.ErrCorruptedData
	case StatusStorageClosed:
		return quorumerr.ErrStorageClosed
	case StatusStorageFull:
		return quorumerr.ErrStorageFull
	case StatusStorageIO:
		return quorumerr.ErrStorageIO
	default:
		return fmt.Errorf("iouring: unrecognized status code %d", uint16(code))
	}
}

// --- length-prefix helpers -------------------------------------------------
//
// These mirror the shape of the unexported helpers in engine/storage's
// types.go (appendUint32Prefixed/decodeUint32Prefixed), duplicated here
// because they are unexported there and this package sits on the other
// side of the engine/infra boundary.

func appendUint8Prefixed(buf, data []byte) ([]byte, error) {
	if len(data) > math.MaxUint8 {
		return nil, fmt.Errorf("iouring: value too long for uint8 length prefix: %d bytes", len(data))
	}
	buf = append(buf, uint8(len(data)))
	return append(buf, data...), nil
}

func decodeUint8Prefixed(data []byte) (value, rest []byte, err error) {
	if len(data) < 1 {
		return nil, nil, fmt.Errorf("%w: uint8 length prefix truncated", quorumerr.ErrCorruptedData)
	}
	n := int(data[0])
	data = data[1:]
	if n > len(data) {
		return nil, nil, fmt.Errorf("%w: uint8 length prefix exceeds remaining data", quorumerr.ErrCorruptedData)
	}
	return data[:n], data[n:], nil
}

func appendUint16Prefixed(buf, data []byte) ([]byte, error) {
	if len(data) > math.MaxUint16 {
		return nil, fmt.Errorf("iouring: value too long for uint16 length prefix: %d bytes", len(data))
	}
	buf = binary.BigEndian.AppendUint16(buf, uint16(len(data)))
	return append(buf, data...), nil
}

func decodeUint16Prefixed(data []byte) (value, rest []byte, err error) {
	if len(data) < 2 {
		return nil, nil, fmt.Errorf("%w: uint16 length prefix truncated", quorumerr.ErrCorruptedData)
	}
	n := binary.BigEndian.Uint16(data)
	data = data[2:]
	if uint64(n) > uint64(len(data)) {
		return nil, nil, fmt.Errorf("%w: uint16 length prefix exceeds remaining data", quorumerr.ErrCorruptedData)
	}
	return data[:n], data[n:], nil
}

func appendUint32Prefixed(buf, data []byte) []byte {
	buf = binary.BigEndian.AppendUint32(buf, uint32(len(data)))
	return append(buf, data...)
}

func decodeUint32Prefixed(data []byte) (value, rest []byte, err error) {
	if len(data) < 4 {
		return nil, nil, fmt.Errorf("%w: uint32 length prefix truncated", quorumerr.ErrCorruptedData)
	}
	n := binary.BigEndian.Uint32(data)
	data = data[4:]
	if uint64(n) > uint64(len(data)) {
		return nil, nil, fmt.Errorf("%w: uint32 length prefix exceeds remaining data", quorumerr.ErrCorruptedData)
	}
	return data[:n], data[n:], nil
}

// --- gossip entry codec, shared by GossipExchangeRequest/Response ---------
//
// Layout per entry: [nodeIDLen uint8][nodeID][addrLen uint16][addr]
// [status byte][version uint64][updatedAt int64]. nodeIDLen is a uint8
// since node.NodeID.Validate caps IDs at 64 characters; addrLen is a
// uint16 since addresses are not similarly bounded.

func marshalGossipEntries(entries []transport.GossipEntry) ([]byte, error) {
	if len(entries) > math.MaxUint16 {
		return nil, fmt.Errorf("iouring: too many gossip entries to encode: %d", len(entries))
	}
	buf := make([]byte, 2, 32*len(entries)+2)
	binary.BigEndian.PutUint16(buf, uint16(len(entries)))
	for _, e := range entries {
		var err error
		buf, err = appendUint8Prefixed(buf, []byte(e.NodeID))
		if err != nil {
			return nil, fmt.Errorf("iouring: encoding gossip entry node ID: %w", err)
		}
		buf, err = appendUint16Prefixed(buf, []byte(e.Addr))
		if err != nil {
			return nil, fmt.Errorf("iouring: encoding gossip entry address: %w", err)
		}
		buf = append(buf, e.Status)
		buf = binary.BigEndian.AppendUint64(buf, e.Version)
		buf = binary.BigEndian.AppendUint64(buf, uint64(e.UpdatedAt))
	}
	return buf, nil
}

func unmarshalGossipEntries(data []byte) ([]transport.GossipEntry, error) {
	if len(data) < 2 {
		return nil, fmt.Errorf("%w: gossip entries header truncated", quorumerr.ErrCorruptedData)
	}
	count := binary.BigEndian.Uint16(data)
	data = data[2:]

	entries := make([]transport.GossipEntry, 0, count)
	for i := uint16(0); i < count; i++ {
		idBytes, rest, err := decodeUint8Prefixed(data)
		if err != nil {
			return nil, fmt.Errorf("gossip entry node ID: %w", err)
		}
		addrBytes, rest2, err := decodeUint16Prefixed(rest)
		if err != nil {
			return nil, fmt.Errorf("gossip entry address: %w", err)
		}
		rest = rest2

		const trailerLen = 1 + 8 + 8 // status + version + updatedAt
		if len(rest) < trailerLen {
			return nil, fmt.Errorf("%w: gossip entry trailer truncated", quorumerr.ErrCorruptedData)
		}
		status := rest[0]
		rest = rest[1:]
		version := binary.BigEndian.Uint64(rest)
		rest = rest[8:]
		updatedAt := int64(binary.BigEndian.Uint64(rest))
		rest = rest[8:]

		entries = append(entries, transport.GossipEntry{
			NodeID:    node.NodeID(idBytes),
			Addr:      string(addrBytes),
			Status:    status,
			Version:   version,
			UpdatedAt: updatedAt,
		})
		data = rest
	}
	return entries, nil
}

// --- RemotePut --------------------------------------------------------------

// RemotePutRequest is the body of a MsgRemotePutRequest frame.
//
// Layout: [keyLen uint32][key][siblingsLen uint32][siblings]
// (siblings is the opaque blob produced by storage.SiblingSet.MarshalBinary)
type RemotePutRequest struct {
	Key      []byte
	Siblings *storage.SiblingSet
}

// Marshal encodes the request body (not including the frame header).
func (r RemotePutRequest) Marshal() ([]byte, error) {
	ss := storage.SiblingSet{}
	if r.Siblings != nil {
		ss = *r.Siblings
	}
	siblingsBytes, err := ss.MarshalBinary()
	if err != nil {
		return nil, fmt.Errorf("iouring: encoding RemotePutRequest siblings: %w", err)
	}
	buf := appendUint32Prefixed(make([]byte, 0, len(r.Key)+len(siblingsBytes)+8), r.Key)
	buf = appendUint32Prefixed(buf, siblingsBytes)
	return buf, nil
}

// Unmarshal decodes a request body previously produced by Marshal. It
// never panics on truncated or malformed input.
func (r *RemotePutRequest) Unmarshal(data []byte) error {
	key, rest, err := decodeUint32Prefixed(data)
	if err != nil {
		return fmt.Errorf("RemotePutRequest key: %w", err)
	}
	siblingsBytes, _, err := decodeUint32Prefixed(rest)
	if err != nil {
		return fmt.Errorf("RemotePutRequest siblings: %w", err)
	}
	var ss storage.SiblingSet
	if err := ss.UnmarshalBinary(siblingsBytes); err != nil {
		return fmt.Errorf("RemotePutRequest siblings: %w", err)
	}
	r.Key = key
	r.Siblings = &ss
	return nil
}

// RemotePutResponse is the body of a MsgRemotePutResponse frame.
//
// Layout: [status uint16]
type RemotePutResponse struct {
	Status StatusCode
}

// Marshal encodes the response body (not including the frame header).
func (r RemotePutResponse) Marshal() ([]byte, error) {
	buf := make([]byte, 2)
	binary.BigEndian.PutUint16(buf, uint16(r.Status))
	return buf, nil
}

// Unmarshal decodes a response body previously produced by Marshal. It
// never panics on truncated or malformed input.
func (r *RemotePutResponse) Unmarshal(data []byte) error {
	if len(data) < 2 {
		return fmt.Errorf("%w: RemotePutResponse status truncated", quorumerr.ErrCorruptedData)
	}
	r.Status = StatusCode(binary.BigEndian.Uint16(data))
	return nil
}

// --- RemoteGet --------------------------------------------------------------

// RemoteGetRequest is the body of a MsgRemoteGetRequest frame.
//
// Layout: [keyLen uint32][key]
type RemoteGetRequest struct {
	Key []byte
}

// Marshal encodes the request body (not including the frame header).
func (r RemoteGetRequest) Marshal() ([]byte, error) {
	return appendUint32Prefixed(make([]byte, 0, len(r.Key)+4), r.Key), nil
}

// Unmarshal decodes a request body previously produced by Marshal. It
// never panics on truncated or malformed input.
func (r *RemoteGetRequest) Unmarshal(data []byte) error {
	key, _, err := decodeUint32Prefixed(data)
	if err != nil {
		return fmt.Errorf("RemoteGetRequest key: %w", err)
	}
	r.Key = key
	return nil
}

// RemoteGetResponse is the body of a MsgRemoteGetResponse frame.
//
// Layout: [status uint16][siblingsLen uint32][siblings]. When Status is
// not StatusOK, the siblings blob is written empty (length 0) regardless
// of the Siblings field, and Siblings decodes back to nil.
type RemoteGetResponse struct {
	Status   StatusCode
	Siblings *storage.SiblingSet
}

// Marshal encodes the response body (not including the frame header).
func (r RemoteGetResponse) Marshal() ([]byte, error) {
	buf := make([]byte, 2)
	binary.BigEndian.PutUint16(buf, uint16(r.Status))

	var siblingsBytes []byte
	if r.Status == StatusOK {
		ss := storage.SiblingSet{}
		if r.Siblings != nil {
			ss = *r.Siblings
		}
		b, err := ss.MarshalBinary()
		if err != nil {
			return nil, fmt.Errorf("iouring: encoding RemoteGetResponse siblings: %w", err)
		}
		siblingsBytes = b
	}
	buf = appendUint32Prefixed(buf, siblingsBytes)
	return buf, nil
}

// Unmarshal decodes a response body previously produced by Marshal. It
// never panics on truncated or malformed input.
func (r *RemoteGetResponse) Unmarshal(data []byte) error {
	if len(data) < 2 {
		return fmt.Errorf("%w: RemoteGetResponse status truncated", quorumerr.ErrCorruptedData)
	}
	status := StatusCode(binary.BigEndian.Uint16(data))
	data = data[2:]

	siblingsBytes, _, err := decodeUint32Prefixed(data)
	if err != nil {
		return fmt.Errorf("RemoteGetResponse siblings: %w", err)
	}

	r.Status = status
	if status != StatusOK {
		r.Siblings = nil
		return nil
	}
	var ss storage.SiblingSet
	if err := ss.UnmarshalBinary(siblingsBytes); err != nil {
		return fmt.Errorf("RemoteGetResponse siblings: %w", err)
	}
	r.Siblings = &ss
	return nil
}

// --- Heartbeat ---------------------------------------------------------------

// HeartbeatRequest is the body of a MsgHeartbeatRequest frame. It carries
// no fields; it still gets a full 16-byte frame header when framed by
// EncodeFrame.
type HeartbeatRequest struct{}

// Marshal encodes the request body (not including the frame header).
func (r HeartbeatRequest) Marshal() ([]byte, error) {
	return nil, nil
}

// Unmarshal decodes a request body previously produced by Marshal. There
// are no fields to read; any bytes present (e.g. from a newer schema
// version) are ignored.
func (r *HeartbeatRequest) Unmarshal(data []byte) error {
	return nil
}

// HeartbeatResponse is the body of a MsgHeartbeatResponse frame.
//
// Layout: [status uint16]
type HeartbeatResponse struct {
	Status StatusCode
}

// Marshal encodes the response body (not including the frame header).
func (r HeartbeatResponse) Marshal() ([]byte, error) {
	buf := make([]byte, 2)
	binary.BigEndian.PutUint16(buf, uint16(r.Status))
	return buf, nil
}

// Unmarshal decodes a response body previously produced by Marshal. It
// never panics on truncated or malformed input.
func (r *HeartbeatResponse) Unmarshal(data []byte) error {
	if len(data) < 2 {
		return fmt.Errorf("%w: HeartbeatResponse status truncated", quorumerr.ErrCorruptedData)
	}
	r.Status = StatusCode(binary.BigEndian.Uint16(data))
	return nil
}

// --- GetMerkleRoot -----------------------------------------------------------

// GetMerkleRootRequest is the body of a MsgGetMerkleRootRequest frame. It
// carries no fields.
type GetMerkleRootRequest struct{}

// Marshal encodes the request body (not including the frame header).
func (r GetMerkleRootRequest) Marshal() ([]byte, error) {
	return nil, nil
}

// Unmarshal decodes a request body previously produced by Marshal. There
// are no fields to read; any bytes present are ignored.
func (r *GetMerkleRootRequest) Unmarshal(data []byte) error {
	return nil
}

// GetMerkleRootResponse is the body of a MsgGetMerkleRootResponse frame.
//
// Layout: [status uint16][rootLen uint16][root]. Root is length-prefixed
// with a uint16 rather than hardcoded to a fixed hash size, so the
// anti-entropy hash algorithm (and thus root length) can change in the
// future without requiring a wire schema bump.
type GetMerkleRootResponse struct {
	Status StatusCode
	Root   []byte
}

// Marshal encodes the response body (not including the frame header).
func (r GetMerkleRootResponse) Marshal() ([]byte, error) {
	buf := make([]byte, 2)
	binary.BigEndian.PutUint16(buf, uint16(r.Status))
	buf, err := appendUint16Prefixed(buf, r.Root)
	if err != nil {
		return nil, fmt.Errorf("iouring: encoding GetMerkleRootResponse root: %w", err)
	}
	return buf, nil
}

// Unmarshal decodes a response body previously produced by Marshal. It
// never panics on truncated or malformed input.
func (r *GetMerkleRootResponse) Unmarshal(data []byte) error {
	if len(data) < 2 {
		return fmt.Errorf("%w: GetMerkleRootResponse status truncated", quorumerr.ErrCorruptedData)
	}
	status := StatusCode(binary.BigEndian.Uint16(data))
	data = data[2:]
	root, _, err := decodeUint16Prefixed(data)
	if err != nil {
		return fmt.Errorf("GetMerkleRootResponse root: %w", err)
	}
	r.Status = status
	r.Root = root
	return nil
}

// --- NotifyLeaving ------------------------------------------------------------

// NotifyLeavingRequest is the body of a MsgNotifyLeavingRequest frame. It
// carries no fields.
type NotifyLeavingRequest struct{}

// Marshal encodes the request body (not including the frame header).
func (r NotifyLeavingRequest) Marshal() ([]byte, error) {
	return nil, nil
}

// Unmarshal decodes a request body previously produced by Marshal. There
// are no fields to read; any bytes present are ignored.
func (r *NotifyLeavingRequest) Unmarshal(data []byte) error {
	return nil
}

// NotifyLeavingResponse is the body of a MsgNotifyLeavingResponse frame.
//
// Layout: [status uint16]
type NotifyLeavingResponse struct {
	Status StatusCode
}

// Marshal encodes the response body (not including the frame header).
func (r NotifyLeavingResponse) Marshal() ([]byte, error) {
	buf := make([]byte, 2)
	binary.BigEndian.PutUint16(buf, uint16(r.Status))
	return buf, nil
}

// Unmarshal decodes a response body previously produced by Marshal. It
// never panics on truncated or malformed input.
func (r *NotifyLeavingResponse) Unmarshal(data []byte) error {
	if len(data) < 2 {
		return fmt.Errorf("%w: NotifyLeavingResponse status truncated", quorumerr.ErrCorruptedData)
	}
	r.Status = StatusCode(binary.BigEndian.Uint16(data))
	return nil
}

// --- GossipExchange -----------------------------------------------------------

// GossipExchangeRequest is the body of a MsgGossipExchangeRequest frame.
//
// Layout: [count uint16]{[nodeIDLen uint8][nodeID][addrLen uint16][addr]
// [status byte][version uint64][updatedAt int64]}*count
type GossipExchangeRequest struct {
	Entries []transport.GossipEntry
}

// Marshal encodes the request body (not including the frame header).
func (r GossipExchangeRequest) Marshal() ([]byte, error) {
	buf, err := marshalGossipEntries(r.Entries)
	if err != nil {
		return nil, fmt.Errorf("iouring: encoding GossipExchangeRequest: %w", err)
	}
	return buf, nil
}

// Unmarshal decodes a request body previously produced by Marshal. It
// never panics on truncated or malformed input.
func (r *GossipExchangeRequest) Unmarshal(data []byte) error {
	entries, err := unmarshalGossipEntries(data)
	if err != nil {
		return fmt.Errorf("GossipExchangeRequest: %w", err)
	}
	r.Entries = entries
	return nil
}

// GossipExchangeResponse is the body of a MsgGossipExchangeResponse frame.
// It uses the same entry layout as GossipExchangeRequest.
type GossipExchangeResponse struct {
	Entries []transport.GossipEntry
}

// Marshal encodes the response body (not including the frame header).
func (r GossipExchangeResponse) Marshal() ([]byte, error) {
	buf, err := marshalGossipEntries(r.Entries)
	if err != nil {
		return nil, fmt.Errorf("iouring: encoding GossipExchangeResponse: %w", err)
	}
	return buf, nil
}

// Unmarshal decodes a response body previously produced by Marshal. It
// never panics on truncated or malformed input.
func (r *GossipExchangeResponse) Unmarshal(data []byte) error {
	entries, err := unmarshalGossipEntries(data)
	if err != nil {
		return fmt.Errorf("GossipExchangeResponse: %w", err)
	}
	r.Entries = entries
	return nil
}
