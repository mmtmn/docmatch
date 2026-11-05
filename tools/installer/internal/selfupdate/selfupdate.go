// Package selfupdate replaces the running docmatch binary with a newer release from the same
// download base install.sh uses (<site>/install): VERSION.txt names the latest release,
// SHA256SUMS.txt and docmatch-<os>-<arch>.{bin,exe} sit next to it, and pinned releases live
// under v<version>/. The new binary is verified against the checksum and asked for its version
// before it replaces the old one with a rename (atomic on Unix; on Windows, where a running
// .exe cannot be overwritten but can be renamed, the old one moves aside first and is removed on
// the next start). Remove deletes the binary at the end of `docmatch uninstall`.
package selfupdate

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mmtmn/docmatch/tools/installer/internal/docker"
	"github.com/mmtmn/docmatch/tools/installer/internal/text"
)

// MaxBinary bounds a download (the binaries are ~10 MB).
const MaxBinary = 200 << 20

// AssetName is the release file of a system. The names carry an extension because the site's
// CloudFront function appends .html to paths without a dot.
func AssetName(goos, goarch string) string {
	ext := ".bin"
	if goos == "windows" {
		ext = ".exe"
	}
	return "docmatch-" + goos + "-" + goarch + ext
}

// File is a file being written.
type File interface {
	io.Writer
	Name() string
	Close() error
}

// FS is what an update or a removal touches. OSFS is the real one; tests use a fake that
// behaves like Windows anywhere (a running executable can be renamed, not overwritten or
// deleted).
type FS interface {
	CreateTemp(dir, pattern string) (File, error)
	Rename(oldpath, newpath string) error
	Remove(name string) error
	Chmod(name string, mode fs.FileMode) error
}

// OSFS is the machine's filesystem.
type OSFS struct{}

// CreateTemp is os.CreateTemp.
func (OSFS) CreateTemp(dir, pattern string) (File, error) { return os.CreateTemp(dir, pattern) }

// Rename is os.Rename.
func (OSFS) Rename(oldpath, newpath string) error { return os.Rename(oldpath, newpath) }

// Remove is os.Remove.
func (OSFS) Remove(name string) error { return os.Remove(name) }

// Chmod is os.Chmod.
func (OSFS) Chmod(name string, mode fs.FileMode) error { return os.Chmod(name, mode) }

// Errors the caller explains.
var (
	ErrNoBase   = errors.New(text.T("update.err_no_base"))
	ErrInsecure = errors.New(text.T("update.err_https"))
	ErrChecksum = errors.New(text.T("update.err_checksum"))
	ErrVersion  = errors.New(text.T("update.err_version"))
	// ErrStranded: on Windows neither the new binary nor the old one is in place.
	ErrStranded = errors.New(text.T("update.err_stranded"))
)

// Updater replaces Exe with a release from Base.
type Updater struct {
	Base    string // download base: VERSION.txt and the latest binaries; v<version>/ pinned ones
	Current string // the running version ("0.1.0", "dev")
	Exe     string // the running binary (symlinks resolved)
	GOOS    string
	GOARCH  string
	HTTP    *http.Client
	FS      FS
	// Probe runs a downloaded binary with --version and returns what it printed.
	Probe func(ctx context.Context, path string) (string, error)
	// AllowLoopbackHTTP lets the tests serve a release over http on 127.0.0.1 (httptest);
	// anything else must be https, redirects included.
	AllowLoopbackHTTP bool
}

// Outcome of an update.
type Outcome struct {
	From, To string
	Updated  bool // false: already at To
}

// allowed reports whether raw may be fetched: https only (the binary replaces this one).
func (u *Updater) allowed(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return false
	}
	if parsed.Scheme == "https" {
		return true
	}
	if !u.AllowLoopbackHTTP || parsed.Scheme != "http" {
		return false
	}
	ip := net.ParseIP(parsed.Hostname())
	return parsed.Hostname() == "localhost" || ip != nil && ip.IsLoopback()
}

// client is the caller's client (or a default one) refusing redirects that leave https.
func (u *Updater) client() *http.Client {
	c := &http.Client{Timeout: 5 * time.Minute}
	if u.HTTP != nil {
		copied := *u.HTTP
		c = &copied
	}
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New(text.T("update.err_redirects"))
		}
		if !u.allowed(req.URL.String()) {
			return fmt.Errorf("%w: %s", ErrInsecure, req.URL.Redacted())
		}
		return nil
	}
	return c
}

func (u *Updater) get(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := u.client().Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		_ = res.Body.Close()
		return nil, errors.New(text.T("update.err_status", "url", url, "code", res.StatusCode))
	}
	return res, nil
}

func (u *Updater) small(ctx context.Context, url string) (string, error) {
	res, err := u.get(ctx, url)
	if err != nil {
		return "", err
	}
	defer func() { _ = res.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	return string(data), err
}

func (u *Updater) base() (string, error) {
	b := strings.TrimRight(strings.TrimSpace(u.Base), "/")
	if b == "" {
		return "", ErrNoBase
	}
	if !u.allowed(b) {
		return "", fmt.Errorf("%w: %s", ErrInsecure, b)
	}
	return b, nil
}

// Latest is the version VERSION.txt announces.
func (u *Updater) Latest(ctx context.Context) (string, error) {
	b, err := u.base()
	if err != nil {
		return "", err
	}
	v, err := u.small(ctx, b+"/VERSION.txt")
	if err != nil {
		return "", err
	}
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if v == "" || strings.ContainsAny(v, " /\\\n") {
		return "", errors.New(text.T("update.err_version_file", "base", b))
	}
	return v, nil
}

// Newer reports whether version a is newer than b ("0.2.0" > "0.1.9"); "dev" and other
// non-versions are never newer.
func Newer(a, b string) bool {
	if !isVersion(a) || !isVersion(b) {
		return false
	}
	return a != b && docker.AtLeast(a, b) && !docker.AtLeast(b, a)
}

func isVersion(v string) bool {
	return v != "" && v[0] >= '0' && v[0] <= '9' && strings.Contains(v, ".")
}

// Expected finds name's checksum in a SHA256SUMS.txt ("<hex>  <name>" or "<hex> *<name>").
func Expected(sums, name string) string {
	sc := bufio.NewScanner(strings.NewReader(sums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name && len(f[0]) == 64 {
			return strings.ToLower(f[0])
		}
	}
	return ""
}

// Update installs version (the latest when ""), unless it is already the running one and force
// is off. say reports each phase in pt-BR.
func (u *Updater) Update(ctx context.Context, version string, force bool, say func(string)) (Outcome, error) {
	if say == nil {
		say = func(string) {}
	}
	b, err := u.base()
	if err != nil {
		return Outcome{}, err
	}
	out := Outcome{From: u.Current}
	pinned := strings.TrimSpace(version) != ""
	if version = strings.TrimPrefix(strings.TrimSpace(version), "v"); version == "" {
		if version, err = u.Latest(ctx); err != nil {
			return out, err
		}
	} else {
		b += "/v" + version
	}
	out.To = version
	if version == u.Current && !force {
		return out, nil
	}
	// The latest is older than this one (a stale VERSION.txt, an earlier --to): never go back
	// without --to or --force.
	if !pinned && !force && Newer(u.Current, version) {
		out.To = u.Current
		return out, nil
	}
	name := AssetName(u.GOOS, u.GOARCH)
	say("SHA256SUMS.txt")
	sums, err := u.small(ctx, b+"/SHA256SUMS.txt")
	if err != nil {
		return out, err
	}
	want := Expected(sums, name)
	if want == "" {
		return out, errors.New(text.T("update.err_not_listed", "name", name, "base", b))
	}
	say(name)
	tmp, err := u.download(ctx, b+"/"+name, want)
	if err != nil {
		return out, err
	}
	keep := false
	defer func() {
		if !keep {
			_ = u.FS.Remove(tmp)
		}
	}()
	if u.Probe != nil {
		got, err := u.Probe(ctx, tmp)
		if err != nil {
			return out, fmt.Errorf("%s: %w", text.T("update.err_probe"), err)
		}
		if strings.TrimSpace(got) != "docmatch "+version {
			return out, fmt.Errorf("%w: %s", ErrVersion, text.T("update.err_probe_says", "want", version, "got", strings.TrimSpace(got)))
		}
	}
	if err := u.Replace(tmp); err != nil {
		if errors.Is(err, ErrStranded) {
			keep = true // the only good binary left: say where it is
			return out, fmt.Errorf("%w; %s", err, text.T("update.err_new_at", "path", tmp))
		}
		return out, err
	}
	keep = true
	out.Updated = true
	return out, nil
}

// download writes url next to the running binary (same filesystem: the rename is atomic) and
// checks its SHA-256 on the way.
func (u *Updater) download(ctx context.Context, url, want string) (string, error) {
	res, err := u.get(ctx, url)
	if err != nil {
		return "", err
	}
	defer func() { _ = res.Body.Close() }()
	pattern := ".docmatch-update-*"
	if u.GOOS == "windows" {
		pattern += ".exe"
	}
	f, err := u.FS.CreateTemp(filepath.Dir(u.Exe), pattern)
	if err != nil {
		return "", fmt.Errorf("%s: %w", text.T("update.err_write", "path", u.Exe), err)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(res.Body, MaxBinary+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > MaxBinary {
		err = errors.New(text.T("update.err_too_big", "url", url, "max", MaxBinary>>20))
	}
	if err == nil {
		if got := hex.EncodeToString(h.Sum(nil)); got != want {
			err = fmt.Errorf("%w %s", ErrChecksum, text.T("update.err_mismatch", "want", want, "got", got))
		}
	}
	if err == nil && u.GOOS != "windows" {
		err = u.FS.Chmod(f.Name(), 0o755)
	}
	if err != nil {
		_ = u.FS.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// OldPath is where Windows keeps the replaced binary until the next start.
func OldPath(exe string) string { return exe + ".old" }

// Replace puts newPath in place of the running binary.
func (u *Updater) Replace(newPath string) error {
	if u.GOOS != "windows" {
		return u.FS.Rename(newPath, u.Exe)
	}
	old := OldPath(u.Exe)
	_ = u.FS.Remove(old) // a leftover of an earlier update
	if err := u.FS.Rename(u.Exe, old); err != nil {
		return fmt.Errorf("%s: %w", text.T("update.err_move_aside", "path", u.Exe), err)
	}
	if err := u.FS.Rename(newPath, u.Exe); err != nil {
		if rerr := u.FS.Rename(old, u.Exe); rerr != nil {
			return fmt.Errorf("%w: %w; %s: %w", ErrStranded, err, text.T("update.err_not_back", "path", old), rerr)
		}
		return err
	}
	_ = u.FS.Remove(old) // fails while it runs: Cleanup removes it on the next start
	return nil
}

// Cleanup removes what an earlier update left behind on Windows.
func Cleanup(fsys FS, exe string) {
	if exe != "" {
		_ = fsys.Remove(OldPath(exe))
	}
}

// Managed tells who else owns the binary: "brew" (a Homebrew keg), "winget" (a portable
// package), "go" (a `go run` build) or "" (install.sh, install.ps1, a copy).
func Managed(exe string) string {
	p := strings.ReplaceAll(exe, `\`, "/") // Windows paths too, whatever the host
	switch {
	case strings.Contains(p, "/Cellar/"):
		return "brew"
	case strings.Contains(strings.ToLower(p), "/winget/packages/"):
		return "winget"
	case strings.Contains(p, "/go-build"):
		return "go"
	}
	return ""
}

// Remover deletes the running binary.
type Remover struct {
	FS   FS
	GOOS string
	// Later deletes a path after this process exits (Windows: a detached cmd.exe).
	Later func(path string) error
}

// Remove deletes exe. Unix removes a running binary at once; Windows moves it aside and has it
// deleted after this process ends.
func (r Remover) Remove(exe string) error {
	if r.GOOS != "windows" {
		return r.FS.Remove(exe)
	}
	if err := r.FS.Remove(exe); err == nil {
		return nil
	}
	old := OldPath(exe)
	_ = r.FS.Remove(old)
	if err := r.FS.Rename(exe, old); err != nil {
		return err
	}
	later := r.Later
	if later == nil {
		later = deleteLater
	}
	return later(old)
}
