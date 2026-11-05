package harness_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mmtmn/docmatch/services/api/internal/harness"
	pb "github.com/mmtmn/docmatch/services/api/internal/harness/pb"
)

// recorder is an inner Client that checks every staged source while the call is open.
type recorder struct {
	harness.Unavailable
	t       *testing.T
	root    string
	seen    []*pb.Source
	content map[string]string
	events  int
}

func (r *recorder) read(sources []*pb.Source) {
	r.seen = sources
	r.content = map[string]string{}
	for _, s := range sources {
		key, ok := strings.CutPrefix(s.GetUri(), "local://staging/")
		if !ok {
			continue // passed through unchanged
		}
		b, err := os.ReadFile(filepath.Join(r.root, filepath.FromSlash(key)))
		if err != nil {
			r.t.Errorf("staged copy of %s unreadable: %v", s.GetId(), err)
			continue
		}
		r.content[s.GetId()] = string(b)
	}
}

func (r *recorder) Estimate(_ context.Context, req *pb.EstimateRequest) (*pb.Quote, error) {
	r.read(req.GetSources())
	return &pb.Quote{}, nil
}

func (r *recorder) Run(_ context.Context, req *pb.RunRequest) (harness.EventStream, error) {
	r.read(req.GetSources())
	return &fixedStream{n: r.events}, nil
}

type fixedStream struct{ n int }

func (s *fixedStream) Recv() (*pb.RunEvent, error) {
	if s.n == 0 {
		return nil, io.EOF
	}
	s.n--
	return &pb.RunEvent{}, nil
}

func opener(objects map[string]string) harness.Opener {
	return func(_ context.Context, uri string) (io.ReadCloser, bool, error) {
		if !strings.HasPrefix(uri, "s3://") {
			return nil, false, nil
		}
		body, ok := objects[uri]
		if !ok {
			return nil, true, errors.New("not found")
		}
		return io.NopCloser(strings.NewReader(body)), true, nil
	}
}

func staged(t *testing.T, objects map[string]string) (*harness.Staged, *recorder) {
	t.Helper()
	root := t.TempDir()
	rec := &recorder{t: t, root: root, events: 2}
	return &harness.Staged{Client: rec, Root: root, Open: opener(objects)}, rec
}

func leftovers(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, ".staging"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestStagedEstimateCopiesRemoteSourcesAndCleansUp(t *testing.T) {
	t.Parallel()
	s, rec := staged(t, map[string]string{"s3://uploads/quarantine/a.csv": "data;valor\n"})
	req := &pb.EstimateRequest{Sources: []*pb.Source{
		{Id: "upl_s3", Uri: "s3://uploads/quarantine/a.csv", FileName: "a.csv"},
		{Id: "upl_local", Uri: "local://media/m/b.csv", FileName: "b.csv"},
		{Id: "upl_other", Uri: "gs://x/y", FileName: "c.csv"},
	}}
	if _, err := s.Estimate(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if got := rec.content["upl_s3"]; got != "data;valor\n" {
		t.Fatalf("staged content = %q", got)
	}
	uri := rec.seen[0].GetUri()
	if !strings.HasPrefix(uri, "local://staging/.staging/call-") || strings.Contains(uri, "a.csv") {
		t.Fatalf("staged uri = %q (named by position, never by the file name)", uri)
	}
	if rec.seen[0].GetFileName() != "a.csv" {
		t.Fatalf("file name must survive: %q", rec.seen[0].GetFileName())
	}
	if rec.seen[1].GetUri() != "local://media/m/b.csv" || rec.seen[2].GetUri() != "gs://x/y" {
		t.Fatalf("local and unknown sources must pass through: %v", rec.seen)
	}
	if req.GetSources()[0].GetUri() != "s3://uploads/quarantine/a.csv" {
		t.Fatal("the caller's request must not be modified")
	}
	if left := leftovers(t, s.Root); len(left) != 0 {
		t.Fatalf("copies left behind: %v", left)
	}
}

func TestStagedRunKeepsCopiesUntilTheStreamEnds(t *testing.T) {
	t.Parallel()
	s, rec := staged(t, map[string]string{"s3://uploads/q/a.csv": "x"})
	stream, err := s.Run(context.Background(), &pb.RunRequest{Sources: []*pb.Source{{Id: "upl_1", Uri: "s3://uploads/q/a.csv"}}})
	if err != nil {
		t.Fatal(err)
	}
	if rec.content["upl_1"] != "x" {
		t.Fatalf("content = %q", rec.content["upl_1"])
	}
	if _, err := stream.Recv(); err != nil {
		t.Fatal(err)
	}
	if left := leftovers(t, s.Root); len(left) != 1 {
		t.Fatalf("the copy must live while events flow, got %v", left)
	}
	for {
		if _, err := stream.Recv(); err != nil {
			break
		}
	}
	if left := leftovers(t, s.Root); len(left) != 0 {
		t.Fatalf("copies left after EOF: %v", left)
	}
}

func TestStagedRunCleansUpWhenTheContextEnds(t *testing.T) {
	t.Parallel()
	s, _ := staged(t, map[string]string{"s3://uploads/q/a.csv": "x"})
	ctx, cancel := context.WithCancel(context.Background())
	if _, err := s.Run(ctx, &pb.RunRequest{Sources: []*pb.Source{{Id: "upl_1", Uri: "s3://uploads/q/a.csv"}}}); err != nil {
		t.Fatal(err)
	}
	cancel()
	// AfterFunc runs in its own goroutine; wait for it by polling the directory.
	for range 200 {
		if len(leftovers(t, s.Root)) == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("copies left after cancel: %v", leftovers(t, s.Root))
}

func TestStagedRefusesOversizeAndMissingSources(t *testing.T) {
	t.Parallel()
	s, rec := staged(t, map[string]string{"s3://uploads/q/big.csv": strings.Repeat("a", 11)})
	s.MaxBytes = 10
	_, err := s.Estimate(context.Background(), &pb.EstimateRequest{Sources: []*pb.Source{{Id: "upl_big", Uri: "s3://uploads/q/big.csv"}}})
	if !errors.Is(err, harness.ErrSourceTooLarge) {
		t.Fatalf("err = %v, want ErrSourceTooLarge", err)
	}
	_, err = s.Estimate(context.Background(), &pb.EstimateRequest{Sources: []*pb.Source{{Id: "upl_gone", Uri: "s3://uploads/q/gone.csv"}}})
	if err == nil || !strings.Contains(err.Error(), "upl_gone") {
		t.Fatalf("err = %v, want the source id", err)
	}
	if rec.seen != nil {
		t.Fatal("the harness must not be called when staging fails")
	}
	if left := leftovers(t, s.Root); len(left) != 0 {
		t.Fatalf("copies left behind: %v", left)
	}
}

func TestStagedWithoutRootFailsClearly(t *testing.T) {
	t.Parallel()
	s, _ := staged(t, map[string]string{"s3://uploads/q/a.csv": "x"})
	s.Root = ""
	_, err := s.Estimate(context.Background(), &pb.EstimateRequest{Sources: []*pb.Source{{Id: "upl_1", Uri: "s3://uploads/q/a.csv"}}})
	if err == nil || !strings.Contains(err.Error(), "DM_MEDIA_LOCAL_DIR") {
		t.Fatalf("err = %v", err)
	}
}
