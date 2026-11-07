package storage

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// LocalPath is where the API mounts the local store's HTTP handler. It
// exists only in dev and tests, when R2 is not configured.
const LocalPath = "/v1/_local/blobs/"

// OriginExempt recognizes a credential-free browser write authorized by this
// local store's own signature. Only wire this exception in development: the
// storage handler still verifies the signature and enforces the object size.
func (l *LocalStore) OriginExempt(r *http.Request) bool {
	if l == nil || r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" ||
		(r.Method != http.MethodPost && r.Method != http.MethodPut) || !strings.HasPrefix(r.URL.Path, LocalPath) {
		return false
	}
	key := strings.TrimPrefix(r.URL.Path, LocalPath)
	if ValidateKey(key) != nil {
		return false
	}
	q := r.URL.Query()
	if len(q["exp"]) != 1 || len(q["max"]) != 1 || len(q["sig"]) != 1 {
		return false
	}
	exp, err := strconv.ParseInt(q.Get("exp"), 10, 64)
	if err != nil || l.now().Unix() > exp {
		return false
	}
	max, err := strconv.ParseInt(q.Get("max"), 10, 64)
	if err != nil || max < 0 {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(l.sign(r.Method, key, exp, max)), []byte(q.Get("sig"))) == 1
}

// LocalStore keeps objects under a directory and serves presigned-style
// upload and download URLs itself: the signature (HMAC with a random
// per-process key) covers key, method, expiry and size ceiling, so the URLs
// behave like the real ones (bound to one object, one size, one deadline).
type LocalStore struct {
	root    string
	bucket  string
	baseURL *url.URL
	key     []byte
	now     func() time.Time
}

// NewLocal creates root if needed. baseURL is the public base of the API
// (DM_API_BASE_URL) the URLs are built on.
func NewLocal(root, bucket string, baseURL *url.URL) (*LocalStore, error) {
	if root == "" || baseURL == nil {
		return nil, errors.New("storage: pasta local e URL base são obrigatórias")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0o750); err != nil {
		return nil, fmt.Errorf("storage: criar %s: %w", abs, err)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if bucket == "" {
		bucket = filepath.Base(abs)
	}
	return &LocalStore{root: abs, bucket: bucket, baseURL: baseURL, key: key, now: time.Now}, nil
}

// Name implements Store.
func (l *LocalStore) Name() string { return NameLocal }

// Bucket implements Store.
func (l *LocalStore) Bucket() string { return l.bucket }

// URI implements Store.
func (l *LocalStore) URI(key string) string { return NameLocal + "://" + l.bucket + "/" + key }

// Root is the directory objects live in.
func (l *LocalStore) Root() string { return l.root }

func (l *LocalStore) path(key string) (string, error) {
	if err := ValidateKey(key); err != nil {
		return "", err
	}
	return filepath.Join(l.root, filepath.FromSlash(key)), nil
}

func (l *LocalStore) sign(method, key string, exp int64, max int64) string {
	mac := hmac.New(sha256.New, l.key)
	_, _ = fmt.Fprintf(mac, "%s\n%s\n%d\n%d", method, key, exp, max)
	return hex.EncodeToString(mac.Sum(nil))
}

func (l *LocalStore) signedURL(method, key string, max int64, ttl time.Duration) (string, time.Time) {
	exp := l.now().Add(ttl)
	u := *l.baseURL
	u.Path = strings.TrimSuffix(u.Path, "/") + LocalPath + key
	q := url.Values{}
	q.Set("exp", strconv.FormatInt(exp.Unix(), 10))
	q.Set("max", strconv.FormatInt(max, 10))
	q.Set("sig", l.sign(method, key, exp.Unix(), max))
	u.RawQuery = q.Encode()
	return u.String(), exp
}

// PresignPost implements Store (multipart/form-data with a "file" field).
func (l *LocalStore) PresignPost(_ context.Context, key string, maxSize int64, _ string, ttl time.Duration) (PresignedPost, error) {
	if err := ValidateKey(key); err != nil {
		return PresignedPost{}, err
	}
	u, exp := l.signedURL(http.MethodPost, key, maxSize, ttl)
	return PresignedPost{URL: u, Fields: map[string]string{"key": key}, ExpiresAt: exp}, nil
}

// PresignPut implements Store.
func (l *LocalStore) PresignPut(_ context.Context, key string, size int64, contentType string, ttl time.Duration) (PresignedPut, error) {
	if err := ValidateKey(key); err != nil {
		return PresignedPut{}, err
	}
	u, exp := l.signedURL(http.MethodPut, key, size, ttl)
	headers := map[string]string{"Content-Length": strconv.FormatInt(size, 10)}
	if contentType != "" {
		headers["Content-Type"] = contentType
	}
	return PresignedPut{URL: u, Headers: headers, ExpiresAt: exp}, nil
}

// Head implements Store.
func (l *LocalStore) Head(_ context.Context, key string) (Object, bool, error) {
	p, err := l.path(key)
	if err != nil {
		return Object{}, false, err
	}
	st, err := os.Stat(p)
	if errors.Is(err, os.ErrNotExist) {
		return Object{}, false, nil
	}
	if err != nil {
		return Object{}, false, err
	}
	return Object{Key: key, Size: st.Size()}, true, nil
}

// Open implements Store.
func (l *LocalStore) Open(_ context.Context, key string) (io.ReadCloser, error) {
	p, err := l.path(key)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p) //nolint:gosec // p is root + a key validated by ValidateKey (no "..", safe charset)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	return f, err
}

// Put implements Store: written to a temp file and renamed, so readers
// never see a partial object.
func (l *LocalStore) Put(_ context.Context, key string, r io.Reader, size int64, _ string) error {
	p, err := l.path(key)
	if err != nil {
		return err
	}
	// p is root + a key validated by ValidateKey (no "..", safe charset).
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil { //nolint:gosec // see above
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".upload-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }() //nolint:gosec // temp file we created under root
	src := r
	if size >= 0 {
		src = io.LimitReader(r, size+1)
	}
	n, err := io.Copy(tmp, src)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if size >= 0 && n != size {
		return fmt.Errorf("storage: tamanho %d difere do declarado %d", n, size)
	}
	return os.Rename(tmp.Name(), p) //nolint:gosec // both paths live under root
}

// Delete implements Store.
func (l *LocalStore) Delete(_ context.Context, key string) error {
	p, err := l.path(key)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// ServeHTTP handles the URLs issued by PresignPost/PresignPut: PUT (raw
// body), POST (multipart "file" field) and GET. Mount it at LocalPath.
func (l *LocalStore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, LocalPath)
	if i := strings.Index(r.URL.Path, LocalPath); i > 0 {
		key = r.URL.Path[i+len(LocalPath):]
	}
	if err := ValidateKey(key); err != nil {
		http.Error(w, "chave inválida", http.StatusBadRequest)
		return
	}
	q := r.URL.Query()
	exp, err := strconv.ParseInt(q.Get("exp"), 10, 64)
	if err != nil {
		http.Error(w, "assinatura inválida", http.StatusForbidden)
		return
	}
	max, err := strconv.ParseInt(q.Get("max"), 10, 64)
	if err != nil {
		http.Error(w, "assinatura inválida", http.StatusForbidden)
		return
	}
	want := l.sign(r.Method, key, exp, max)
	if subtle.ConstantTimeCompare([]byte(want), []byte(q.Get("sig"))) != 1 {
		http.Error(w, "assinatura inválida", http.StatusForbidden)
		return
	}
	if l.now().Unix() > exp {
		http.Error(w, "URL expirada", http.StatusForbidden)
		return
	}
	switch r.Method {
	case http.MethodPut:
		if r.ContentLength < 0 || r.ContentLength > max {
			http.Error(w, "tamanho não permitido", http.StatusRequestEntityTooLarge)
			return
		}
		if err := l.Put(r.Context(), key, http.MaxBytesReader(w, r.Body, max), r.ContentLength, r.Header.Get("Content-Type")); err != nil {
			http.Error(w, "falha ao gravar", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	case http.MethodPost:
		mr, err := r.MultipartReader()
		if err != nil {
			http.Error(w, "multipart inválido", http.StatusBadRequest)
			return
		}
		for {
			part, err := mr.NextPart()
			if errors.Is(err, io.EOF) {
				http.Error(w, "campo file ausente", http.StatusBadRequest)
				return
			}
			if err != nil {
				http.Error(w, "multipart inválido", http.StatusBadRequest)
				return
			}
			if part.FormName() != "file" {
				_, _ = io.Copy(io.Discard, io.LimitReader(part, 1<<20))
				continue
			}
			// -1: unknown size; the limit reader bounds it at max.
			if err := l.Put(r.Context(), key, io.LimitReader(part, max), -1, part.Header.Get("Content-Type")); err != nil {
				http.Error(w, "falha ao gravar", http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
	case http.MethodGet:
		f, err := l.Open(r.Context(), key)
		if err != nil {
			http.Error(w, "não encontrado", http.StatusNotFound)
			return
		}
		defer func() { _ = f.Close() }()
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = io.Copy(w, f)
	default:
		http.Error(w, "método não permitido", http.StatusMethodNotAllowed)
	}
}
