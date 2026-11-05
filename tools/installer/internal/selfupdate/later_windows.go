//go:build windows

package selfupdate

import (
	"os"
	"os/exec"
	"syscall"
)

// deleteLater starts the hidden, detached cmd.exe of laterCommand.
func deleteLater(path string) error {
	exe, line, dir, err := laterCommand(os.Getenv("SystemRoot"), path)
	if err != nil {
		return err
	}
	cmd := exec.Command(exe) //nolint:gosec,noctx // absolute System32 program; outlives this process on purpose
	cmd.Dir = dir
	const detached, noWindow = 0x00000008, 0x08000000
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: line, HideWindow: true, CreationFlags: detached | noWindow}
	return cmd.Start()
}
