package cli

import (
	"bytes"
	"context"
	"gitcode-mcp/internal/service"
	"testing"
)

func (s *spyService) GetRepositoryMetadata(context.Context, string) (service.RepositoryMetadataResult, error) {
	s.called("GetRepositoryMetadata")
	return service.RepositoryMetadataResult{}, nil
}

func TestIssue157CLIBootstrapParity(t *testing.T) {
	for _, tc := range []struct{ command, method string }{{"repo-metadata", "GetRepositoryMetadata"}, {"list-repo-labels", "ListRepositoryLabels"}, {"create-repo-label", "CreateRepositoryLabel"}} {
		spy := &spyService{}
		factory := func(context.Context, string) (queryService, func() error, error) { return spy, nil, nil }
		args := []string{tc.command, "--repo", "fixture-a", "--format", "json", "--offline"}
		if tc.command == "create-repo-label" {
			args = append(args, "--name", "state:ready", "--color", "#AABBCC", "--idempotency-key", "label-key", "--dry-run")
		}
		var out, errOut bytes.Buffer
		if code := executeWithFactory(args, &out, &errOut, factory); code != 0 || spy.calls[tc.method] != 1 {
			t.Fatalf("bootstrap dispatch failed: command=%s code=%d error=%s", tc.command, code, errOut.String())
		}
		if tc.command == "create-repo-label" {
			req := spy.lastWriteRequest[tc.method]
			if req.Label != "state:ready" || req.Color != "#AABBCC" || req.IdempotencyKey != "label-key" || req.Mode != service.WriteModeDryRun {
				t.Fatal("CLI label mapping diverged")
			}
		}
		if !isLiveStartupCommand(tc.command) {
			t.Fatal("explicit bootstrap command omitted from live startup")
		}
	}
	spy := &spyService{}
	factory := func(context.Context, string) (queryService, func() error, error) { return spy, nil, nil }
	var out, errOut bytes.Buffer
	code := executeWithFactory([]string{"create-repo-label", "--repo", "fixture-a", "--name", "unused", "--color", "#aabbcc"}, &out, &errOut, factory)
	if code == 0 || spy.calls["CreateRepositoryLabel"] != 0 {
		t.Fatal("CLI implicit live intent accepted")
	}
}
func (s *spyService) ListRepositoryLabels(context.Context, string) (service.RepositoryLabelsResult, error) {
	s.called("ListRepositoryLabels")
	return service.RepositoryLabelsResult{}, nil
}
func (s *spyService) CreateRepositoryLabel(_ context.Context, req service.WriteCommandRequest) (service.WriteCommandResult, error) {
	s.called("CreateRepositoryLabel")
	if s.lastWriteRequest == nil {
		s.lastWriteRequest = map[string]service.WriteCommandRequest{}
	}
	s.lastWriteRequest["CreateRepositoryLabel"] = req
	return service.WriteCommandResult{Command: "create-repo-label", Status: "succeeded"}, nil
}
