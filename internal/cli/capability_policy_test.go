package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCapabilityPolicyCLIRejectsAliasesBeforeFactory(t *testing.T) {
	src := newCLIConfigSource(t)
	path := filepath.Join(src.configDir, "gitcode-mcp", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("capabilities: {schema: 1, ceiling: {allow: []}}"), 0600); err != nil {
		t.Fatal(err)
	}
	factory := func(context.Context, string) (queryService, func() error, error) {
		t.Fatal("denied CLI constructed dependencies")
		return nil, nil, nil
	}
	for _, args := range [][]string{{"search"}, {"search_sources"}, {"merge-pr"}, {"repo", "add"}, {"service", "run"}, {"add-issue-comment", "--body", "--help"}, {"feedback", "submit"}} {
		var out, errout bytes.Buffer
		code := executeWithFactoryAndDeps(args, &out, &errout, factory, localCommandDeps{Source: src})
		if code == 0 || !strings.Contains(errout.String(), "capability_policy_") {
			t.Fatalf("policy bypass: %v", args)
		}
	}
	var out, errout bytes.Buffer
	if code := executeWithFactoryAndDeps([]string{"capabilities"}, &out, &errout, factory, localCommandDeps{Source: src}); code != 0 || !strings.Contains(out.String(), `"managed":true`) {
		t.Fatalf("policy diagnostics: %d %s", code, errout.String())
	}
}
