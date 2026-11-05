package selfupdate

import (
	"errors"
	"strings"

	"github.com/mmtmn/docmatch/tools/installer/internal/text"
)

// laterCommand is the hidden cmd.exe that deletes path after this process exits (Windows):
// absolute System32 programs (cmd.exe would otherwise find a ping.exe in its working folder
// first), run from System32, a ping as the console-free sleep, then the folder install.ps1
// created when it is empty. cmd expands %VAR% even inside quotes, so a path with % is refused.
// Windows paths are built by hand: this runs (and is tested) on every system.
func laterCommand(systemRoot, path string) (exe, line, dir string, err error) {
	if strings.ContainsAny(path, `%"`) || strings.ContainsAny(systemRoot, `%"`) {
		return "", "", "", errors.New(text.T("update.err_later_path", "path", path))
	}
	if systemRoot == "" {
		systemRoot = `C:\Windows`
	}
	dir = strings.TrimRight(systemRoot, `\`) + `\System32`
	exe = dir + `\cmd.exe`
	inner := `"` + dir + `\PING.EXE" -n 3 127.0.0.1 >nul & del /f /q "` + path + `"`
	parent := winDir(path)
	if winBase(parent) == "bin" && winBase(winDir(parent)) == "docmatch" {
		inner += ` & rmdir "` + parent + `" 2>nul & rmdir "` + winDir(parent) + `" 2>nul`
	}
	// /s with one outer pair of quotes: cmd removes exactly those and keeps the inner ones.
	line = `"` + exe + `" /d /s /c "` + inner + `"`
	return exe, line, dir, nil
}

func winDir(p string) string {
	if i := strings.LastIndexAny(p, `\/`); i > 0 {
		return p[:i]
	}
	return p
}

func winBase(p string) string {
	if i := strings.LastIndexAny(p, `\/`); i >= 0 {
		return p[i+1:]
	}
	return p
}
