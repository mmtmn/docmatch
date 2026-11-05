//go:build windows

package selfupdate

import (
	"os/exec"
	"path/filepath"
	"syscall"
)

// deleteLater starts a hidden, detached cmd.exe that waits a couple of seconds (ping is the
// console-free sleep) and deletes path, then the folder install.ps1 created when it is empty.
func deleteLater(path string) error {
	dir := filepath.Dir(path)
	line := `cmd.exe /d /c ping -n 3 127.0.0.1 >nul & del /f /q "` + path + `"`
	if filepath.Base(dir) == "bin" && filepath.Base(filepath.Dir(dir)) == "docmatch" {
		line += ` & rmdir "` + dir + `" 2>nul & rmdir "` + filepath.Dir(dir) + `" 2>nul`
	}
	cmd := exec.Command("cmd.exe") //nolint:gosec,noctx // fixed program; outlives this process on purpose
	const detached, noWindow = 0x00000008, 0x08000000
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: line, HideWindow: true, CreationFlags: detached | noWindow}
	return cmd.Start()
}
