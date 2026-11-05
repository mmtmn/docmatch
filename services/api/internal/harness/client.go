// Package harness is the Go client of the reconciliation engine: gRPC over
// the Unix socket named by DM_HARNESS_SOCKET, with a timeout on every unary
// call and the caller's context bounding the Run stream. The generated stubs
// live in pb (make proto); harnesstest has an in-process fake server that
// replays the public Vantara twin.
package harness

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/status"

	pb "github.com/mmtmn/docmatch/services/api/internal/harness/pb"
)

// Call timeouts. Estimate and Health are interactive; ProposeRules compiles
// and back-tests rules with a model and takes longer.
const (
	EstimateTimeout     = 30 * time.Second
	HealthTimeout       = 5 * time.Second
	ProposeRulesTimeout = 5 * time.Minute
	// MaxMessageBytes bounds a single event or response from the harness.
	MaxMessageBytes = 16 << 20
)

// ErrUnavailable is returned when no harness is configured or reachable.
var ErrUnavailable = errors.New("harness: indisponível")

// EventStream yields the events of a Run or Replay call.
type EventStream interface {
	Recv() (*pb.RunEvent, error)
}

// Client is what the API and the worker need from the harness.
type Client interface {
	Estimate(ctx context.Context, req *pb.EstimateRequest) (*pb.Quote, error)
	Run(ctx context.Context, req *pb.RunRequest) (EventStream, error)
	ProposeRules(ctx context.Context, req *pb.ProposeRulesRequest) (*pb.ProposeRulesResponse, error)
	Health(ctx context.Context) (*pb.HealthResponse, error)
	Close() error
}

// GRPCClient is the Client over a gRPC connection.
type GRPCClient struct {
	conn *grpc.ClientConn
	stub pb.HarnessClient
}

// Dial prepares a connection to the Unix socket at path. gRPC connects
// lazily, so a missing socket surfaces as ErrUnavailable on the first call.
func Dial(path string) (*GRPCClient, error) {
	if path == "" {
		return nil, ErrUnavailable
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("harness: caminho do socket: %w", err)
	}
	conn, err := grpc.NewClient("unix:"+abs,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(MaxMessageBytes)),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{Time: 30 * time.Second, Timeout: 10 * time.Second, PermitWithoutStream: true}),
	)
	if err != nil {
		return nil, fmt.Errorf("harness: conectar: %w", err)
	}
	return &GRPCClient{conn: conn, stub: pb.NewHarnessClient(conn)}, nil
}

// Estimate implements Client.
func (c *GRPCClient) Estimate(ctx context.Context, req *pb.EstimateRequest) (*pb.Quote, error) {
	ctx, cancel := context.WithTimeout(ctx, EstimateTimeout)
	defer cancel()
	out, err := c.stub.Estimate(ctx, req)
	return out, wrap(err)
}

// Run implements Client. The stream lives as long as ctx.
func (c *GRPCClient) Run(ctx context.Context, req *pb.RunRequest) (EventStream, error) {
	stream, err := c.stub.Run(ctx, req)
	if err != nil {
		return nil, wrap(err)
	}
	return wrappedStream{stream}, nil
}

// ProposeRules implements Client.
func (c *GRPCClient) ProposeRules(ctx context.Context, req *pb.ProposeRulesRequest) (*pb.ProposeRulesResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, ProposeRulesTimeout)
	defer cancel()
	out, err := c.stub.ProposeRules(ctx, req)
	return out, wrap(err)
}

// Health implements Client.
func (c *GRPCClient) Health(ctx context.Context) (*pb.HealthResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, HealthTimeout)
	defer cancel()
	out, err := c.stub.Health(ctx, &pb.HealthRequest{})
	return out, wrap(err)
}

// Close implements Client.
func (c *GRPCClient) Close() error { return c.conn.Close() }

type wrappedStream struct {
	s grpc.ServerStreamingClient[pb.RunEvent]
}

func (w wrappedStream) Recv() (*pb.RunEvent, error) {
	ev, err := w.s.Recv()
	return ev, wrap(err)
}

// wrap turns transport failures into ErrUnavailable (kept in the chain).
func wrap(err error) error {
	if err == nil {
		return nil
	}
	if IsUnavailable(err) && !errors.Is(err, ErrUnavailable) {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return err
}

// IsUnavailable reports whether err means the harness cannot be reached.
func IsUnavailable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrUnavailable) {
		return true
	}
	switch status.Code(err) {
	case codes.Unavailable, codes.DeadlineExceeded, codes.Unimplemented:
		return true
	}
	return false
}

// Unavailable is the Client used when DM_HARNESS_SOCKET is empty: every
// call fails with ErrUnavailable and the API answers harness_unavailable.
type Unavailable struct{}

// Estimate implements Client.
func (Unavailable) Estimate(context.Context, *pb.EstimateRequest) (*pb.Quote, error) {
	return nil, ErrUnavailable
}

// Run implements Client.
func (Unavailable) Run(context.Context, *pb.RunRequest) (EventStream, error) {
	return nil, ErrUnavailable
}

// ProposeRules implements Client.
func (Unavailable) ProposeRules(context.Context, *pb.ProposeRulesRequest) (*pb.ProposeRulesResponse, error) {
	return nil, ErrUnavailable
}

// Health implements Client.
func (Unavailable) Health(context.Context) (*pb.HealthResponse, error) { return nil, ErrUnavailable }

// Close implements Client.
func (Unavailable) Close() error { return nil }

// New returns the gRPC client for socket, or Unavailable when it is empty.
func New(socket string) (Client, error) {
	if socket == "" {
		return Unavailable{}, nil
	}
	return Dial(socket)
}
