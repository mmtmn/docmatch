package uploads

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mmtmn/docmatch/services/api/internal/limits"
	"github.com/mmtmn/docmatch/services/api/internal/storage"
)

// ScanRunner produces the verdict of an upload. A nil verdict with a nil
// error means the verdict arrives asynchronously (production: the scanner
// Lambda writes it to the SQS queue).
type ScanRunner interface {
	Scan(ctx context.Context, u Upload, store storage.Store) (*Verdict, error)
}

// QueueScanner is the production runner: the scanner is triggered by the
// object landing in quarantine and answers through SQS, so there is
// nothing to do here.
type QueueScanner struct{}

// Scan implements ScanRunner.
func (QueueScanner) Scan(context.Context, Upload, storage.Store) (*Verdict, error) { return nil, nil }

// LocalScanner is the dev runner: it downloads the object and runs the
// dm-scanner binary when it is on PATH; otherwise it applies magic-byte,
// playlist and zip-bomb checks only and says so loudly in the log.
type LocalScanner struct {
	// Bin is the scanner binary name or path (DM_SCANNER_BIN).
	Bin     string
	Limits  *limits.Limits
	Logger  *slog.Logger
	Timeout time.Duration
	// TempDir receives the downloaded copies (default os.TempDir()).
	TempDir string
	// now is the clock (tests).
	now func() time.Time
}

// NewLocalScanner resolves the binary once and warns when it is absent.
func NewLocalScanner(bin string, l *limits.Limits, logger *slog.Logger) *LocalScanner {
	if logger == nil {
		logger = slog.Default()
	}
	if l == nil {
		l = limits.Must()
	}
	s := &LocalScanner{Bin: bin, Limits: l, Logger: logger, Timeout: 5 * time.Minute, now: time.Now}
	if _, err := s.binary(); err != nil {
		logger.Warn("NENHUM SCANNER DE MALWARE PRESENTE: dm-scanner não está no PATH; uploads passam só por verificação de bytes mágicos, listas de reprodução e bombas zip",
			slog.String("bin", bin))
	}
	return s
}

func (s *LocalScanner) binary() (string, error) {
	if s.Bin == "" {
		return "", errors.New("scanner não configurado")
	}
	return exec.LookPath(s.Bin)
}

// Scan implements ScanRunner.
func (s *LocalScanner) Scan(ctx context.Context, u Upload, store storage.Store) (*Verdict, error) {
	if s.now == nil {
		s.now = time.Now
	}
	path, sum, size, err := s.download(ctx, u, store)
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.Remove(path) }()

	if bin, err := s.binary(); err == nil {
		return s.runBinary(ctx, bin, path, u, sum, size)
	}
	s.Logger.Warn("scanner ausente: veredito por bytes mágicos apenas", slog.String("upload_id", u.PublicID()))
	return s.builtin(path, u, sum, size)
}

// download copies the object to a temp file, bounded by the declared size.
func (s *LocalScanner) download(ctx context.Context, u Upload, store storage.Store) (path string, sum string, size int64, err error) {
	rc, err := store.Open(ctx, u.StorageKey)
	if err != nil {
		return "", "", 0, err
	}
	defer func() { _ = rc.Close() }()
	f, err := os.CreateTemp(s.TempDir, "dm-scan-*")
	if err != nil {
		return "", "", 0, err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(rc, u.SizeBytes+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(f.Name())
		return "", "", 0, err
	}
	if n > u.SizeBytes {
		_ = os.Remove(f.Name())
		return "", "", 0, fmt.Errorf("uploads: objeto maior que o declarado (%d > %d)", n, u.SizeBytes)
	}
	return f.Name(), hex.EncodeToString(h.Sum(nil)), n, nil
}

func (s *LocalScanner) runBinary(ctx context.Context, bin, path string, u Upload, sum string, size int64) (*Verdict, error) {
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "scan", "--input", path, "--file-name", u.FileName, "--declared-mime", u.DeclaredMime, "--size", strconv.FormatInt(size, 10)) //nolint:gosec // bin resolved from config, args are ours
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &boundedWriter{w: &stderr, left: 4096}
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("uploads: dm-scanner falhou: %w (%s)", err, strings.TrimSpace(stderr.String()))
	}
	v, err := ParseVerdict(out.Bytes())
	if err != nil {
		return nil, err
	}
	if v.Verdict != VerdictReject && v.SHA256 != sum {
		return nil, fmt.Errorf("uploads: dm-scanner devolveu sha256 de outro objeto")
	}
	return &v, nil
}

// builtin is the no-binary fallback.
func (s *LocalScanner) builtin(path string, u Upload, sum string, size int64) (*Verdict, error) {
	v := &Verdict{
		Version: VerdictVersion, UploadID: u.PublicID(), Key: u.StorageKey, SHA256: sum, SizeBytes: size,
		Scanner: ScannerInfo{Name: "builtin-magic", Version: "1"}, ScannedAt: s.now().UTC(),
	}
	f, err := os.Open(path) //nolint:gosec // temp file we created
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	head := make([]byte, 4096)
	n, _ := io.ReadFull(f, head)
	head = head[:n]

	detected := Sniff(head)
	if detected.Family == familyText && IsPlaylist(head) {
		return reject(v, ReasonPlaylist, "lista de reprodução"), nil
	}
	declared := FamilyOf(u.DeclaredMime)
	if declared == familyUnknown {
		return reject(v, ReasonType, "tipo declarado não suportado"), nil
	}
	if !compatible(declared, detected.Family) {
		return reject(v, ReasonType, fmt.Sprintf("conteúdo %s não bate com o tipo declarado %s", detected.Mime, u.DeclaredMime)), nil
	}
	if detected.Family == familyZip {
		if reason := CheckZip(path, size, s.Limits); reason != "" {
			return reject(v, ReasonBomb, reason), nil
		}
	}
	// 7z has no reader in the Go standard library and the API takes no
	// dependency for one: the harness applies the archive limits of
	// limits.yaml (entries, uncompressed size, ratio, depth) when it opens it.
	v.Verdict = VerdictAccept
	v.Mime = detected.Mime
	if detected.Family == familyText || detected.Family == familyZip || detected.Family == family7z || detected.Family == familyOLE {
		// Text and container families keep the declared, more specific type.
		v.Mime = u.DeclaredMime
	}
	return v, nil
}

// boundedWriter keeps the first `left` bytes and drops the rest (stderr of
// the scanner in error messages).
type boundedWriter struct {
	w    io.Writer
	left int
}

func (b *boundedWriter) Write(p []byte) (int, error) {
	n := len(p)
	if b.left <= 0 {
		return n, nil
	}
	if len(p) > b.left {
		p = p[:b.left]
	}
	b.left -= len(p)
	_, err := b.w.Write(p)
	return n, err
}

func reject(v *Verdict, code, reason string) *Verdict {
	v.Verdict = VerdictReject
	v.ReasonCode = code
	v.Reason = reason
	return v
}

// Families of content, used to compare declared and detected types.
const (
	familyUnknown = ""
	familyPDF     = "pdf"
	familyZip     = "zip"
	family7z      = "7z"
	familyOLE     = "ole"
	familyText    = "text"
	familyImage   = "image"
	familyAudio   = "audio"
	familyVideo   = "video"
)

// Detected is the result of Sniff.
type Detected struct {
	Mime   string
	Family string
}

// documentMimes lists the document types the API accepts and their family.
var documentMimes = map[string]string{
	"application/pdf":              familyPDF,
	"application/zip":              familyZip,
	"application/x-zip-compressed": familyZip,
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":       familyZip,
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document": familyZip,
	"application/vnd.oasis.opendocument.spreadsheet":                          familyZip,
	"application/vnd.oasis.opendocument.text":                                 familyZip,
	"application/x-7z-compressed":                                             family7z,
	"application/vnd.ms-excel":                                                familyOLE,
	"application/msword":                                                      familyOLE,
	"text/csv":                                                                familyText,
	"text/plain":                                                              familyText,
	"text/tab-separated-values":                                               familyText,
	"text/html":                                                               familyText,
	"text/xml":                                                                familyText,
	"application/xml":                                                         familyText,
	"application/json":                                                        familyText,
	"message/rfc822":                                                          familyText,
	"application/mbox":                                                        familyText,
	"application/x-ofx":                                                       familyText,
	"application/vnd.ms-outlook":                                              familyOLE,
}

// FamilyOf classifies a declared MIME type; unknown types are not accepted.
func FamilyOf(mime string) string {
	mime = strings.ToLower(strings.TrimSpace(mime))
	if f, ok := documentMimes[mime]; ok {
		return f
	}
	switch {
	case strings.HasPrefix(mime, "image/"):
		return familyImage
	case strings.HasPrefix(mime, "audio/"):
		return familyAudio
	case strings.HasPrefix(mime, "video/"):
		return familyVideo
	}
	return familyUnknown
}

// AcceptedMime reports whether the API issues tickets for mime and kind.
func AcceptedMime(kind Kind, mime string) bool {
	f := FamilyOf(mime)
	switch kind {
	case KindDocument:
		return f == familyPDF || f == familyZip || f == family7z || f == familyOLE || f == familyText
	case KindMedia:
		return f == familyImage || f == familyAudio || f == familyVideo
	}
	return false
}

// Sniff detects the family of content from its first bytes.
func Sniff(head []byte) Detected {
	has := func(prefix string) bool { return bytes.HasPrefix(head, []byte(prefix)) }
	switch {
	case has("%PDF-"):
		return Detected{"application/pdf", familyPDF}
	case has("PK\x03\x04"), has("PK\x05\x06"):
		return Detected{"application/zip", familyZip}
	case has("7z\xBC\xAF\x27\x1C"):
		return Detected{"application/x-7z-compressed", family7z}
	case has("\xD0\xCF\x11\xE0\xA1\xB1\x1A\xE1"):
		return Detected{"application/vnd.ms-excel", familyOLE}
	case has("\xFF\xD8\xFF"):
		return Detected{"image/jpeg", familyImage}
	case has("\x89PNG\r\n\x1a\n"):
		return Detected{"image/png", familyImage}
	case has("GIF87a"), has("GIF89a"):
		return Detected{"image/gif", familyImage}
	case has("RIFF") && len(head) >= 12 && string(head[8:12]) == "WEBP":
		return Detected{"image/webp", familyImage}
	case has("RIFF") && len(head) >= 12 && string(head[8:12]) == "WAVE":
		return Detected{"audio/wav", familyAudio}
	case has("RIFF") && len(head) >= 12 && string(head[8:12]) == "AVI ":
		return Detected{"video/x-msvideo", familyVideo}
	case has("ID3"), has("\xFF\xFB"), has("\xFF\xF3"), has("\xFF\xF2"):
		return Detected{"audio/mpeg", familyAudio}
	case has("OggS"):
		return Detected{"audio/ogg", familyAudio}
	case has("fLaC"):
		return Detected{"audio/flac", familyAudio}
	case len(head) >= 12 && string(head[4:8]) == "ftyp":
		brand := string(head[8:12])
		if strings.HasPrefix(brand, "M4A") || strings.HasPrefix(brand, "M4B") {
			return Detected{"audio/mp4", familyAudio}
		}
		return Detected{"video/mp4", familyVideo}
	case has("\x1A\x45\xDF\xA3"):
		return Detected{"video/webm", familyVideo}
	}
	if isText(head) {
		return Detected{"text/plain", familyText}
	}
	return Detected{"application/octet-stream", familyUnknown}
}

// isText accepts UTF-8 (or a UTF-8 prefix cut mid-rune) without NUL bytes.
func isText(head []byte) bool {
	if len(head) == 0 {
		return false
	}
	if bytes.IndexByte(head, 0) >= 0 {
		return false
	}
	trimmed := head
	for i := 0; i < 3 && len(trimmed) > 0 && !utf8.Valid(trimmed); i++ {
		trimmed = trimmed[:len(trimmed)-1]
	}
	return utf8.Valid(trimmed)
}

// compatible decides whether a sniffed family may stand for a declared one.
func compatible(declared, detected string) bool {
	if declared == detected {
		return true
	}
	// Audio containers are shared with video (mp4/m4a, webm/ogg): accept
	// either direction between the two media families.
	if (declared == familyAudio && detected == familyVideo) || (declared == familyVideo && detected == familyAudio) {
		return true
	}
	return false
}

// IsPlaylist recognizes media playlists and link lists, which are refused
// (upload_rejected_playlist): the file is supposed to be the media itself.
func IsPlaylist(head []byte) bool {
	s := strings.TrimSpace(string(head))
	if strings.HasPrefix(s, "#EXTM3U") || strings.HasPrefix(strings.ToLower(s), "[playlist]") {
		return true
	}
	lines := strings.Split(s, "\n")
	links := 0
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		lower := strings.ToLower(l)
		if strings.HasPrefix(lower, "http:") || strings.HasPrefix(lower, "https:") || strings.HasPrefix(lower, "rtsp:") || strings.HasPrefix(lower, "rtmp:") {
			links++
			continue
		}
		return false
	}
	return links > 0
}

// CheckZip applies the zip limits of limits.yaml; it returns the reason of
// a bomb ("" when the archive is fine).
func CheckZip(path string, size int64, l *limits.Limits) string {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return "arquivo zip ilegível"
	}
	defer func() { _ = zr.Close() }()
	if len(zr.File) > l.Files.ZipMaxEntries {
		return fmt.Sprintf("%d entradas (máximo %d)", len(zr.File), l.Files.ZipMaxEntries)
	}
	var total uint64
	maxTotal := uint64(max(l.Files.ZipMaxUncompressedMB, 0)) << 20 //nolint:gosec // non-negative
	for _, f := range zr.File {
		total += f.UncompressedSize64
		if total > maxTotal {
			return fmt.Sprintf("descompactado acima de %d MB", l.Files.ZipMaxUncompressedMB)
		}
		if f.CompressedSize64 > 0 && f.UncompressedSize64/f.CompressedSize64 > uint64(l.Files.ZipMaxRatio) { //nolint:gosec // ratio is small and positive
			return fmt.Sprintf("razão de compressão acima de %dx", l.Files.ZipMaxRatio)
		}
		if l.Files.ZipMaxDepth <= 1 && strings.HasSuffix(strings.ToLower(f.Name), ".zip") {
			return "zip dentro de zip"
		}
		if strings.Contains(f.Name, "..") || filepath.IsAbs(f.Name) {
			return "caminho perigoso dentro do zip"
		}
	}
	if size > 0 && total/uint64(size) > uint64(l.Files.ZipMaxRatio) { //nolint:gosec // size is positive
		return fmt.Sprintf("razão de compressão acima de %dx", l.Files.ZipMaxRatio)
	}
	return ""
}
