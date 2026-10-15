package service

import (
	"context"
	"path/filepath"
	"time"

	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/localgit"
)

// Trusted CLI-only file selection, never an IPC/MCP/Admin payload.
type LocalProjectBackupRequest struct {
	LocalProjectSetupRequest
	Bundle       string
	ExpectedHead string
}
type LocalProjectBackupResult struct {
	localgit.BackupResult
	Project    string `json:"project"`
	NextAction string `json:"next_action"`
}

func ExportLocalProject(ctx context.Context, src config.Source, req LocalProjectBackupRequest) (LocalProjectBackupResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var zero LocalProjectBackupResult
	if ctx.Err() != nil {
		return zero, ctx.Err()
	}
	if !req.Confirm || req.Bundle == "" {
		return zero, localProjectError("invalid_argument", "backup_confirmation")
	}
	cfg, err := config.LocalProjectBackupConfiguration(src)
	if err != nil || cfg.Service.Mode != config.ServiceModeLocal {
		return zero, config.LocalProjectConfigError{}
	}
	p, ok := cfg.LocalProjects[req.Project]
	if !ok {
		return zero, localProjectError("unknown_project", "project")
	}
	path, err := config.LocalProjectStorePath(src, p.StorageRef)
	if err != nil {
		return zero, err
	}
	store, err := localgit.Open(ctx, path, localgit.Limits{})
	if err != nil {
		return zero, err
	}
	bundle, err := filepath.Abs(req.Bundle)
	if err != nil {
		return zero, localProjectError("invalid_argument", "bundle")
	}
	r, err := store.ExportBundle(ctx, p.Ref, p.ProjectID, bundle)
	if err != nil {
		return zero, err
	}
	return LocalProjectBackupResult{r, req.Project, "keep the bundle private; restore only into a new inactive managed store"}, nil
}

func RestoreLocalProject(ctx context.Context, src config.Source, req LocalProjectBackupRequest) (LocalProjectBackupResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var zero LocalProjectBackupResult
	if ctx.Err() != nil {
		return zero, ctx.Err()
	}
	profile := req.RAGProfile
	if profile == "" {
		profile = "disabled"
	}
	b := config.LocalProjectConfig{Backend: "local_git", ProjectID: req.ProjectID, StorageRef: req.StorageRef, Ref: req.Ref, ReadOnly: req.ReadOnly, RAGProfile: profile}
	if !req.Confirm || req.Bundle == "" || config.ValidateLocalProjects(config.ServiceModeLocal, map[string]config.LocalProjectConfig{req.Project: b}) != nil {
		return zero, localProjectError("invalid_argument", "restore_confirmation")
	}
	if config.Locate(src).Format != "yaml" {
		return zero, localProjectError("invalid_configuration", "global_yaml_required")
	}
	cfg, err := config.LocalProjectBackupConfiguration(src)
	if err != nil {
		return zero, config.LocalProjectConfigError{}
	}
	for selector, p := range cfg.LocalProjects {
		if selector == req.Project || p.StorageRef == req.StorageRef || p.ProjectID == req.ProjectID {
			return zero, localProjectError("write_conflict", "restore_registration")
		}
	}
	path, err := config.LocalProjectStorePath(src, req.StorageRef)
	if err != nil {
		return zero, err
	}
	bundle, err := filepath.Abs(req.Bundle)
	if err != nil {
		return zero, localProjectError("invalid_argument", "bundle")
	}
	_, r, err := localgit.RestoreBundle(ctx, path, bundle, req.ProjectID, req.Ref, req.ExpectedHead)
	if err != nil {
		return zero, err
	}
	if err = config.RegisterLocalProject(src, req.Project, b, func(all map[string]config.LocalProjectConfig) error {
		_, e := NewLocalProjects(ctx, all, func(ref string) (string, error) { return config.LocalProjectStorePath(src, ref) })
		return e
	}); err != nil {
		return zero, err
	}
	return LocalProjectBackupResult{r, req.Project, "explicitly start the local daemon; derived indexes may be rebuilt, never imported as authority"}, nil
}
