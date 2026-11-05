//go:build !windows

package selfupdate

import "os"

// deleteLater only matters on Windows; elsewhere a running binary can be deleted at once.
func deleteLater(path string) error { return os.Remove(path) }
