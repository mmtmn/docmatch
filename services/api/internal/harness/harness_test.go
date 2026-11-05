package harness_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/mmtmn/docmatch/services/api/internal/harness"
	"github.com/mmtmn/docmatch/services/api/internal/harness/harnesstest"
	pb "github.com/mmtmn/docmatch/services/api/internal/harness/pb"
)

func TestClientAgainstFake(t *testing.T) {
	t.Parallel()
	fake := harnesstest.NewServer(harnesstest.MustLoadGolden(), harnesstest.Options{})
	sock, cleanup, err := harnesstest.SocketPath()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	stop, err := harnesstest.Serve(sock, fake)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	client, err := harness.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	h, err := client.Health(ctx)
	if err != nil || h.GetStatus() != pb.ServingStatus_SERVING_STATUS_SERVING {
		t.Fatalf("health = %v %v", h, err)
	}
	q, err := client.Estimate(ctx, &pb.EstimateRequest{Sources: []*pb.Source{{Id: "upl_1"}}})
	if err != nil || q.GetExpectedCredits() == 0 || len(q.GetBreakdown().GetItems()) == 0 {
		t.Fatalf("estimate = %v %v", q, err)
	}
	stream, err := client.Run(ctx, &pb.RunRequest{RunId: "run_1", Sources: []*pb.Source{{Id: "upl_a", Role: "protheus"}, {Id: "upl_b", Role: "omie"}, {Id: "upl_c", Role: "bank"}, {Id: "upl_d", Role: "ap_inbox"}}})
	if err != nil {
		t.Fatal(err)
	}
	var seq uint64
	terminal := ""
	for {
		ev, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if ev.GetSeq() <= seq {
			t.Fatalf("seq não monotônica: %d após %d", ev.GetSeq(), seq)
		}
		seq = ev.GetSeq()
		if c := ev.GetCompleted(); c != nil {
			terminal = "completed"
		}
	}
	if terminal != "completed" {
		t.Fatal("fluxo sem completed")
	}
	resp, err := client.ProposeRules(ctx, &pb.ProposeRulesRequest{ConfirmedFindings: []*pb.Finding{{Code: "twin.f01", Title: "x"}}})
	if err != nil || len(resp.GetRules()) != 1 {
		t.Fatalf("propose = %v %v", resp, err)
	}
}

func TestUnavailable(t *testing.T) {
	t.Parallel()
	c, err := harness.New("")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Health(context.Background()); !harness.IsUnavailable(err) {
		t.Fatalf("health = %v", err)
	}
	if _, err := c.Estimate(context.Background(), nil); !errors.Is(err, harness.ErrUnavailable) {
		t.Fatal("estimate")
	}
	if _, err := c.Run(context.Background(), nil); err == nil {
		t.Fatal("run")
	}
	if _, err := c.ProposeRules(context.Background(), nil); err == nil {
		t.Fatal("propose")
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := harness.Dial(""); !errors.Is(err, harness.ErrUnavailable) {
		t.Fatal("dial vazio")
	}
	// A socket nobody listens on: the first call reports unavailability.
	sock, cleanup, _ := harnesstest.SocketPath()
	defer cleanup()
	dead, err := harness.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dead.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := dead.Health(ctx); !harness.IsUnavailable(err) {
		t.Fatalf("socket morto = %v", err)
	}
	if !harness.IsUnavailable(status.Error(codes.Unavailable, "x")) || harness.IsUnavailable(status.Error(codes.InvalidArgument, "x")) || harness.IsUnavailable(nil) {
		t.Fatal("IsUnavailable")
	}
}
