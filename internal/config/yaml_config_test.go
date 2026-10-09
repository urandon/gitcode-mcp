package config

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestEffectiveRepoConfigFormattedDocumentationSequences(t *testing.T) {
	for _, content := range []string{
		"repository_docs:\n  include:\n    - README.md\n",
		"repository_docs: {include: [README.md]}\n",
	} {
		src := newMemorySource(t)
		root := filepath.Join(src.homeDir, "example-worktree")
		src.cwd = filepath.Join(root, "nested")
		src.dirs[filepath.Join(root, ".git")] = true
		src.files[filepath.Join(root, ".gitcode", "gitcode-mcp.yaml")] = []byte(content)
		eff, err := LoadEffective(src, Overrides{})
		if err != nil {
			t.Fatalf("valid formatted repository config rejected: %v", err)
		}
		if eff.RepoRoot != root || eff.Config.CacheMode != CacheModeGlobal {
			t.Fatal("documentation intent changed default cache authority")
		}
	}
}

func TestYAMLConfigStandardFormsAndLegacyLists(t *testing.T) {
	forms := []string{
		`# formatted block YAML
cache:
  mode: global # inline comment
mcp:
  tools: {access: read}
credential: {store: env, keyring_account: 'agent: reports'}
feedback:
  enabled: true
  labels:
    - dogfood
    - feedback
rag:
  providers:
    example:
      endpoint: 'http://127.0.0.1:1234'
      env: {EXAMPLE_VALUE: 'a: b # literal'}
      install_hints:
        - 'one: hint|literal; text'
        - second
  search: {hybrid: false, top_k: 9}
service: {job_retention: {success_ttl: 24h}}
repository_docs:
  include: [README.md]
unrelated: {nested: [a, {b: c}]}
`,
		`cache: {mode: global}
mcp: {tools: {access: read}}
credential: {store: env, keyring_account: "agent: reports"}
feedback: {enabled: true, labels: [dogfood, feedback]}
rag: {providers: {example: {endpoint: 'http://127.0.0.1:1234', env: {EXAMPLE_VALUE: 'a: b # literal'}, install_hints: ['one: hint|literal; text', second]}}, search: {hybrid: false, top_k: 9}}
service: {job_retention: {success_ttl: 24h}}
repository_docs: {include: [README.md]}
unrelated: {nested: [a, {b: c}]}
`,
	}
	var first fileConfig
	for i, content := range forms {
		cfg, cred, err := parseYAMLConfig([]byte(content), "")
		if err != nil {
			t.Fatal(err)
		}
		if cred.Store != "env" || cred.KeyringAccount != "agent: reports" || cfg.MCP.Tools.Access != "read" {
			t.Fatal("quoted scalar or nested mapping changed")
		}
		provider := cfg.RAG.Providers["example"]
		if *provider.Endpoint != "http://127.0.0.1:1234" || provider.Env["EXAMPLE_VALUE"] != "a: b # literal" || !reflect.DeepEqual([]string(provider.InstallHints), []string{"one: hint|literal; text", "second"}) {
			t.Fatal("nested provider scalar/list changed")
		}
		if i == 0 {
			first = cfg
		} else if !reflect.DeepEqual(first, cfg) {
			t.Fatal("block and flow configurations differ")
		}
	}
	for _, input := range []string{
		"feedback:\n  labels: dogfood|feedback\n",
		"feedback:\n  labels: dogfood;feedback\n",
		"labels: &labels [dogfood, feedback]\nfeedback: {labels: *labels}\n",
	} {
		cfg, _, err := parseYAMLConfig([]byte(input), "")
		if err != nil || !reflect.DeepEqual([]string(cfg.Feedback.Labels), []string{"dogfood", "feedback"}) {
			t.Fatalf("legacy/alias list rejected: %v", err)
		}
	}
	var legacy fileConfig
	if err := json.Unmarshal([]byte(`{"feedback":{"labels":["dogfood","feedback"]},"rag":{"providers":{"example":{"install_hints":["hint"]}}}}`), &legacy); err != nil || len(legacy.Feedback.Labels) != 2 || len(legacy.RAG.Providers["example"].InstallHints) != 1 {
		t.Fatal("YAML list compatibility changed JSON decoding")
	}
}

func TestYAMLConfigRejectsInvalidDocumentsWithoutScalarDisclosure(t *testing.T) {
	for _, input := range []string{
		"cache_mode: [invalid-secret-value]\n",
		"max_retries: invalid-secret-value\n",
		"mcp: [invalid-secret-value]\n",
		"feedback: {enabled: invalid-secret-value}\n",
		"feedback: {labels: {bad: invalid-secret-value}}\n",
		"rag: {providers: {example: {install_hints: {bad: invalid-secret-value}}}}\n",
		"format: text\nformat: json\n",
		"rag: {search: {top_k: 1, top_k: 2}}\n",
		"[invalid-secret-value]\n",
		"null\n",
		"format: [invalid-secret-value\n",
		"format: json\n---\ncache_mode: global\n",
		"format: json\n---\n[invalid-secret-value\n",
		"&root {<<: *root}\n",
		"feedback: &self {enabled: *self}\n",
	} {
		_, _, err := parseYAMLConfig([]byte(input), "")
		if err == nil || !strings.Contains(err.Error(), "malformed config") || strings.Contains(err.Error(), "invalid-secret-value") {
			t.Fatalf("unsafe/absent validation error: %v", err)
		}
	}
	for _, input := range []string{"", "# empty\n", "---\n# empty\n", "{}\n"} {
		if _, _, err := parseYAMLConfig([]byte(input), ""); err != nil {
			t.Fatalf("empty config rejected: %v", err)
		}
	}
}

func TestYAMLConfigLegacyQuotedScalarsAndSharedAnchors(t *testing.T) {
	cfg, cred, err := parseYAMLConfig([]byte(`number: &number "2"
boolean: &boolean "T"
max_retries: *number
max_response_size: "1024"
rate_limit_rps: "1.25"
rate_limit_burst: "3"
credential: {keyring_account: *boolean}
feedback: {enabled: *boolean}
rag:
  providers: {example: {autostart: "1"}}
  profiles: {example: {dimensions: "128", batch_size: "8"}}
  search: {top_k: "4", hybrid: "false"}
service: {job_retention: {max_terminal_jobs: "32"}}
`), "")
	if err != nil {
		t.Fatal(err)
	}
	if *cfg.MaxRetries != 2 || *cfg.MaxResponseSize != 1024 || *cfg.RateLimitRPS != 1.25 || *cfg.RateLimitBurst != 3 || !*cfg.Feedback.Enabled || cred.KeyringAccount != "T" || !*cfg.RAG.Providers["example"].Autostart || *cfg.RAG.Search.Hybrid || *cfg.RAG.Search.TopK != 4 || *cfg.RAG.Profiles["example"].Dimensions != 128 || *cfg.Service.JobRetention.MaxTerminalJobs != 32 {
		t.Fatal("legacy scalar coercion or shared string anchor changed")
	}
	for _, input := range []string{
		"defaults: &defaults {max_retries: '2'}\n<<: *defaults\n",
		"defaults: &defaults {max_retries: '2'}\n<<: [*defaults]\n",
	} {
		cfg, _, err := parseYAMLConfig([]byte(input), "")
		if err != nil || *cfg.MaxRetries != 2 {
			t.Fatalf("quoted scalar merge failed: %v", err)
		}
	}
	for _, spelling := range []string{"1", "0", "T", "F"} {
		cfg, _, err := parseYAMLConfig([]byte("feedback: {enabled: "+spelling+"}\n"), "")
		if err != nil || *cfg.Feedback.Enabled != (spelling == "1" || spelling == "T") {
			t.Fatalf("legacy unquoted boolean rejected: %v", err)
		}
	}
}

func TestFormattedYAMLRetainsLayerPrecedenceAndLocalPathRestrictions(t *testing.T) {
	src := newMemorySource(t)
	root := filepath.Join(src.homeDir, "example-worktree")
	src.cwd = root
	src.dirs[filepath.Join(root, ".git")] = true
	local := filepath.Join(root, ".gitcode", "gitcode-mcp.yaml")
	src.files[local] = []byte("cache: {mode: repo-local}\nrag: {search: {top_k: 4}}\nrepository_docs: {include: [README.md]}\n")
	global := filepath.Join(src.configDir, "global.yaml")
	src.env[EnvMCPConfigPath] = global
	src.files[global] = []byte("rag: {search: {top_k: 7}}\n")
	eff, err := LoadEffective(src, Overrides{})
	if err != nil || eff.Config.RAG.Search.TopK != 4 || eff.Config.CacheMode != CacheModeRepoLocal || eff.FieldSources["rag.search.top_k"] != "repo-local:"+local {
		t.Fatalf("layer precedence/source changed: %v", err)
	}
	for _, policy := range []string{"service: {runtime_dir: /example/runtime}\n", "rag: {model_store_path: /example/models}\n"} {
		src.files[local] = []byte(policy)
		if _, err := LoadEffective(src, Overrides{}); err == nil || !strings.Contains(err.Error(), "repo-local") {
			t.Fatalf("formatted YAML bypassed machine path restriction: %v", err)
		}
	}
}
