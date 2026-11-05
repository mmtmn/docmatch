package wire

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/mmtmn/docmatch/services/api/internal/storage"
)

// namedStore answers Name, Bucket and Open; everything else is unused here.
type namedStore struct {
	storage.Store
	name, bucket string
}

func (n namedStore) Name() string   { return n.name }
func (n namedStore) Bucket() string { return n.bucket }
func (n namedStore) Open(_ context.Context, key string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(n.name + ":" + key)), nil
}

func TestOpenRemoteResolvesOnlyConfiguredStores(t *testing.T) {
	t.Parallel()
	s := &System{Stores: storage.Stores{
		Documents: namedStore{name: storage.NameS3, bucket: "docmatch-prod-uploads"},
		Media:     namedStore{name: storage.NameR2, bucket: "docmatch-prod-media"},
	}}
	cases := []struct {
		uri  string
		ok   bool
		body string
	}{
		{"s3://docmatch-prod-uploads/quarantine/org/a.csv", true, "s3:quarantine/org/a.csv"},
		{"r2://docmatch-prod-media/media/x.mp4", true, "r2:media/x.mp4"},
		{"s3://other-bucket/a.csv", false, ""},
		{"r2://docmatch-prod-uploads/a.csv", false, ""},
		{"local://media/a.csv", false, ""},
		{"s3://docmatch-prod-uploads/", false, ""},
		{"nonsense", false, ""},
	}
	for _, c := range cases {
		rc, ok, err := s.openRemote(context.Background(), c.uri)
		if err != nil || ok != c.ok {
			t.Fatalf("%s: ok=%v err=%v, want ok=%v", c.uri, ok, err, c.ok)
		}
		if !ok {
			continue
		}
		b, _ := io.ReadAll(rc)
		_ = rc.Close()
		if string(b) != c.body {
			t.Fatalf("%s: body %q, want %q", c.uri, b, c.body)
		}
	}
}
