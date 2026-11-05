// Command docmatch installs docmatch on this machine with one command and then manages it:
//
//	docmatch            install (or resume) and open the app
//	docmatch up|down|status|logs|doctor|demo
//	docmatch update|uninstall|version
//
// See README.md (pt-BR) for the modes, the flags and the checksums.
package main

import (
	"os"

	"github.com/mmtmn/docmatch/tools/installer/internal/cli"
)

// Stamped by release.sh (-ldflags -X main.version=...). installBase is where install.sh came
// from (the same base `docmatch update` downloads from).
var (
	version     = "dev"
	registry    = ""
	repoURL     = ""
	installBase = ""
)

func main() {
	os.Exit(cli.Main(os.Args[1:], cli.Build{Version: version, Registry: registry, RepoURL: repoURL, InstallBase: installBase}))
}
