// Package version exposes build metadata injected at link time.
//
// The Makefile sets these through -ldflags:
//
//	-X github.com/mmtmn/docmatch/services/api/internal/version.GitSHA=<sha>
//	-X github.com/mmtmn/docmatch/services/api/internal/version.BuiltAt=<RFC3339>
//	-X github.com/mmtmn/docmatch/services/api/internal/version.Version=<tag>
package version

import "runtime"

// Values are plain variables (not constants) so the linker can overwrite them.
var (
	// GitSHA is the short commit hash the binary was built from.
	GitSHA = "desconhecido"
	// BuiltAt is the build timestamp in RFC 3339.
	BuiltAt = "desconhecido"
	// Version is the human-readable version (git tag or "dev").
	Version = "dev"
)

// Info is the JSON shape served by GET /v1/version.
type Info struct {
	Version string `json:"version"`
	GitSHA  string `json:"git_sha"`
	BuiltAt string `json:"built_at"`
	Go      string `json:"go"`
}

// Current returns the build metadata of the running binary.
func Current() Info {
	return Info{Version: Version, GitSHA: GitSHA, BuiltAt: BuiltAt, Go: runtime.Version()}
}
