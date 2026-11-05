// Package webserve serves the static site build (apps/web/dist) inside the web image of
// deploy/local the way production serves it: clean URLs map to `<path>.html`, `/` to index.html
// and unknown paths to 404.html with status 404 (the same rules as the CloudFront viewer-request
// function and apps/web/tools/serve-dist.mjs).
//
// The image is built once and must work on any port, so the site is compiled with placeholder
// origins (SitePlaceholder, APIPlaceholder) and the server swaps them for the real origins when
// it starts. Files that carried a placeholder are served from memory with `no-cache`, so a
// browser never keeps a bundle that points at an old API port.
package webserve

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	// SitePlaceholder is the DM_PUBLIC_BASE_URL the site is built with.
	SitePlaceholder = "http://dm-site.invalid"
	// APIPlaceholder is the PUBLIC_DM_API_BASE_URL the site is built with.
	APIPlaceholder = "http://dm-api.invalid"
)

// textExt lists the extensions scanned for placeholders.
var textExt = map[string]bool{
	".html": true, ".js": true, ".mjs": true, ".css": true, ".json": true, ".map": true,
	".xml": true, ".txt": true, ".webmanifest": true, ".svg": true,
}

var contentTypes = map[string]string{
	".html":        "text/html; charset=utf-8",
	".css":         "text/css; charset=utf-8",
	".js":          "text/javascript; charset=utf-8",
	".mjs":         "text/javascript; charset=utf-8",
	".json":        "application/json; charset=utf-8",
	".map":         "application/json; charset=utf-8",
	".xml":         "application/xml; charset=utf-8",
	".txt":         "text/plain; charset=utf-8",
	".webmanifest": "application/manifest+json; charset=utf-8",
	".svg":         "image/svg+xml",
	".png":         "image/png",
	".jpg":         "image/jpeg",
	".jpeg":        "image/jpeg",
	".gif":         "image/gif",
	".webp":        "image/webp",
	".avif":        "image/avif",
	".ico":         "image/x-icon",
	".woff":        "font/woff",
	".woff2":       "font/woff2",
	".ttf":         "font/ttf",
	".wav":         "audio/wav",
	".mp3":         "audio/mpeg",
	".m4a":         "audio/mp4",
	".mp4":         "video/mp4",
	".webm":        "video/webm",
	".pdf":         "application/pdf",
}

// Handler serves one directory.
type Handler struct {
	root     string
	rendered map[string][]byte // site path ("/app.html") -> content with real origins
	modTime  time.Time
}

// New prepares root for serving. replace maps each placeholder to its real value; every text
// file that contains one is rendered once, here, and kept in memory.
func New(root string, replace map[string]string) (*Handler, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(abs, "index.html")); err != nil {
		return nil, fmt.Errorf("webserve: %s/index.html não existe: %w", abs, err)
	}
	h := &Handler{root: abs, rendered: map[string][]byte{}, modTime: time.Now()}
	if len(replace) == 0 {
		return h, nil
	}
	pairs := make([]string, 0, len(replace)*2)
	for from, to := range replace {
		pairs = append(pairs, from, to)
	}
	replacer := strings.NewReplacer(pairs...)
	err = filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !textExt[strings.ToLower(filepath.Ext(p))] {
			return err
		}
		data, err := os.ReadFile(p) //nolint:gosec // p comes from walking our own root
		if err != nil {
			return err
		}
		found := false
		for from := range replace {
			if bytes.Contains(data, []byte(from)) {
				found = true
				break
			}
		}
		if !found {
			return nil
		}
		rel, err := filepath.Rel(abs, p)
		if err != nil {
			return err
		}
		h.rendered["/"+filepath.ToSlash(rel)] = []byte(replacer.Replace(string(data)))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return h, nil
}

// Rendered reports how many files carried a placeholder.
func (h *Handler) Rendered() int { return len(h.rendered) }

// RewriteURI is rewriteUri() of apps/web/tools/serve-dist.mjs (keep the two in sync).
func RewriteURI(uri string) string {
	if strings.HasSuffix(uri, "/") {
		if uri == "/" {
			return "/index.html"
		}
		return strings.TrimSuffix(uri, "/") + ".html"
	}
	if !strings.Contains(path.Base(uri), ".") {
		return uri + ".html"
	}
	return uri
}

// CacheControl is cacheControl() of serve-dist.mjs: hashed assets are immutable, HTML always
// revalidates. Rendered files never use the immutable policy (their origins may change).
func CacheControl(sitePath string, rendered bool) string {
	switch {
	case rendered:
		return "no-cache"
	case strings.HasPrefix(sitePath, "/_astro/"):
		return "public, max-age=31536000, immutable"
	case strings.HasSuffix(sitePath, ".html"):
		return "public, max-age=0, must-revalidate"
	default:
		return "public, max-age=3600"
	}
}

func contentType(p string) string {
	ext := strings.ToLower(path.Ext(p))
	if ct, ok := contentTypes[ext]; ok {
		return ct
	}
	if ct := mime.TypeByExtension(ext); ct != "" {
		return ct
	}
	return "application/octet-stream"
}

// resolve maps a request path to a site path inside the root, or "" when it is malformed or
// escapes the directory.
func resolve(rawPath string) string {
	p, err := url.PathUnescape(rawPath)
	if err != nil || strings.ContainsRune(p, 0) {
		return ""
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	trailing := strings.HasSuffix(p, "/")
	clean := path.Clean(p)
	if trailing && clean != "/" {
		clean += "/"
	}
	return RewriteURI(clean)
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "405", http.StatusMethodNotAllowed)
		return
	}
	sitePath := resolve(r.URL.EscapedPath())
	if sitePath == "" {
		http.Error(w, "400", http.StatusBadRequest)
		return
	}
	if h.send(w, r, http.StatusOK, sitePath) {
		return
	}
	if h.send(w, r, http.StatusNotFound, "/404.html") {
		return
	}
	http.Error(w, "404", http.StatusNotFound)
}

// send writes sitePath with status and reports whether the file exists.
func (h *Handler) send(w http.ResponseWriter, r *http.Request, status int, sitePath string) bool {
	body, rendered := h.rendered[sitePath]
	if !rendered {
		file := filepath.Join(h.root, filepath.FromSlash(strings.TrimPrefix(sitePath, "/")))
		if file != h.root && !strings.HasPrefix(file, h.root+string(filepath.Separator)) {
			return false
		}
		info, err := os.Stat(file) //nolint:gosec // confined to the root just above
		if err != nil || !info.Mode().IsRegular() {
			return false
		}
		data, err := os.ReadFile(file) //nolint:gosec // confined to the root above
		if err != nil {
			return false
		}
		body = data
	}
	hd := w.Header()
	hd.Set("Content-Type", contentType(sitePath))
	hd.Set("Content-Length", strconv.Itoa(len(body)))
	hd.Set("Cache-Control", CacheControl(sitePath, rendered))
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("X-Frame-Options", "DENY")
	hd.Set("Content-Security-Policy", "frame-ancestors 'none'")
	hd.Set("Referrer-Policy", "strict-origin-when-cross-origin")
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body) //nolint:gosec // static files of the build, not request data
	}
	return true
}

// Origin validates a base URL given to the server (http or https, host, no path).
func Origin(name, value string) (string, error) {
	value = strings.TrimRight(strings.TrimSpace(value), "/")
	u, err := url.Parse(value)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || (u.Path != "" && u.Path != "/") {
		return "", errors.New(name + " precisa ser uma origem http(s) sem caminho, ex.: http://localhost:4321")
	}
	return value, nil
}
