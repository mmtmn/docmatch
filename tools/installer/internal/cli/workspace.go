package cli

import (
	"context"

	"github.com/mmtmn/docmatch/tools/installer/internal/workspace"
)

func runWorkspace(ctx context.Context, w world, args []string, b Build) (int, error) {
	var c common
	var api, output string
	fs := newFlagSet("workspace")
	c.register(fs)
	fs.StringVar(&api, "api", "", "")
	fs.StringVar(&output, "output", "", "")
	files, err := parseArgs(fs, args)
	if err != nil {
		return exitUsage, err
	}
	if api == "" {
		if r, err := loadInstalled(ctx, w, c, b); err == nil {
			api = r.opts.URLs().API
		} else {
			api = first(w.env.Get("DM_API_BASE_URL"), "http://localhost:8080")
		}
	}
	dir := c.dir
	if dir == "" {
		dir = w.cwd
	}
	prefs := w.prefs(c)
	err = workspace.Run(ctx, workspace.Options{API: api, Key: w.env.Get("DOCMATCH_API_KEY"), Dir: dir, Output: output, Files: files, Plain: prefs.plain, Yes: c.y, NoAnimation: !prefs.animate, In: w.stdin, Out: w.stdout})
	if err != nil {
		return exitFailed, err
	}
	return exitOK, nil
}
