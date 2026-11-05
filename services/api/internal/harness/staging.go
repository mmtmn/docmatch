package harness

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"google.golang.org/protobuf/proto"

	pb "github.com/mmtmn/docmatch/services/api/internal/harness/pb"
)

// The harness reads only local files: it has no cloud credentials and no
// network. Sources kept in a remote store (s3://, r2://) are copied under the
// harness's local root (DM_MEDIA_LOCAL_DIR, shared by the API, the worker and
// the harness) for the duration of one call, their URIs rewritten to
// local://, and the copies removed when the call ends.

// stagingDir is the directory under the local root that holds the copies. The
// leading dot keeps it out of the local object store's key space (keys never
// start with a dot), so a stored object can never collide with a copy.
const stagingDir = ".staging"

// stagingLabel is the bucket label of the rewritten URIs (the harness ignores it).
const stagingLabel = "staging"

// DefaultStagingMaxBytes matches the harness's own per-source cap.
const DefaultStagingMaxBytes = 50 << 20

// ErrSourceTooLarge is returned when a remote source exceeds the staging cap.
var ErrSourceTooLarge = errors.New("harness: fonte maior que o limite")

// Opener opens the object named by a remote URI. ok is false when the URI is
// not one it serves; such sources are passed through unchanged and the harness
// refuses them with its own error.
type Opener func(ctx context.Context, uri string) (rc io.ReadCloser, ok bool, err error)

// Staged wraps a Client so that remote sources reach the harness as local copies.
type Staged struct {
	Client
	// Root is the harness's local root (DM_MEDIA_LOCAL_DIR).
	Root string
	// Open resolves remote URIs.
	Open Opener
	// MaxBytes bounds each copy (DefaultStagingMaxBytes when zero).
	MaxBytes int64
}

// Estimate implements Client.
func (s *Staged) Estimate(ctx context.Context, req *pb.EstimateRequest) (*pb.Quote, error) {
	sources, cleanup, err := s.stage(ctx, req.GetSources())
	if err != nil {
		return nil, err
	}
	defer cleanup()
	r, ok := proto.Clone(req).(*pb.EstimateRequest)
	if !ok {
		return nil, errors.New("harness: cópia do pedido de estimativa falhou")
	}
	r.Sources = sources
	return s.Client.Estimate(ctx, r)
}

// Run implements Client. The copies live until the stream ends or ctx is done.
func (s *Staged) Run(ctx context.Context, req *pb.RunRequest) (EventStream, error) {
	sources, cleanup, err := s.stage(ctx, req.GetSources())
	if err != nil {
		return nil, err
	}
	r, ok := proto.Clone(req).(*pb.RunRequest)
	if !ok {
		cleanup()
		return nil, errors.New("harness: cópia do pedido de execução falhou")
	}
	r.Sources = sources
	stream, err := s.Client.Run(ctx, r)
	if err != nil {
		cleanup()
		return nil, err
	}
	once := sync.OnceFunc(cleanup)
	stop := context.AfterFunc(ctx, once)
	return &stagedStream{EventStream: stream, done: func() { stop(); once() }}, nil
}

type stagedStream struct {
	EventStream
	done func()
}

// Recv removes the copies once the stream reports its end or an error.
func (s *stagedStream) Recv() (*pb.RunEvent, error) {
	ev, err := s.EventStream.Recv()
	if err != nil {
		s.done()
	}
	return ev, err
}

// stage copies the remote sources and returns the rewritten list (the input
// is not modified) and the function that removes the copies.
func (s *Staged) stage(ctx context.Context, sources []*pb.Source) ([]*pb.Source, func(), error) {
	out := make([]*pb.Source, len(sources))
	dir := ""
	cleanup := func() {
		if dir != "" {
			_ = os.RemoveAll(dir)
		}
	}
	for i, src := range sources {
		out[i] = src
		if strings.HasPrefix(src.GetUri(), "local://") || s.Open == nil {
			continue
		}
		rc, ok, err := s.Open(ctx, src.GetUri())
		if err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("harness: abrir a fonte %s: %w", src.GetId(), err)
		}
		if !ok {
			continue
		}
		if dir == "" {
			if dir, err = s.newDir(); err != nil {
				_ = rc.Close()
				return nil, nil, err
			}
		}
		// The copy is named by position, never by the user's file name; the
		// harness takes the name from Source.file_name.
		name := fmt.Sprintf("%03d", i)
		err = s.copy(filepath.Join(dir, name), rc)
		_ = rc.Close()
		if err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("harness: copiar a fonte %s: %w", src.GetId(), err)
		}
		c, ok := proto.Clone(src).(*pb.Source)
		if !ok {
			cleanup()
			return nil, nil, errors.New("harness: cópia da fonte falhou")
		}
		c.Uri = "local://" + stagingLabel + "/" + stagingDir + "/" + filepath.Base(dir) + "/" + name
		out[i] = c
	}
	return out, cleanup, nil
}

func (s *Staged) newDir() (string, error) {
	if s.Root == "" {
		return "", errors.New("harness: DM_MEDIA_LOCAL_DIR vazio; fontes remotas não podem ser preparadas")
	}
	base := filepath.Join(s.Root, stagingDir)
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", fmt.Errorf("harness: preparar %s: %w", base, err)
	}
	dir, err := os.MkdirTemp(base, "call-")
	if err != nil {
		return "", fmt.Errorf("harness: preparar %s: %w", base, err)
	}
	return dir, nil
}

func (s *Staged) copy(path string, r io.Reader) error {
	maxBytes := s.MaxBytes
	if maxBytes <= 0 {
		maxBytes = DefaultStagingMaxBytes
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // path is built from a temp dir and a counter
	if err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(r, maxBytes+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if n > maxBytes {
		return fmt.Errorf("%w (%d bytes)", ErrSourceTooLarge, maxBytes)
	}
	return nil
}
