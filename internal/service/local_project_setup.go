package service

import (
	"context"

	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/localgit"
)

type LocalProjectSetupRequest struct {
	Project    string
	ProjectID  string
	StorageRef string
	Ref        string
	ReadOnly   bool
	RAGProfile string
	Confirm    bool
	Initialize bool
}
type LocalProjectSetupResult struct {
	Project    string `json:"project"`
	ProjectID  string `json:"project_id"`
	Status     string `json:"status"`
	NextAction string `json:"next_action"`
}

func SetupLocalProject(ctx context.Context, src config.Source, req LocalProjectSetupRequest) (LocalProjectSetupResult, error) {
	var zero LocalProjectSetupResult
	if ctx.Err() != nil {
		return zero, ctx.Err()
	}
	profile := req.RAGProfile
	if profile == "" {
		profile = "disabled"
	}
	b := config.LocalProjectConfig{Backend: "local_git", ProjectID: req.ProjectID, StorageRef: req.StorageRef, Ref: req.Ref, ReadOnly: req.ReadOnly, RAGProfile: profile}
	if !req.Confirm || config.ValidateLocalProjects(config.ServiceModeLocal, map[string]config.LocalProjectConfig{req.Project: b}) != nil {
		return zero, localProjectError("invalid_argument", "setup_confirmation")
	}
	if config.Locate(src).Format != "yaml" {
		return zero, localProjectError("invalid_configuration", "global_yaml_required")
	}
	path, err := config.LocalProjectStorePath(src, req.StorageRef)
	if err != nil {
		return zero, err
	}
	if req.Initialize {
		if _, err = localgit.Initialize(ctx, path, req.ProjectID, req.Ref); err != nil {
			return zero, err
		}
	}
	if err = config.RegisterLocalProject(src, req.Project, b, func(all map[string]config.LocalProjectConfig) error {
		_, err := NewLocalProjects(ctx, all, func(ref string) (string, error) { return config.LocalProjectStorePath(src, ref) })
		return err
	}); err != nil {
		return zero, err
	}
	return LocalProjectSetupResult{Project: req.Project, ProjectID: req.ProjectID, Status: "registered", NextAction: "explicitly stop any other daemon, then run gitcode-mcp service run with this configuration"}, nil
}
