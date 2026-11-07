package rules

import (
	"context"

	"github.com/google/uuid"
)

type projectScope struct{ org, project uuid.UUID }
type projectScopeKey struct{}

// WithProject is used only after authorization to that project. Project rules
// never enter the legacy account-wide picker or another project's analysis.
func WithProject(ctx context.Context, org, project uuid.UUID) context.Context {
	return context.WithValue(ctx, projectScopeKey{}, projectScope{org, project})
}

func ProjectID(ctx context.Context, org uuid.UUID) *uuid.UUID {
	if scope, ok := ctx.Value(projectScopeKey{}).(projectScope); ok && scope.org == org {
		return &scope.project
	}
	return nil
}

func visible(ctx context.Context, org uuid.UUID, r Rule) bool {
	if r.Scope == ScopeGlobal && r.ProjectID == nil {
		return true
	}
	project := ProjectID(ctx, org)
	return project == nil && r.ProjectID == nil || project != nil && r.ProjectID != nil && *project == *r.ProjectID
}
