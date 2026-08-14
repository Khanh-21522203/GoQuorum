package iouring

import (
	"bytes"
	"errors"
	"testing"

	"goquorum.io/v2/contracts/node"
	"goquorum.io/v2/contracts/quorumerr"
	"goquorum.io/v2/contracts/vclock"
	"goquorum.io/v2/engine/storage"
	"goquorum.io/v2/engine/transport"
)

func sampleSiblingSet() *storage.SiblingSet {
	vc := vclock.NewVectorClock()
	vc.Set("node-a", 3)
	return &storage.SiblingSet{Siblings: []storage.Sibling{
		{Value: []byte("hello"), VClock: vc, Timestamp: 100, Tombstone: false, ExpiresAt: 0},
	}}
}

func sampleGossipEntries() []transport.GossipEntry {
	return []transport.GossipEntry{
		{NodeID: node.NodeID("node-a"), Addr: "10.0.0.1:9000", Status: 1, Version: 7, UpdatedAt: 1000},
		{NodeID: node.NodeID("node-b"), Addr: "10.0.0.2:9000", Status: 2, Version: 8, UpdatedAt: 2000},
	}
}

// --- RemotePut ---------------------------------------------------------------

func TestRemotePutRequest_RoundTrip(t *testing.T) {
	req := RemotePutRequest{Key: []byte("some-key"), Siblings: sampleSiblingSet()}
	data, err := req.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got RemotePutRequest
	if err := got.Unmarshal(data); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !bytes.Equal(got.Key, req.Key) {
		t.Errorf("Key = %q, want %q", got.Key, req.Key)
	}
	if len(got.Siblings.Siblings) != 1 || string(got.Siblings.Siblings[0].Value) != "hello" {
		t.Errorf("Siblings mismatch: %+v", got.Siblings)
	}
}

func TestRemotePutRequest_RoundTrip_EmptyKeyAndSiblings(t *testing.T) {
	req := RemotePutRequest{Key: []byte{}, Siblings: &storage.SiblingSet{}}
	data, err := req.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got RemotePutRequest
	if err := got.Unmarshal(data); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(got.Key) != 0 {
		t.Errorf("Key = %q, want empty", got.Key)
	}
	if got.Siblings == nil || len(got.Siblings.Siblings) != 0 {
		t.Errorf("Siblings = %+v, want empty", got.Siblings)
	}
}

func TestRemotePutRequest_RoundTrip_NilSiblings(t *testing.T) {
	req := RemotePutRequest{Key: []byte("k")}
	data, err := req.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got RemotePutRequest
	if err := got.Unmarshal(data); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Siblings == nil || len(got.Siblings.Siblings) != 0 {
		t.Errorf("Siblings = %+v, want empty (non-nil)", got.Siblings)
	}
}

func TestRemotePutResponse_RoundTrip(t *testing.T) {
	for _, status := range []StatusCode{StatusOK, StatusStorageFull, StatusUnknownError} {
		resp := RemotePutResponse{Status: status}
		data, err := resp.Marshal()
		if err != nil {
			t.Fatalf("Marshal(%v): %v", status, err)
		}
		var got RemotePutResponse
		if err := got.Unmarshal(data); err != nil {
			t.Fatalf("Unmarshal(%v): %v", status, err)
		}
		if got.Status != status {
			t.Errorf("Status = %v, want %v", got.Status, status)
		}
	}
}

// --- RemoteGet ---------------------------------------------------------------

func TestRemoteGetRequest_RoundTrip(t *testing.T) {
	req := RemoteGetRequest{Key: []byte("get-key")}
	data, err := req.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got RemoteGetRequest
	if err := got.Unmarshal(data); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !bytes.Equal(got.Key, req.Key) {
		t.Errorf("Key = %q, want %q", got.Key, req.Key)
	}
}

func TestRemoteGetRequest_RoundTrip_EmptyKey(t *testing.T) {
	req := RemoteGetRequest{Key: []byte{}}
	data, err := req.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got RemoteGetRequest
	if err := got.Unmarshal(data); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(got.Key) != 0 {
		t.Errorf("Key = %q, want empty", got.Key)
	}
}

func TestRemoteGetResponse_RoundTrip_OK(t *testing.T) {
	resp := RemoteGetResponse{Status: StatusOK, Siblings: sampleSiblingSet()}
	data, err := resp.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got RemoteGetResponse
	if err := got.Unmarshal(data); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Status != StatusOK {
		t.Errorf("Status = %v, want OK", got.Status)
	}
	if got.Siblings == nil || len(got.Siblings.Siblings) != 1 {
		t.Errorf("Siblings mismatch: %+v", got.Siblings)
	}
}

func TestRemoteGetResponse_RoundTrip_OKNilSiblings(t *testing.T) {
	resp := RemoteGetResponse{Status: StatusOK, Siblings: nil}
	data, err := resp.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got RemoteGetResponse
	if err := got.Unmarshal(data); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Status != StatusOK {
		t.Errorf("Status = %v, want OK", got.Status)
	}
	if got.Siblings == nil || len(got.Siblings.Siblings) != 0 {
		t.Errorf("Siblings = %+v, want empty (non-nil)", got.Siblings)
	}
}

func TestRemoteGetResponse_RoundTrip_NotFoundOmitsSiblings(t *testing.T) {
	resp := RemoteGetResponse{Status: StatusKeyNotFound, Siblings: sampleSiblingSet()}
	data, err := resp.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got RemoteGetResponse
	if err := got.Unmarshal(data); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Status != StatusKeyNotFound {
		t.Errorf("Status = %v, want KeyNotFound", got.Status)
	}
	if got.Siblings != nil {
		t.Errorf("Siblings = %+v, want nil when Status != OK", got.Siblings)
	}
}

// --- Heartbeat -----------------------------------------------------------------

func TestHeartbeatRequest_RoundTrip(t *testing.T) {
	req := HeartbeatRequest{}
	data, err := req.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if len(data) != 0 {
		t.Errorf("Marshal() = %d bytes, want 0", len(data))
	}
	var got HeartbeatRequest
	if err := got.Unmarshal(data); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
}

func TestHeartbeatResponse_RoundTrip(t *testing.T) {
	resp := HeartbeatResponse{Status: StatusOK}
	data, err := resp.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got HeartbeatResponse
	if err := got.Unmarshal(data); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Status != StatusOK {
		t.Errorf("Status = %v, want OK", got.Status)
	}
}

// --- GetMerkleRoot -----------------------------------------------------------

func TestGetMerkleRootRequest_RoundTrip(t *testing.T) {
	req := GetMerkleRootRequest{}
	data, err := req.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got GetMerkleRootRequest
	if err := got.Unmarshal(data); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
}

func TestGetMerkleRootResponse_RoundTrip(t *testing.T) {
	resp := GetMerkleRootResponse{Status: StatusOK, Root: []byte{0xDE, 0xAD, 0xBE, 0xEF}}
	data, err := resp.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got GetMerkleRootResponse
	if err := got.Unmarshal(data); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Status != StatusOK || !bytes.Equal(got.Root, resp.Root) {
		t.Errorf("got %+v, want %+v", got, resp)
	}
}

func TestGetMerkleRootResponse_RoundTrip_EmptyRoot(t *testing.T) {
	resp := GetMerkleRootResponse{Status: StatusStorageIO, Root: nil}
	data, err := resp.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got GetMerkleRootResponse
	if err := got.Unmarshal(data); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Status != StatusStorageIO || len(got.Root) != 0 {
		t.Errorf("got %+v, want empty root with StorageIO status", got)
	}
}

// --- NotifyLeaving -------------------------------------------------------------

func TestNotifyLeavingRequest_RoundTrip(t *testing.T) {
	req := NotifyLeavingRequest{}
	data, err := req.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got NotifyLeavingRequest
	if err := got.Unmarshal(data); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
}

func TestNotifyLeavingResponse_RoundTrip(t *testing.T) {
	resp := NotifyLeavingResponse{Status: StatusOK}
	data, err := resp.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got NotifyLeavingResponse
	if err := got.Unmarshal(data); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Status != StatusOK {
		t.Errorf("Status = %v, want OK", got.Status)
	}
}

// --- GossipExchange -----------------------------------------------------------

func TestGossipExchangeRequest_RoundTrip(t *testing.T) {
	req := GossipExchangeRequest{Entries: sampleGossipEntries()}
	data, err := req.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got GossipExchangeRequest
	if err := got.Unmarshal(data); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(got.Entries) != 2 {
		t.Fatalf("Entries len = %d, want 2", len(got.Entries))
	}
	for i, e := range req.Entries {
		g := got.Entries[i]
		if g.NodeID != e.NodeID || g.Addr != e.Addr || g.Status != e.Status || g.Version != e.Version || g.UpdatedAt != e.UpdatedAt {
			t.Errorf("entry %d mismatch: got %+v, want %+v", i, g, e)
		}
	}
}

func TestGossipExchangeRequest_RoundTrip_EmptyEntries(t *testing.T) {
	req := GossipExchangeRequest{}
	data, err := req.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got GossipExchangeRequest
	if err := got.Unmarshal(data); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(got.Entries) != 0 {
		t.Errorf("Entries = %+v, want empty", got.Entries)
	}
}

func TestGossipExchangeResponse_RoundTrip(t *testing.T) {
	resp := GossipExchangeResponse{Entries: sampleGossipEntries()}
	data, err := resp.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got GossipExchangeResponse
	if err := got.Unmarshal(data); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(got.Entries) != 2 {
		t.Fatalf("Entries len = %d, want 2", len(got.Entries))
	}
}

func TestGossipExchangeResponse_RoundTrip_EmptyEntries(t *testing.T) {
	resp := GossipExchangeResponse{}
	data, err := resp.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got GossipExchangeResponse
	if err := got.Unmarshal(data); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(got.Entries) != 0 {
		t.Errorf("Entries = %+v, want empty", got.Entries)
	}
}

// --- StatusCode <-> error ------------------------------------------------------

func TestStatusCodeFromError_ToError_RoundTrip(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code StatusCode
	}{
		{"nil", nil, StatusOK},
		{"KeyNotFound", quorumerr.ErrKeyNotFound, StatusKeyNotFound},
		{"CorruptedData", quorumerr.ErrCorruptedData, StatusCorruptedData},
		{"StorageClosed", quorumerr.ErrStorageClosed, StatusStorageClosed},
		{"StorageFull", quorumerr.ErrStorageFull, StatusStorageFull},
		{"StorageIO", quorumerr.ErrStorageIO, StatusStorageIO},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotCode := StatusCodeFromError(c.err)
			if gotCode != c.code {
				t.Errorf("StatusCodeFromError(%v) = %v, want %v", c.err, gotCode, c.code)
			}
			if c.err != nil {
				gotErr := StatusCodeToError(c.code)
				if !errors.Is(gotErr, c.err) {
					t.Errorf("StatusCodeToError(%v) = %v, want errors.Is match with %v", c.code, gotErr, c.err)
				}
			} else {
				if gotErr := StatusCodeToError(c.code); gotErr != nil {
					t.Errorf("StatusCodeToError(StatusOK) = %v, want nil", gotErr)
				}
			}
		})
	}
}

func TestStatusCodeFromError_UnrelatedError(t *testing.T) {
	err := errors.New("some unrelated failure")
	if got := StatusCodeFromError(err); got != StatusUnknownError {
		t.Errorf("StatusCodeFromError(unrelated) = %v, want StatusUnknownError", got)
	}
}

func TestStatusCodeToError_UnknownAndUnrecognized(t *testing.T) {
	if err := StatusCodeToError(StatusUnknownError); err == nil {
		t.Error("StatusCodeToError(StatusUnknownError) = nil, want a generic error")
	}
	if err := StatusCodeToError(StatusCode(9999)); err == nil {
		t.Error("StatusCodeToError(9999) = nil, want a generic error")
	}
}

func TestStatusCodeFromError_WrappedSentinel(t *testing.T) {
	wrapped := errors.Join(errors.New("wrapper"), quorumerr.ErrStorageIO)
	if got := StatusCodeFromError(wrapped); got != StatusStorageIO {
		t.Errorf("StatusCodeFromError(wrapped ErrStorageIO) = %v, want StatusStorageIO", got)
	}
}

// --- Fuzz: Unmarshal must never panic on hostile input ------------------------

func FuzzRemotePutRequestUnmarshal(f *testing.F) {
	valid, err := (RemotePutRequest{Key: []byte("k"), Siblings: sampleSiblingSet()}).Marshal()
	if err != nil {
		f.Fatalf("seed Marshal: %v", err)
	}
	f.Add(valid)
	for n := 0; n <= len(valid); n++ {
		f.Add(valid[:n])
	}
	f.Add([]byte{})
	f.Add([]byte{0xFF, 0xFF, 0xFF, 0xFF})
	f.Fuzz(func(t *testing.T, data []byte) {
		var r RemotePutRequest
		_ = r.Unmarshal(data)
	})
}

func FuzzRemotePutResponseUnmarshal(f *testing.F) {
	valid, err := (RemotePutResponse{Status: StatusOK}).Marshal()
	if err != nil {
		f.Fatalf("seed Marshal: %v", err)
	}
	f.Add(valid)
	f.Add([]byte{})
	f.Add([]byte{0x00})
	f.Fuzz(func(t *testing.T, data []byte) {
		var r RemotePutResponse
		_ = r.Unmarshal(data)
	})
}

func FuzzRemoteGetRequestUnmarshal(f *testing.F) {
	valid, err := (RemoteGetRequest{Key: []byte("k")}).Marshal()
	if err != nil {
		f.Fatalf("seed Marshal: %v", err)
	}
	f.Add(valid)
	for n := 0; n <= len(valid); n++ {
		f.Add(valid[:n])
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		var r RemoteGetRequest
		_ = r.Unmarshal(data)
	})
}

func FuzzRemoteGetResponseUnmarshal(f *testing.F) {
	valid, err := (RemoteGetResponse{Status: StatusOK, Siblings: sampleSiblingSet()}).Marshal()
	if err != nil {
		f.Fatalf("seed Marshal: %v", err)
	}
	f.Add(valid)
	for n := 0; n <= len(valid); n++ {
		f.Add(valid[:n])
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		var r RemoteGetResponse
		_ = r.Unmarshal(data)
	})
}

func FuzzHeartbeatRequestUnmarshal(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0x01, 0x02, 0x03})
	f.Fuzz(func(t *testing.T, data []byte) {
		var r HeartbeatRequest
		_ = r.Unmarshal(data)
	})
}

func FuzzHeartbeatResponseUnmarshal(f *testing.F) {
	valid, err := (HeartbeatResponse{Status: StatusOK}).Marshal()
	if err != nil {
		f.Fatalf("seed Marshal: %v", err)
	}
	f.Add(valid)
	f.Add([]byte{})
	f.Add([]byte{0x00})
	f.Fuzz(func(t *testing.T, data []byte) {
		var r HeartbeatResponse
		_ = r.Unmarshal(data)
	})
}

func FuzzGetMerkleRootRequestUnmarshal(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0x01})
	f.Fuzz(func(t *testing.T, data []byte) {
		var r GetMerkleRootRequest
		_ = r.Unmarshal(data)
	})
}

func FuzzGetMerkleRootResponseUnmarshal(f *testing.F) {
	valid, err := (GetMerkleRootResponse{Status: StatusOK, Root: []byte{1, 2, 3, 4}}).Marshal()
	if err != nil {
		f.Fatalf("seed Marshal: %v", err)
	}
	f.Add(valid)
	for n := 0; n <= len(valid); n++ {
		f.Add(valid[:n])
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		var r GetMerkleRootResponse
		_ = r.Unmarshal(data)
	})
}

func FuzzNotifyLeavingRequestUnmarshal(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0x01})
	f.Fuzz(func(t *testing.T, data []byte) {
		var r NotifyLeavingRequest
		_ = r.Unmarshal(data)
	})
}

func FuzzNotifyLeavingResponseUnmarshal(f *testing.F) {
	valid, err := (NotifyLeavingResponse{Status: StatusOK}).Marshal()
	if err != nil {
		f.Fatalf("seed Marshal: %v", err)
	}
	f.Add(valid)
	f.Add([]byte{})
	f.Add([]byte{0x00})
	f.Fuzz(func(t *testing.T, data []byte) {
		var r NotifyLeavingResponse
		_ = r.Unmarshal(data)
	})
}

func FuzzGossipExchangeRequestUnmarshal(f *testing.F) {
	valid, err := (GossipExchangeRequest{Entries: sampleGossipEntries()}).Marshal()
	if err != nil {
		f.Fatalf("seed Marshal: %v", err)
	}
	f.Add(valid)
	for n := 0; n <= len(valid); n++ {
		f.Add(valid[:n])
	}
	f.Add([]byte{0xFF, 0xFF})
	f.Fuzz(func(t *testing.T, data []byte) {
		var r GossipExchangeRequest
		_ = r.Unmarshal(data)
	})
}

func FuzzGossipExchangeResponseUnmarshal(f *testing.F) {
	valid, err := (GossipExchangeResponse{Entries: sampleGossipEntries()}).Marshal()
	if err != nil {
		f.Fatalf("seed Marshal: %v", err)
	}
	f.Add(valid)
	for n := 0; n <= len(valid); n++ {
		f.Add(valid[:n])
	}
	f.Add([]byte{0xFF, 0xFF})
	f.Fuzz(func(t *testing.T, data []byte) {
		var r GossipExchangeResponse
		_ = r.Unmarshal(data)
	})
}
