package toolchain

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

// The version probes (`go version`, `cargo --version`...) run in a folder only this user can
// write to, with no folder anyone can write to above it. Tools look for configuration in the
// folder they run in and its parents: rustup, for one, honors a rust-toolchain.toml there and
// its `path = ...`, so a /tmp/rust-toolchain.toml planted by another user of the machine would
// run their program as this one, right before a sudo prompt.

var (
	probeOnce sync.Once
	probeDir  string
)

// ProbeDir is that folder: <user cache>/docmatch/probe (0700), else the home folder, else "/".
func ProbeDir() string {
	probeOnce.Do(func() {
		var candidates []string
		if c, err := os.UserCacheDir(); err == nil {
			candidates = append(candidates, filepath.Join(c, "docmatch", "probe"))
		}
		if h, err := os.UserHomeDir(); err == nil {
			candidates = append(candidates, h)
		}
		probeDir = pickProbeDir(candidates, os.Lstat, func(dir string) error {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return err
			}
			return os.Chmod(dir, 0o700) //nolint:gosec // a folder: only its owner may enter or write
		}, runtime.GOOS)
	})
	return probeDir
}

// pickProbeDir returns the first candidate that can be prepared and has no folder writable by
// everyone in its path, or "/" (written only by root). Windows reports no such permission bit
// (and its cache folder is per user), so there the check is skipped.
func pickProbeDir(candidates []string, lstat func(string) (fs.FileInfo, error), prepare func(string) error, goos string) string {
	for _, dir := range candidates {
		if goos != "windows" && !safePath(dir, lstat) {
			continue
		}
		if err := prepare(dir); err != nil {
			continue
		}
		return dir
	}
	if goos == "windows" {
		return os.TempDir() // per user on Windows
	}
	return "/"
}

// safePath reports whether no existing folder from dir up to the root is writable by others
// (or a symlink, which could point anywhere).
func safePath(dir string, lstat func(string) (fs.FileInfo, error)) bool {
	p := filepath.Clean(dir)
	for {
		if info, err := lstat(p); err == nil {
			if info.Mode()&fs.ModeSymlink != 0 || info.Mode().Perm()&0o002 != 0 {
				return false
			}
		}
		parent := filepath.Dir(p)
		if parent == p {
			return true
		}
		p = parent
	}
}
