package repositorydocs

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestPublishedRepositoryDocsPolicyFixture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "repository-docs", "gitcode-mcp.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := ParsePolicy(data, PolicySourceCommitted)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != PolicyStatusReady || !result.Policy.Matches("README.md") || !result.Policy.Matches("architecture/overview.md") || result.Policy.Matches("docs/generated/api.md") {
		t.Fatalf("fixture policy = %#v", result)
	}
}

func TestParsePolicyBuiltinAndCommitted(t *testing.T) {
	builtin, err := ParsePolicy([]byte("cache_mode: repo-local\n"), PolicySourceCommitted)
	if err != nil {
		t.Fatal(err)
	}
	if builtin.Source != PolicySourceBuiltin || builtin.Status != PolicyStatusReady || builtin.Policy.Preset != DefaultPolicyPreset {
		t.Fatalf("builtin = %#v", builtin)
	}
	if !builtin.Policy.Matches("README.md") || !builtin.Policy.Matches("docs/architecture.md") || !builtin.Policy.Matches("AGENTS.md") || builtin.Policy.Matches("internal/main.go") {
		t.Fatalf("unexpected builtin matching: %#v", builtin.Policy)
	}

	configured, err := ParsePolicy([]byte(`cache_mode: repo-local
repository_docs:
  schema: 1
  enabled: true
  preset: none
  include:
    - architecture/**
    - decisions/**/*.md
  exclude:
    - architecture/generated/**
service:
  runtime_dir: ignored
`), PolicySourceCommitted)
	if err != nil {
		t.Fatal(err)
	}
	if configured.Source != PolicySourceCommitted || configured.PolicyHash == "" || configured.ConfigDigest == "" {
		t.Fatalf("configured = %#v", configured)
	}
	if !configured.Policy.Matches("architecture/overview.md") || configured.Policy.Matches("architecture/generated/api.md") || !configured.Policy.Matches("decisions/2026/cache.md") {
		t.Fatalf("unexpected configured matching: %#v", configured.Policy)
	}
}

func TestParsePolicyNormalizesAndHashesDeterministically(t *testing.T) {
	a, err := ParsePolicy([]byte(`repository_docs:
  include:
    - ./z/**
    - a/**
    - a/**
  exclude: []
`), PolicySourceCommitted)
	if err != nil {
		t.Fatal(err)
	}
	b, err := ParsePolicy([]byte(`repository_docs:
  include:
    - a/**
    - z/**
`), PolicySourceCommitted)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a.Policy.Include, []string{"a/**", "z/**"}) || a.PolicyHash != b.PolicyHash {
		t.Fatalf("a=%#v b=%#v", a, b)
	}
}

func TestParsePolicyFailsClosed(t *testing.T) {
	tests := []string{
		"repository_docs:\n  schema: 2\n",
		"repository_docs: {schema: 2, enabled: true}\n",
		"'repository_docs': {schema: 2}\n",
		"repository_docs: null\n",
		"repository_docs:\n  schema: 1\nrepository_docs:\n  schema: 1\n",
		"repository_docs:\n  preset: future\n",
		"repository_docs:\n  preset: none\n",
		"repository_docs:\n  include:\n    - ../secret.md\n",
		"repository_docs:\n  mystery: true\n",
	}
	for _, input := range tests {
		result, err := ParsePolicy([]byte(input), PolicySourceCommitted)
		if !errors.Is(err, ErrPolicyInvalid) || result.Status != PolicyStatusInvalid {
			t.Fatalf("input=%q result=%#v err=%v", input, result, err)
		}
	}
}

func TestParsePolicySupportsStandardYAMLForms(t *testing.T) {
	result, err := ParsePolicy([]byte(`"repository_docs": {schema: 1, enabled: true, preset: none, include: ["docs/**", 'architecture/#notes.md']} # inline comment
`), PolicySourceCommitted)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != PolicyStatusReady || !result.Policy.Matches("docs/guide.md") || !result.Policy.Matches("architecture/#notes.md") {
		t.Fatalf("result = %#v", result)
	}
}

func TestDisabledPolicy(t *testing.T) {
	result, err := ParsePolicy([]byte("repository_docs:\n  enabled: false\n"), PolicySourceCommitted)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != PolicyStatusDisabled || result.Policy.Matches("README.md") {
		t.Fatalf("result=%#v", result)
	}
}

func TestPolicyMatcherIsSlashNormalizedCaseSensitiveAndMultilingual(t *testing.T) {
	result, err := ParsePolicy([]byte(`repository_docs:
  preset: none
  include:
    - docs/**
    - архитектура/**
    - 中文资料/**
  exclude:
    - docs/generated/**
`), PolicySourceCommitted)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		path string
		want bool
	}{
		{path: `docs\guide.md`, want: true},
		{path: "./docs/guide.md", want: true},
		{path: "docs/generated/api.md", want: false},
		{path: "Docs/guide.md", want: false},
		{path: "архитектура/обзор.md", want: true},
		{path: "中文资料/设计.md", want: true},
		{path: ".git/config", want: false},
		{path: ".gitcode/mcp/cache.db", want: false},
	}
	for _, test := range tests {
		if got := result.Policy.Matches(test.path); got != test.want {
			t.Errorf("Matches(%q)=%t, want %t", test.path, got, test.want)
		}
	}
	builtinInclude, builtinExclude := BuiltinPolicy().EffectiveMatchers()
	if !reflect.DeepEqual(builtinInclude, []string{"README*", "AGENTS.md", "docs/**"}) || len(builtinExclude) != 0 {
		t.Fatalf("builtin effective matchers include=%v exclude=%v", builtinInclude, builtinExclude)
	}
}
