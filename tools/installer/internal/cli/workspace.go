package cli

import (
	"context"

	"github.com/mmtmn/docmatch/tools/installer/internal/workspace"
)

func runWorkspace(ctx context.Context, w world, args []string, b Build) (int, error) {
	var c common
	var api, output, projectID, newProject, accountID, saveRunID string
	var listProjects, uploadOnly bool
	fs := newFlagSet("workspace")
	c.register(fs)
	fs.StringVar(&api, "api", "", "")
	fs.StringVar(&output, "output", "", "")
	fs.StringVar(&projectID, "saved-project", "", "")
	fs.StringVar(&newProject, "new-project", "", "")
	fs.StringVar(&saveRunID, "save-run", "", "")
	fs.StringVar(&accountID, "account", "", "")
	fs.BoolVar(&listProjects, "list-projects", false, "")
	fs.BoolVar(&uploadOnly, "upload-only", false, "")
	files, err := parseArgs(fs, args)
	if err != nil {
		return exitUsage, err
	}
	profile, err := loadConnection(w.installDir(""))
	if err != nil {
		return exitFailed, err
	}
	key := w.env.Get("DOCMATCH_API_KEY")
	// A saved key is bound to its origin; --api or an environment override must not leak it.
	if api == "" {
		api = w.env.Get("DM_API_BASE_URL")
	}
	if api == "" {
		api = profile.API
	}
	if api == profile.API && key == "" {
		key = profile.Key
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
	err = workspace.Run(ctx, workspace.Options{API: api, Key: key, ProjectID: projectID, NewProject: newProject, AccountID: accountID, SaveRunID: saveRunID, ListProjects: listProjects, UploadOnly: uploadOnly, Dir: dir, Output: output, Files: files, Plain: prefs.plain, Yes: c.y, NoAnimation: !prefs.animate, In: w.stdin, Out: w.stdout})
	if err != nil {
		return exitFailed, err
	}
	return exitOK, nil
}
