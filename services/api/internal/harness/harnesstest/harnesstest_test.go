package harnesstest

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "github.com/mmtmn/docmatch/services/api/internal/harness/pb"
)

func TestGoldenScript(t *testing.T) {
	t.Parallel()
	s := MustLoadGolden()
	if len(s.Findings) != 23 || len(s.BankLines) != 18 || len(s.Titles) != 17 {
		t.Fatalf("gabarito: %d achados, %d linhas, %d títulos", len(s.Findings), len(s.BankLines), len(s.Titles))
	}
	if s.MatchedLines() != 15 || s.TotalAtRisk() <= 0 {
		t.Fatalf("matched=%d risco=%d", s.MatchedLines(), s.TotalAtRisk())
	}
	if s.sourceOfRef("bank:4") != "bank" || s.sourceOfRef("title:N02") != "omie" || s.sourceOfRef("title:T01") != "protheus" || s.sourceOfRef("email:2") != "ap_inbox" || s.sourceOfRef("chat:x") != "ap_inbox" {
		t.Fatal("sourceOfRef")
	}
	if Centavos(1234.565) != 123457 {
		t.Fatal("Centavos")
	}
	if _, err := ParseScript([]byte("findings: []")); err == nil {
		t.Fatal("gabarito vazio deveria falhar")
	}
	if _, err := ParseScript([]byte("[")); err == nil {
		t.Fatal("yaml inválido deveria falhar")
	}
}

func dial(t *testing.T, srv *Server) pb.HarnessClient {
	t.Helper()
	sock, cleanup, err := SocketPath()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	stop, err := Serve(sock, srv)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	conn, err := grpc.NewClient("unix:"+sock, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return pb.NewHarnessClient(conn)
}

func drain(t *testing.T, stream grpc.ServerStreamingClient[pb.RunEvent]) (findings, matches int, last *pb.RunEvent) {
	t.Helper()
	for {
		ev, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		last = ev
		switch ev.GetEvent().(type) {
		case *pb.RunEvent_Finding:
			findings++
			f := ev.GetFinding()
			if len(f.GetEvidence()) == 0 || f.GetId() == "" || f.GetCode() == "" {
				t.Fatalf("achado incompleto: %v", f)
			}
		case *pb.RunEvent_Match:
			matches++
		}
	}
}

func TestRunReplayPauseResumeAndFail(t *testing.T) {
	t.Parallel()
	script := MustLoadGolden()
	srv := NewServer(script, Options{PauseAfterFindings: 5})
	client := dial(t, srv)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req := &pb.RunRequest{RunId: "run_x", Sources: []*pb.Source{{Id: "upl_1"}, {Id: "upl_2"}, {Id: "upl_3"}, {Id: "upl_4"}}}
	stream, err := client.Run(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	findings, matches, last := drain(t, stream)
	if findings != 5 || matches != script.MatchedLines() || last.GetNeedsApproval() == nil {
		t.Fatalf("pausa: findings=%d matches=%d last=%v", findings, matches, last)
	}
	pausedSeq := last.GetSeq()
	if na := last.GetNeedsApproval(); na.GetQuote().GetCapCredits() <= na.GetQuote().GetExpectedCredits() || len(na.GetReasons()) != 1 {
		t.Fatalf("needs_approval = %v", na)
	}
	// Resume: the remaining findings, no repeated matches, seq continues.
	req.Resume = true
	stream, err = client.Run(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	findings, matches, last = drain(t, stream)
	if findings != len(script.Findings)-5 || matches != 0 || last.GetCompleted() == nil || last.GetSeq() <= pausedSeq {
		t.Fatalf("retomada: findings=%d matches=%d last=%v", findings, matches, last)
	}
	sum := last.GetCompleted().GetSummary()
	if int(sum.GetFindingsTotal()) != len(script.Findings) || sum.GetAmountAtRisk() != script.TotalAtRisk() || sum.GetLinesMatched() != 15 {
		t.Fatalf("resumo = %v", sum)
	}
	if len(srv.Runs()) != 2 || srv.Estimates() != 0 {
		t.Fatal("contadores")
	}
	// Budget pause: a tiny cap stops the run on the first spend.
	budget := NewServer(script, Options{})
	bclient := dial(t, budget)
	stream, err = bclient.Run(ctx, &pb.RunRequest{RunId: "run_b", Options: &pb.RunOptions{CreditBudget: 3}, Sources: []*pb.Source{{Id: "upl_1"}}})
	if err != nil {
		t.Fatal(err)
	}
	_, _, last = drain(t, stream)
	if last.GetNeedsApproval() == nil || last.GetNeedsApproval().GetReasons()[0] != pb.ApprovalReason_APPROVAL_REASON_OVER_DEFAULT_BUDGET {
		t.Fatalf("orçamento: %v", last)
	}
	// Failure.
	failing := NewServer(script, Options{FailAfterFindings: 2, FailCode: "run_timeout"})
	fclient := dial(t, failing)
	stream, err = fclient.Run(ctx, &pb.RunRequest{RunId: "run_f", Sources: []*pb.Source{{Id: "upl_1"}}})
	if err != nil {
		t.Fatal(err)
	}
	findings, _, last = drain(t, stream)
	if findings != 2 || last.GetFailed() == nil || last.GetFailed().GetCode() != "run_timeout" {
		t.Fatalf("falha: %d %v", findings, last)
	}
	// Replay behaves like a fresh run.
	stream2, err := fclient.Replay(ctx, &pb.ReplayRequest{RunId: "run_r", Sources: []*pb.Source{{Id: "upl_1"}}})
	if err != nil {
		t.Fatal(err)
	}
	if n, _, _ := drain(t, stream2); n != 2 {
		t.Fatalf("replay = %d achados", n)
	}
	if h, err := fclient.Health(ctx, &pb.HealthRequest{}); err != nil || h.GetStatus() != pb.ServingStatus_SERVING_STATUS_SERVING {
		t.Fatal("health")
	}
	props, err := fclient.ProposeRules(ctx, &pb.ProposeRulesRequest{MaxRules: 1, ConfirmedFindings: []*pb.Finding{{Code: "a", Title: "A"}, {Code: "b", Title: "B"}}})
	if err != nil || len(props.GetRules()) != 1 || props.GetRules()[0].GetCompiled().GetText() == "" || len(failing.Proposals()) != 1 {
		t.Fatalf("propose = %v %v", props, err)
	}
	q, err := fclient.Estimate(ctx, &pb.EstimateRequest{Sources: []*pb.Source{{Id: "m", Kind: pb.SourceKind_SOURCE_KIND_MEDIA, DurationMs: 120000}}})
	if err != nil || len(q.GetBreakdown().GetItems()) != 5 {
		t.Fatalf("estimate = %v %v", q, err)
	}
}
