package config

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"gitcode-mcp/internal/capability"
)

func TestCapabilityPolicyTrustAndPinning(t *testing.T) {
	s := newMemorySource(t)
	canonical := filepath.Join(s.configDir, "gitcode-mcp", "config.yaml")
	s.files[canonical] = []byte("mcp: {tools: {access: read}}\ncapabilities: {schema: 1, ceiling: {deny: [merge_pr]}}")
	override := filepath.Join(s.configDir, "runtime.yaml")
	s.files[override] = []byte("mcp: {tools: {access: write}}")
	s.env[EnvMCPConfigPath] = override
	s.env[EnvMCPToolAccess] = "write"
	s.env["XDG_CONFIG_HOME"] = "/ignored-policy-root"
	pinned, p, err := PrepareCapabilityPolicy(s, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if p.Check("get_source") != nil || p.Check("sync_live") == nil || p.Check("merge_pr") == nil {
		t.Fatal("read ceiling widened")
	}
	fingerprint := p.Fingerprint()
	s.files[canonical] = []byte("capabilities: {schema: 1}")
	_, q, err := PrepareCapabilityPolicy(pinned, "", "")
	if err != nil || q.Fingerprint() != fingerprint {
		t.Fatal("live reload changed grants")
	}
	s.files[override] = []byte("capabilities: {schema: 1}")
	if _, _, err = PrepareCapabilityPolicy(s, "", ""); err == nil {
		t.Fatal("env selected authority")
	}
	if _, err = pinned.ReadFile(filepath.Join(s.cwd, "missing.yaml")); err == nil {
		t.Fatal("missing file suppressed")
	}
	repo := filepath.Join(s.cwd, "repo.yaml")
	s.files[repo] = []byte("capabilities: {schema: 1, ceiling: {allow: []}}")
	if _, err = pinned.ReadFile(repo); err == nil {
		t.Fatal("repo policy accepted")
	}
}

func TestCapabilityPolicyLaunchFlagsAreNotCommandData(t *testing.T) {
	for _, args := range [][]string{{"add-issue-comment", "--body", "--deployment-config"}, {"search", "--query", "--policy-fingerprint=x"}, {"--", "--deployment-config", "ignored"}} {
		clean, deployment, expected, err := ExtractPolicyLaunchFlags(args)
		if err != nil || deployment != "" || expected != "" || strings.Join(clean, "\x00") != strings.Join(args, "\x00") {
			t.Fatal("command data reinterpreted as authority")
		}
	}
	for _, args := range [][]string{{"--deployment-config"}, {"--deployment-config="}, {"--deployment-config", "--policy-fingerprint"}, {"--policy-fingerprint=x", "--policy-fingerprint=y"}} {
		if _, _, _, err := ExtractPolicyLaunchFlags(args); err == nil {
			t.Fatal("invalid launch accepted")
		}
	}
}

func TestCapabilityPolicyRelativeDeploymentPinsAbsoluteAuthority(t *testing.T) {
	s := newMemorySource(t)
	path := filepath.Join(s.cwd, "instance.yaml")
	s.files[path] = []byte("capabilities: {schema: 1, ceiling: {allow: [get_source]}}")
	pinned, p, err := PrepareCapabilityPolicy(s, "instance.yaml", "")
	if err != nil || PolicyDeployment(pinned) != path || !filepath.IsAbs(PolicyDeployment(pinned)) {
		t.Fatal("relative deployment not normalized before pinning")
	}
	s.cwd = filepath.Join(s.cwd, "different-native-cwd")
	if _, q, err := PrepareCapabilityPolicy(s, PolicyDeployment(pinned), p.Fingerprint()); err != nil || q.Fingerprint() != p.Fingerprint() {
		t.Fatal("native launch depended on original cwd")
	}
}

func TestCapabilityPolicyLateRepositoryErrorsKeepTypedClass(t *testing.T) {
	for _, tt := range []struct{ body, code string }{{"capabilities: {schema: 1}", "capability_policy_invalid"}, {"mcp: {tools: {access: read}}", "capability_policy_mismatch"}} {
		s := newMemorySource(t)
		s.files[filepath.Join(s.configDir, "gitcode-mcp", "config.yaml")] = []byte("capabilities: {schema: 1}")
		s.cwd = filepath.Join(s.homeDir, "workspace", "repo")
		s.dirs[filepath.Join(s.cwd, ".git")] = true
		repoPath := filepath.Join(s.cwd, ".gitcode", "gitcode-mcp.yaml")
		s.files[repoPath] = []byte(tt.body)
		pinned, _, err := PrepareCapabilityPolicy(s, "", "")
		if err != nil {
			t.Fatal(err)
		}
		_, err = LoadEffective(pinned, Overrides{})
		var policy capability.PolicyError
		if !errors.As(err, &policy) || policy.Code != tt.code || strings.Contains(err.Error(), repoPath) {
			t.Fatalf("late policy error lost its finite class: %v", err)
		}
	}
}

func TestCapabilityPolicyExpectedAndMissingSources(t *testing.T) {
	s := newMemorySource(t)
	if _, p, e := PrepareCapabilityPolicy(s, "", ""); e != nil || p != nil {
		t.Fatal("legacy", e)
	}
	for _, path := range []string{"/unavailable/private.yaml"} {
		_, _, err := PrepareCapabilityPolicy(s, path, "")
		var e capability.PolicyError
		if !errors.As(err, &e) || strings.Contains(err.Error(), path) {
			t.Fatal(err)
		}
	}
	if _, _, err := PrepareCapabilityPolicy(s, "", "policy-required"); err == nil {
		t.Fatal("lost managed marker")
	}
	path := filepath.Join(s.configDir, "instance.yaml")
	s.files[path] = []byte("capabilities: {schema: 1, ceiling: {allow: []}}")
	_, p, e := PrepareCapabilityPolicy(s, path, "")
	if e != nil {
		t.Fatal(e)
	}
	if p.Check("get_source") == nil {
		t.Fatal("empty allow lost")
	}
	if _, _, e = PrepareCapabilityPolicy(s, path, p.Fingerprint()); e != nil {
		t.Fatal(e)
	}
}
