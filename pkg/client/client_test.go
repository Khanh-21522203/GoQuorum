package client

import (
	"context"
	"errors"
	"net"
	"testing"

	pb "GoQuorum/api"
	"GoQuorum/internal/vclock"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// ── fake gRPC server ──────────────────────────────────────────────────────

type fakeGoQuorumServer struct {
	pb.UnimplementedGoQuorumServer
	getResp   *pb.GetResponse
	getErr    error
	putResp   *pb.PutResponse
	putErr    error
	deleteErr error
}

func (f *fakeGoQuorumServer) Get(_ context.Context, req *pb.GetRequest) (*pb.GetResponse, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	if f.getResp != nil {
		return f.getResp, nil
	}
	return nil, status.Error(codes.NotFound, "not found")
}

func (f *fakeGoQuorumServer) Put(_ context.Context, req *pb.PutRequest) (*pb.PutResponse, error) {
	if f.putErr != nil {
		return nil, f.putErr
	}
	if f.putResp != nil {
		return f.putResp, nil
	}
	// Return context echoed back with an extra tick
	ctx := req.GetContext()
	if ctx == nil {
		ctx = &pb.Context{}
	}
	return &pb.PutResponse{Context: ctx}, nil
}

func (f *fakeGoQuorumServer) Delete(_ context.Context, req *pb.DeleteRequest) (*pb.DeleteResponse, error) {
	if f.deleteErr != nil {
		return nil, f.deleteErr
	}
	return &pb.DeleteResponse{}, nil
}

// startFakeServer starts a gRPC server on a random port and returns the address + cleanup.
func startFakeServer(t *testing.T, srv pb.GoQuorumServer) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	grpcSrv := grpc.NewServer()
	pb.RegisterGoQuorumServer(grpcSrv, srv)
	go grpcSrv.Serve(lis)
	t.Cleanup(grpcSrv.GracefulStop)
	return lis.Addr().String()
}

func newTestClient(t *testing.T, addr string) *Client {
	t.Helper()
	cfg := DefaultClientConfig(addr)
	cfg.MaxRetries = 0 // most tests don't need retries
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// ── Tests ─────────────────────────────────────────────────────────────────

func TestClientGetReturnsErrKeyNotFound(t *testing.T) {
	addr := startFakeServer(t, &fakeGoQuorumServer{
		getErr: status.Error(codes.NotFound, "not found"),
	})
	c := newTestClient(t, addr)

	_, err := c.Get(context.Background(), []byte("k"))
	if !errors.Is(err, ErrKeyNotFound) {
		t.Errorf("expected ErrKeyNotFound, got %v", err)
	}
}

func TestClientGetReturnsSiblings(t *testing.T) {
	ctx := &pb.Context{Entries: []*pb.ContextEntry{{NodeId: "n1", Counter: 3}}}
	addr := startFakeServer(t, &fakeGoQuorumServer{
		getResp: &pb.GetResponse{
			Siblings: []*pb.Sibling{
				{Value: []byte("hello"), Context: ctx, Timestamp: 42},
			},
		},
	})
	c := newTestClient(t, addr)

	siblings, err := c.Get(context.Background(), []byte("k"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(siblings) != 1 {
		t.Fatalf("expected 1 sibling, got %d", len(siblings))
	}
	if string(siblings[0].Value) != "hello" {
		t.Errorf("wrong value: %s", siblings[0].Value)
	}
	if siblings[0].Timestamp != 42 {
		t.Errorf("wrong timestamp: %d", siblings[0].Timestamp)
	}
}

func TestClientPutReturnsNewContext(t *testing.T) {
	retCtx := &pb.Context{Entries: []*pb.ContextEntry{{NodeId: "n1", Counter: 5}}}
	addr := startFakeServer(t, &fakeGoQuorumServer{
		putResp: &pb.PutResponse{Context: retCtx},
	})
	c := newTestClient(t, addr)

	newVC, err := c.Put(context.Background(), []byte("k"), []byte("v"), vclock.NewVectorClock())
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if newVC.Get("n1") != 5 {
		t.Errorf("expected counter 5 for n1, got %d", newVC.Get("n1"))
	}
}

func TestClientDeleteSucceeds(t *testing.T) {
	addr := startFakeServer(t, &fakeGoQuorumServer{})
	c := newTestClient(t, addr)

	causal := vclock.NewVectorClock()
	causal.SetString("n1", 1)

	err := c.Delete(context.Background(), []byte("k"), causal)
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

func TestClientRetryOnUnavailable(t *testing.T) {
	attempts := 0
	fakeSrv := &fakeGoQuorumServer{}

	addr := startFakeServer(t, fakeSrv)
	cfg := DefaultClientConfig(addr)
	cfg.MaxRetries = 2
	cfg.RetryBaseDelay = 1 // minimal delay for test

	// Wrap stub to count calls
	conn, err := grpc.Dial(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	c := &Client{conn: conn, stub: &countingStub{inner: pb.NewGoQuorumClient(conn), count: &attempts}, config: cfg}

	fakeSrv.getErr = status.Error(codes.Unavailable, "unavailable")
	_, _ = c.Get(context.Background(), []byte("k"))

	// Should have retried MaxRetries+1 times
	if attempts != cfg.MaxRetries+1 {
		t.Errorf("expected %d attempts, got %d", cfg.MaxRetries+1, attempts)
	}
}

func TestClientNoRetryOnNotFound(t *testing.T) {
	attempts := 0
	fakeSrv := &fakeGoQuorumServer{
		getErr: status.Error(codes.NotFound, "not found"),
	}
	addr := startFakeServer(t, fakeSrv)
	cfg := DefaultClientConfig(addr)
	cfg.MaxRetries = 3

	conn, err := grpc.Dial(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	c := &Client{conn: conn, stub: &countingStub{inner: pb.NewGoQuorumClient(conn), count: &attempts}, config: cfg}
	_, _ = c.Get(context.Background(), []byte("k"))

	if attempts != 1 {
		t.Errorf("NotFound should not be retried, expected 1 attempt, got %d", attempts)
	}
}

// countingStub wraps a GoQuorumClient and counts Get calls.
type countingStub struct {
	inner pb.GoQuorumClient
	count *int
}

func (cs *countingStub) Get(ctx context.Context, in *pb.GetRequest, opts ...grpc.CallOption) (*pb.GetResponse, error) {
	(*cs.count)++
	return cs.inner.Get(ctx, in, opts...)
}
func (cs *countingStub) Put(ctx context.Context, in *pb.PutRequest, opts ...grpc.CallOption) (*pb.PutResponse, error) {
	return cs.inner.Put(ctx, in, opts...)
}
func (cs *countingStub) Delete(ctx context.Context, in *pb.DeleteRequest, opts ...grpc.CallOption) (*pb.DeleteResponse, error) {
	return cs.inner.Delete(ctx, in, opts...)
}
func (cs *countingStub) BatchGet(ctx context.Context, in *pb.BatchGetRequest, opts ...grpc.CallOption) (*pb.BatchGetResponse, error) {
	return cs.inner.BatchGet(ctx, in, opts...)
}
func (cs *countingStub) BatchPut(ctx context.Context, in *pb.BatchPutRequest, opts ...grpc.CallOption) (*pb.BatchPutResponse, error) {
	return cs.inner.BatchPut(ctx, in, opts...)
}

func TestLWWResolver(t *testing.T) {
	r := &LWWResolver{}

	vc1 := vclock.NewVectorClock()
	vc1.SetString("n1", 1)
	vc2 := vclock.NewVectorClock()
	vc2.SetString("n1", 2)

	siblings := []Sibling{
		{Value: []byte("old"), Context: vc1, Timestamp: 10},
		{Value: []byte("new"), Context: vc2, Timestamp: 20},
	}

	val, mergedCtx, err := r.Resolve(siblings)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if string(val) != "new" {
		t.Errorf("expected 'new', got %s", val)
	}
	// merged context should have counter 2 for n1
	if mergedCtx.Get("n1") != 2 {
		t.Errorf("expected counter 2 for n1 in merged context, got %d", mergedCtx.Get("n1"))
	}
}
