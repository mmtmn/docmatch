package workspace

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/huh"

	dm "github.com/mmtmn/docmatch/sdks/go"
	"github.com/mmtmn/docmatch/tools/installer/internal/text"
)

type destination struct {
	client  *dm.Client
	project *dm.ProjectClient
	saved   dm.Project
	account string
}

// Resolve the destination once, before reading a file. API keys never select another account.
func chooseDestination(ctx context.Context, c *dm.Client, o Options) (destination, error) {
	target := destination{client: c, account: text.T("projects.key_account")}
	if o.ProjectID != "" && o.NewProject != "" {
		return target, errors.New(text.T("projects.choose_one"))
	}
	if o.Key != "" && o.AccountID != "" {
		return target, errors.New(text.T("projects.key_bound"))
	}
	if o.Key == "" {
		if err := c.DevLogin(ctx); err != nil {
			return target, err
		}
		accounts, err := c.Organizations(ctx)
		if err != nil {
			return target, err
		}
		selected := o.AccountID
		if selected == "" && len(accounts.Items) > 0 {
			selected = accounts.Items[0].ID
		}
		if !o.Plain && o.AccountID == "" && len(accounts.Items) > 1 {
			choices := make([]huh.Option[string], 0, len(accounts.Items))
			for _, account := range accounts.Items {
				choices = append(choices, huh.NewOption(Safe(account.Name)+" · "+Safe(account.Role), account.ID))
			}
			err = huh.NewForm(huh.NewGroup(huh.NewSelect[string]().Title(text.T("projects.account")).Options(choices...).Value(&selected))).WithInput(o.In).WithOutput(o.Out).RunWithContext(ctx)
			if err != nil {
				return target, err
			}
		}
		found := false
		for _, account := range accounts.Items {
			if account.ID == selected {
				found = true
				target.account = account.Name
				break
			}
		}
		if !found {
			return target, errors.New(text.T("projects.account_missing"))
		}
		target.client = c.WithAccount(selected)
	}
	if o.ListProjects {
		cursor := ""
		for {
			page, err := target.client.ListProjects(ctx, false, cursor)
			if err != nil {
				return target, err
			}
			for _, project := range page.Items {
				_, _ = fmt.Fprintf(o.Out, "%s\t%s\n", Safe(project.ID), Safe(project.Name))
			}
			if page.NextCursor == "" {
				return target, nil
			}
			cursor = page.NextCursor
		}
	}
	id, name := o.ProjectID, strings.TrimSpace(o.NewProject)
	if id == "" && name == "" && o.Plain {
		return target, errors.New(text.T("projects.required"))
	}
	cursor := ""
	for id == "" && name == "" {
		page, err := target.client.ListProjects(ctx, false, cursor)
		if err != nil {
			return target, err
		}
		choices := []huh.Option[string]{huh.NewOption(text.T("projects.new"), "new")}
		for _, project := range page.Items {
			choices = append(choices, huh.NewOption(Safe(project.Name), project.ID))
		}
		if page.NextCursor != "" {
			choices = append(choices, huh.NewOption(text.T("projects.more"), "more"))
		}
		if cursor != "" {
			choices = append(choices, huh.NewOption(text.T("projects.first"), "first"))
		}
		selected := ""
		err = huh.NewForm(huh.NewGroup(huh.NewSelect[string]().Title(text.T("projects.title")).Description(Safe(target.account)).Options(choices...).Value(&selected))).WithInput(o.In).WithOutput(o.Out).RunWithContext(ctx)
		if err != nil {
			return target, err
		}
		switch selected {
		case "more":
			cursor = page.NextCursor
		case "first":
			cursor = ""
		case "new":
			err = huh.NewForm(huh.NewGroup(huh.NewInput().Title(text.T("projects.name")).Value(&name).Validate(func(s string) error {
				if strings.TrimSpace(s) == "" {
					return errors.New(text.T("projects.name_required"))
				}
				return nil
			}))).WithInput(o.In).WithOutput(o.Out).RunWithContext(ctx)
			if err != nil {
				return target, err
			}
		default:
			id = selected
		}
	}
	var err error
	if name != "" {
		target.saved, err = target.client.CreateProject(ctx, name)
		id = target.saved.ID
	} else {
		target.saved, err = target.client.Project(id).Get(ctx)
	}
	if err != nil {
		return target, err
	}
	target.project = target.client.Project(id)
	return target, nil
}

func estimateProject(ctx context.Context, p *dm.ProjectClient) (dm.ProjectAnalysis, error) {
	current, err := p.Get(ctx)
	if err != nil {
		return dm.ProjectAnalysis{}, err
	}
	analysis, err := p.Estimate(ctx, current.Revision, "")
	if err == nil && analysis.State == "expired" {
		err = errors.New(text.T("projects.expired"))
	}
	return analysis, err
}
