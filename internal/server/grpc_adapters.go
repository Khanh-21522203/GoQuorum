package server

// grpc_adapters.go wires the generated proto server interfaces to the internal
// API implementations (ClientAPI, AdminAPI, InternalAPI).

import (
	"context"
	"strings"

	pb "GoQuorum/api"
	internalpb "GoQuorum/api/cluster"
	"GoQuorum/internal/storage"
	"GoQuorum/internal/vclock"
)

// ── Client API adapter ───────────────────────────────────────────────────────

// goQuorumGRPCServer adapts ClientAPI to the generated pb.GoQuorumServer interface.
type goQuorumGRPCServer struct {
	pb.UnimplementedGoQuorumServer
	api *ClientAPI
}

func (s *goQuorumGRPCServer) Get(ctx context.Context, req *pb.GetRequest) (*pb.GetResponse, error) {
	result, err := s.api.Get(ctx, req.Key, 0, 0)
	if err != nil {
		return nil, err
	}
	resp := &pb.GetResponse{}
	for _, sib := range result.Siblings {
		resp.Siblings = append(resp.Siblings, internalSiblingToProto(sib))
	}
	return resp, nil
}

func (s *goQuorumGRPCServer) Put(ctx context.Context, req *pb.PutRequest) (*pb.PutResponse, error) {
	ctx2 := protoContextToVClockCtx(req.Context)
	var ttlSeconds int64
	if req.Options != nil {
		ttlSeconds = req.Options.TtlSeconds
	}
	result, err := s.api.Put(ctx, req.Key, req.Value, ctx2, 0, 0, ttlSeconds)
	if err != nil {
		return nil, err
	}
	return &pb.PutResponse{Context: vclockCtxToProto(result.Context)}, nil
}

func (s *goQuorumGRPCServer) Delete(ctx context.Context, req *pb.DeleteRequest) (*pb.DeleteResponse, error) {
	ctx2 := protoContextToVClockCtx(req.Context)
	if err := s.api.Delete(ctx, req.Key, ctx2, 0, 0); err != nil {
		return nil, err
	}
	return &pb.DeleteResponse{}, nil
}

func (s *goQuorumGRPCServer) BatchGet(ctx context.Context, req *pb.BatchGetRequest) (*pb.BatchGetResponse, error) {
	results, err := s.api.BatchGet(ctx, req.Keys)
	if err != nil {
		return nil, err
	}
	resp := &pb.BatchGetResponse{}
	for _, r := range results {
		pbResult := &pb.BatchGetResult{
			Key:   r.Key,
			Error: r.Error,
		}
		for _, sib := range r.Siblings {
			pbResult.Siblings = append(pbResult.Siblings, internalSiblingToProto(sib))
		}
		resp.Results = append(resp.Results, pbResult)
	}
	return resp, nil
}

func (s *goQuorumGRPCServer) BatchPut(ctx context.Context, req *pb.BatchPutRequest) (*pb.BatchPutResponse, error) {
	items := make([]BatchPutItemAPI, 0, len(req.Items))
	for _, item := range req.Items {
		items = append(items, BatchPutItemAPI{
			Key:     item.Key,
			Value:   item.Value,
			Context: protoContextToVClockCtx(item.Context),
		})
	}
	results, err := s.api.BatchPut(ctx, items)
	if err != nil {
		return nil, err
	}
	resp := &pb.BatchPutResponse{}
	for _, r := range results {
		pbResult := &pb.BatchPutResult{
			Key:     r.Key,
			Error:   r.Error,
			Context: vclockCtxToProto(r.Context),
		}
		resp.Results = append(resp.Results, pbResult)
	}
	return resp, nil
}

// ── Admin API adapter ─────────────────────────────────────────────────────────

// goQuorumAdminGRPCServer adapts AdminAPI to pb.GoQuorumAdminServer.
type goQuorumAdminGRPCServer struct {
	pb.UnimplementedGoQuorumAdminServer
	api *AdminAPI
}

func (s *goQuorumAdminGRPCServer) Health(ctx context.Context, _ *pb.HealthRequest) (*pb.HealthResponse, error) {
	result := s.api.Health()
	resp := &pb.HealthResponse{
		NodeId:        result.NodeID,
		UptimeSeconds: result.UptimeSeconds,
		Version:       result.Version,
		Checks:        make(map[string]*pb.CheckResult),
	}
	switch result.Status {
	case "healthy":
		resp.Status = pb.HealthStatus_HEALTH_HEALTHY
	case "degraded":
		resp.Status = pb.HealthStatus_HEALTH_DEGRADED
	case "unhealthy":
		resp.Status = pb.HealthStatus_HEALTH_UNHEALTHY
	}
	for name, check := range result.Checks {
		resp.Checks[name] = &pb.CheckResult{
			Status:      check.Status,
			LatencyMs:   check.LatencyMs,
			Error:       check.Error,
			PeersActive: int32(check.PeersActive),
			PeersTotal:  int32(check.PeersTotal),
			FreeBytes:   check.FreeBytes,
			TotalBytes:  check.TotalBytes,
		}
	}
	return resp, nil
}

func (s *goQuorumAdminGRPCServer) ClusterInfo(ctx context.Context, _ *pb.ClusterInfoRequest) (*pb.ClusterInfoResponse, error) {
	result := s.api.ClusterInfo()
	resp := &pb.ClusterInfoResponse{NodeId: result.NodeID}
	for _, peer := range result.Peers {
		peerStatus := pb.PeerStatus_PEER_UNKNOWN
		switch strings.ToUpper(peer.Status) {
		case "ACTIVE":
			peerStatus = pb.PeerStatus_PEER_ACTIVE
		case "SUSPECT":
			peerStatus = pb.PeerStatus_PEER_SUSPECT
		case "FAILED":
			peerStatus = pb.PeerStatus_PEER_FAILED
		}
		resp.Peers = append(resp.Peers, &pb.PeerInfo{
			NodeId:       peer.NodeID,
			Address:      peer.Address,
			Status:       peerStatus,
			LastSeenUnix: peer.LastSeenUnix,
			LatencyMs:    peer.LatencyMs,
		})
	}
	switch result.Status {
	case "healthy":
		resp.ClusterStatus = pb.ClusterStatus_CLUSTER_HEALTHY
	case "degraded":
		resp.ClusterStatus = pb.ClusterStatus_CLUSTER_DEGRADED
	case "critical":
		resp.ClusterStatus = pb.ClusterStatus_CLUSTER_CRITICAL
	}
	return resp, nil
}

func (s *goQuorumAdminGRPCServer) GetMetrics(ctx context.Context, _ *pb.GetMetricsRequest) (*pb.GetMetricsResponse, error) {
	metrics, err := s.api.GetMetrics()
	if err != nil {
		return nil, err
	}
	return &pb.GetMetricsResponse{Metrics: metrics}, nil
}

func (s *goQuorumAdminGRPCServer) KeyInfo(ctx context.Context, req *pb.KeyInfoRequest) (*pb.KeyInfoResponse, error) {
	result, err := s.api.KeyInfo(req.Key)
	if err != nil {
		return nil, err
	}
	resp := &pb.KeyInfoResponse{Key: result.Key}
	for _, r := range result.Replicas {
		ri := &pb.ReplicaKeyInfo{NodeId: r.NodeID, HasKey: r.HasKey, Error: r.Error}
		for _, sib := range r.Siblings {
			ri.Siblings = append(ri.Siblings, &pb.SiblingInfo{
				Timestamp: sib.Timestamp,
				ValueSize: sib.ValueSize,
				Tombstone: sib.Tombstone,
			})
		}
		resp.Replicas = append(resp.Replicas, ri)
	}
	return resp, nil
}

func (s *goQuorumAdminGRPCServer) TriggerCompaction(ctx context.Context, _ *pb.TriggerCompactionRequest) (*pb.TriggerCompactionResponse, error) {
	started, _ := s.api.TriggerCompaction()
	return &pb.TriggerCompactionResponse{Started: started}, nil
}

// ── Internal API adapter ──────────────────────────────────────────────────────

// goQuorumInternalGRPCServer adapts InternalAPI to internalpb.GoQuorumInternalServer.
type goQuorumInternalGRPCServer struct {
	internalpb.UnimplementedGoQuorumInternalServer
	api *InternalAPI
}

func (s *goQuorumInternalGRPCServer) Replicate(ctx context.Context, req *internalpb.ReplicateRequest) (*internalpb.ReplicateResponse, error) {
	internalReq := &ReplicateReq{
		Key:           req.Key,
		CoordinatorID: req.CoordinatorId,
		RequestID:     req.RequestId,
	}
	if req.Sibling != nil {
		internalReq.Sibling = SiblingData{
			Value:     req.Sibling.Value,
			Context:   protoContextEntriesToInternal(req.Sibling.Context),
			Tombstone: req.Sibling.Tombstone,
			Timestamp: req.Sibling.Timestamp,
		}
	}
	resp, err := s.api.Replicate(ctx, internalReq)
	if err != nil {
		return nil, err
	}
	return &internalpb.ReplicateResponse{Success: resp.Success, Error: resp.Error}, nil
}

func (s *goQuorumInternalGRPCServer) Read(ctx context.Context, req *internalpb.InternalReadRequest) (*internalpb.InternalReadResponse, error) {
	internalReq := &InternalReadReq{Key: req.Key, CoordinatorID: req.CoordinatorId}
	resp, err := s.api.Read(ctx, internalReq)
	if err != nil {
		return nil, err
	}
	pbResp := &internalpb.InternalReadResponse{Found: resp.Found}
	for _, sib := range resp.Siblings {
		pbResp.Siblings = append(pbResp.Siblings, internalSiblingDataToProto(sib))
	}
	return pbResp, nil
}

func (s *goQuorumInternalGRPCServer) Heartbeat(ctx context.Context, req *internalpb.HeartbeatRequest) (*internalpb.HeartbeatResponse, error) {
	internalReq := &HeartbeatReq{
		SenderID:  req.SenderId,
		Timestamp: req.Timestamp,
		Version:   req.Version,
	}
	resp, err := s.api.Heartbeat(ctx, internalReq)
	if err != nil {
		return nil, err
	}
	pbResp := &internalpb.HeartbeatResponse{
		ResponderId: resp.ResponderID,
		Timestamp:   resp.Timestamp,
	}
	for _, peer := range resp.Peers {
		pbResp.Peers = append(pbResp.Peers, &internalpb.PeerStatusInfo{
			NodeId:   peer.NodeID,
			LastSeen: peer.LastSeen,
		})
	}
	return pbResp, nil
}

func (s *goQuorumInternalGRPCServer) AntiEntropyExchange(ctx context.Context, req *internalpb.AntiEntropyRequest) (*internalpb.AntiEntropyResponse, error) {
	// Anti-entropy full exchange is handled by the anti-entropy subsystem;
	// this endpoint returns the peer's Merkle root for comparison.
	merkleReq := &GetMerkleRootReq{SenderID: req.SenderId}
	merkleResp, err := s.api.GetMerkleRoot(ctx, merkleReq)
	if err != nil {
		return nil, err
	}
	return &internalpb.AntiEntropyResponse{
		MerkleRoot: merkleResp.MerkleRoot,
	}, nil
}

func (s *goQuorumInternalGRPCServer) GetMerkleRoot(ctx context.Context, req *internalpb.GetMerkleRootRequest) (*internalpb.GetMerkleRootResponse, error) {
	internalReq := &GetMerkleRootReq{SenderID: req.SenderId}
	resp, err := s.api.GetMerkleRoot(ctx, internalReq)
	if err != nil {
		return nil, err
	}
	return &internalpb.GetMerkleRootResponse{MerkleRoot: resp.MerkleRoot}, nil
}

// ── Conversion helpers ────────────────────────────────────────────────────────

func internalSiblingToProto(sib SiblingResult) *pb.Sibling {
	return &pb.Sibling{
		Value:     sib.Value,
		Context:   vclockCtxToProto(sib.Context),
		Tombstone: sib.Tombstone,
		Timestamp: sib.Timestamp,
	}
}

func vclockCtxToProto(ctx *VClockContext) *pb.Context {
	if ctx == nil {
		return nil
	}
	pbCtx := &pb.Context{}
	for nodeID, counter := range ctx.Entries {
		pbCtx.Entries = append(pbCtx.Entries, &pb.ContextEntry{
			NodeId:  nodeID,
			Counter: counter,
		})
	}
	return pbCtx
}

func protoContextToVClockCtx(ctx *pb.Context) *VClockContext {
	if ctx == nil {
		return nil
	}
	entries := make(map[string]uint64, len(ctx.Entries))
	for _, e := range ctx.Entries {
		entries[e.NodeId] = e.Counter
	}
	return &VClockContext{Entries: entries}
}

func protoContextEntriesToInternal(entries []*internalpb.ContextEntry) []ContextEntryData {
	result := make([]ContextEntryData, 0, len(entries))
	for _, e := range entries {
		result = append(result, ContextEntryData{NodeID: e.NodeId, Counter: e.Counter})
	}
	return result
}

func internalSiblingDataToProto(sib SiblingData) *internalpb.SiblingData {
	pbSib := &internalpb.SiblingData{
		Value:     sib.Value,
		Tombstone: sib.Tombstone,
		Timestamp: sib.Timestamp,
	}
	for _, e := range sib.Context {
		pbSib.Context = append(pbSib.Context, &internalpb.ContextEntry{
			NodeId:  e.NodeID,
			Counter: e.Counter,
		})
	}
	return pbSib
}

// storageSiblingToVClockCtx converts a storage.Sibling's VClock to VClockContext
func storageSiblingToVClockCtx(vc vclock.VectorClock) *VClockContext {
	entries := make(map[string]uint64)
	for nodeID, counter := range vc.Entries() {
		entries[nodeID] = counter
	}
	return &VClockContext{Entries: entries}
}

// storageSiblingToSiblingResult converts a storage.Sibling to SiblingResult
func storageSiblingToSiblingResult(sib storage.Sibling) SiblingResult {
	return SiblingResult{
		Value:     sib.Value,
		Context:   storageSiblingToVClockCtx(sib.VClock),
		Tombstone: sib.Tombstone,
		Timestamp: sib.Timestamp,
	}
}
