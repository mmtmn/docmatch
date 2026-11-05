package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// S3Options configure NewS3. The same client serves AWS S3 (no endpoint,
// instance role), MinIO/RustFS in dev (endpoint + path style + static keys)
// and Cloudflare R2 (endpoint + static keys).
type S3Options struct {
	// Name is NameS3 or NameR2 (what uploads.store records).
	Name   string
	Bucket string
	Region string
	// Endpoint overrides the service URL; nil uses AWS.
	Endpoint *url.URL
	// PathStyle addresses buckets as /bucket/key (MinIO, RustFS).
	PathStyle bool
	// AccessKeyID/SecretAccessKey select static credentials; empty uses the
	// default chain (instance role in production).
	AccessKeyID     string
	SecretAccessKey string
	// Timeout bounds every API call (default 30s).
	Timeout time.Duration
}

// S3Store is a Store over the S3 API.
type S3Store struct {
	name    string
	bucket  string
	client  *s3.Client
	presign *s3.PresignClient
	timeout time.Duration
}

// NewS3 builds the client; it does not touch the network.
func NewS3(ctx context.Context, o S3Options) (*S3Store, error) {
	if o.Bucket == "" {
		return nil, errors.New("storage: bucket vazio")
	}
	if o.Name == "" {
		o.Name = NameS3
	}
	if o.Timeout <= 0 {
		o.Timeout = 30 * time.Second
	}
	loadOpts := []func(*awsconfig.LoadOptions) error{}
	if o.Region != "" {
		loadOpts = append(loadOpts, awsconfig.WithRegion(o.Region))
	}
	if o.AccessKeyID != "" || o.SecretAccessKey != "" {
		loadOpts = append(loadOpts, awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(o.AccessKeyID, o.SecretAccessKey, "")))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, loadOpts...)
	if err != nil {
		return nil, fmt.Errorf("storage: configuração AWS: %w", err)
	}
	if cfg.Region == "" {
		// R2 and local stores sign with the "auto" region; AWS needs a real one.
		cfg.Region = "auto"
	}
	client := s3.NewFromConfig(cfg, func(so *s3.Options) {
		if o.Endpoint != nil {
			so.BaseEndpoint = aws.String(o.Endpoint.String())
		}
		so.UsePathStyle = o.PathStyle
	})
	return &S3Store{name: o.Name, bucket: o.Bucket, client: client, presign: s3.NewPresignClient(client), timeout: o.Timeout}, nil
}

// Name implements Store.
func (s *S3Store) Name() string { return s.name }

// Bucket implements Store.
func (s *S3Store) Bucket() string { return s.bucket }

// URI implements Store.
func (s *S3Store) URI(key string) string { return s.name + "://" + s.bucket + "/" + key }

func (s *S3Store) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, s.timeout)
}

// PresignPost implements Store: the policy pins the key, the content type
// and a content-length-range of [1, maxSize].
func (s *S3Store) PresignPost(ctx context.Context, key string, maxSize int64, contentType string, ttl time.Duration) (PresignedPost, error) {
	if err := ValidateKey(key); err != nil {
		return PresignedPost{}, err
	}
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	input := &s3.PutObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)}
	if contentType != "" {
		input.ContentType = aws.String(contentType)
	}
	req, err := s.presign.PresignPostObject(ctx, input, func(po *s3.PresignPostOptions) {
		po.Expires = ttl
		po.Conditions = []interface{}{[]interface{}{"content-length-range", int64(1), maxSize}}
		if contentType != "" {
			po.Conditions = append(po.Conditions, map[string]string{"Content-Type": contentType})
		}
	})
	if err != nil {
		return PresignedPost{}, fmt.Errorf("storage: POST pré-assinado: %w", err)
	}
	fields := req.Values
	if contentType != "" {
		// The policy pins the type; the client must send the matching field.
		fields["Content-Type"] = contentType
	}
	return PresignedPost{URL: req.URL, Fields: fields, ExpiresAt: time.Now().Add(ttl)}, nil
}

// PresignPut implements Store: the signature covers the exact Content-Length
// and Content-Type, so a different body is refused by the store.
func (s *S3Store) PresignPut(ctx context.Context, key string, size int64, contentType string, ttl time.Duration) (PresignedPut, error) {
	if err := ValidateKey(key); err != nil {
		return PresignedPut{}, err
	}
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	input := &s3.PutObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key), ContentLength: aws.Int64(size)}
	if contentType != "" {
		input.ContentType = aws.String(contentType)
	}
	req, err := s.presign.PresignPutObject(ctx, input, s3.WithPresignExpires(ttl))
	if err != nil {
		return PresignedPut{}, fmt.Errorf("storage: PUT pré-assinado: %w", err)
	}
	headers := map[string]string{}
	for name, values := range req.SignedHeader {
		if strings.EqualFold(name, "host") || len(values) == 0 {
			continue
		}
		headers[http.CanonicalHeaderKey(name)] = values[0]
	}
	return PresignedPut{URL: req.URL, Headers: headers, ExpiresAt: time.Now().Add(ttl)}, nil
}

// Head implements Store.
func (s *S3Store) Head(ctx context.Context, key string) (Object, bool, error) {
	if err := ValidateKey(key); err != nil {
		return Object{}, false, err
	}
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		if isNotFound(err) {
			return Object{}, false, nil
		}
		return Object{}, false, fmt.Errorf("storage: head %s: %w", key, err)
	}
	return Object{Key: key, Size: aws.ToInt64(out.ContentLength), ETag: strings.Trim(aws.ToString(out.ETag), `"`), ContentType: aws.ToString(out.ContentType)}, true, nil
}

// Open implements Store. The returned body is not bound by the call timeout
// (the caller streams it); the caller's ctx still cancels it.
func (s *S3Store) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := ValidateKey(key); err != nil {
		return nil, err
	}
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		if isNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("storage: get %s: %w", key, err)
	}
	return out.Body, nil
}

// Put implements Store.
func (s *S3Store) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	if err := ValidateKey(key); err != nil {
		return err
	}
	input := &s3.PutObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key), Body: r, ContentLength: aws.Int64(size)}
	if contentType != "" {
		input.ContentType = aws.String(contentType)
	}
	if _, err := s.client.PutObject(ctx, input); err != nil {
		return fmt.Errorf("storage: put %s: %w", key, err)
	}
	return nil
}

// Delete implements Store.
func (s *S3Store) Delete(ctx context.Context, key string) error {
	if err := ValidateKey(key); err != nil {
		return err
	}
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	if _, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)}); err != nil && !isNotFound(err) {
		return fmt.Errorf("storage: delete %s: %w", key, err)
	}
	return nil
}

// isNotFound recognizes the three shapes a missing object takes in the SDK.
func isNotFound(err error) bool {
	var nf *types.NotFound
	var nsk *types.NoSuchKey
	if errors.As(err, &nf) || errors.As(err, &nsk) {
		return true
	}
	var ae smithy.APIError
	if errors.As(err, &ae) && (ae.ErrorCode() == "NotFound" || ae.ErrorCode() == "NoSuchKey") {
		return true
	}
	var re *awshttp.ResponseError
	return errors.As(err, &re) && re.HTTPStatusCode() == http.StatusNotFound
}
