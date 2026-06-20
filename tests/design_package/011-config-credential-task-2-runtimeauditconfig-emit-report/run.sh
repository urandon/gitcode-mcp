#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
SCENARIO_DIR="$ROOT/tests/design_package/011-config-credential-task-2-runtimeauditconfig-emit-report"
TEST_FILE="$SCENARIO_DIR/runtimeauditconfig_product_test.go"
trap 'rm -f "$TEST_FILE"' EXIT

cat > "$TEST_FILE" <<'GOEOF'
package runtimeauditconfig_product_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gitcode-mcp/internal/cli"
	"gitcode-mcp/internal/config"
)

type cliResult struct{ code int; stdout, stderr string }

func TestRuntimeAuditConfigDoctorProductScenarios(t *testing.T) {
	t.Run("011-config-credential-task-2-runtimeauditconfig-emit-report-scenario-1", func(t *testing.T) {
		tmp := t.TempDir()
		cfgPath := filepath.Join(tmp, "active.yaml")
		cachePath := filepath.Join(tmp, "cache.db")
		writeFile(t, cfgPath, "cache_path: "+cachePath+"\ncredential:\n  store: env\n")

		result := runCLI(t, map[string]string{
			"GITCODE_MCP_CONFIG": cfgPath,
			"GITCODE_TOKEN":      "secret-token-value",
		}, "doctor", "--runtime-audit", "--repo", "fixture-repo")
		requireSuccess(t, result)
		combined := result.stdout + result.stderr
		assertContainsAll(t, combined, []string{
			"repo_id: fixture-repo",
			"config:",
			"version:",
			"config_path: " + cfgPath,
			"config_source: explicit-yaml",
			"config_format: yaml",
			"config_exists: true",
			"cache_path: " + cachePath,
			"cache_path_source: explicit-yaml",
			"credential_source: env:GITCODE_TOKEN",
			"token_present: true",
			"credential_store_mode: env",
			"failure_classes:",
			"handoff_fields:",
			"resolved_config_path: " + cfgPath,
			"resolved_cache_path: " + cachePath,
			"cache: not_reported_by_owner",
			"repo: not_reported_by_owner",
			"mcp: not_reported_by_owner",
			"index: not_reported_by_owner",
		})
		assertNoLeakOrSynthesizedSuccess(t, combined, []string{"secret-token-value"})
	})

	t.Run("011-config-credential-task-2-runtimeauditconfig-emit-report-scenario-2", func(t *testing.T) {
		checkValidYAMLJSON(t)
		checkMissingConfig(t)
		checkMalformedConfig(t)
		checkLegacyJSON(t)
		checkMissingToken(t)
		checkKeyringUnavailable(t)
	})
}

func checkValidYAMLJSON(t *testing.T) {
	t.Helper()
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "valid.yaml")
	cachePath := filepath.Join(tmp, "cache.db")
	writeFile(t, cfgPath, "cache_path: "+cachePath+"\ncredential:\n  store: env\n")
	result := runCLI(t, map[string]string{"GITCODE_MCP_CONFIG": cfgPath, "GITCODE_TOKEN": "secret-token-value"}, "doctor", "--runtime-audit", "--repo", "fixture-repo", "--format", "json")
	requireSuccess(t, result)
	payload := decodeAuditPayload(t, result.stdout)
	if payload.RepoID != "fixture-repo" || payload.Config.ConfigSource != "explicit-yaml" || payload.Config.ConfigFormat != "yaml" || !payload.Config.ConfigExists || payload.Config.CachePath != cachePath || payload.Config.CachePathSource != "explicit-yaml" || payload.Config.CredentialSource != "env:GITCODE_TOKEN" || !payload.Config.TokenPresent {
		t.Fatalf("valid yaml audit payload mismatch: %#v", payload)
	}
	assertHandoffFields(t, payload)
	assertNoJSONOwnerSections(t, result.stdout)
	assertNoLeakOrSynthesizedSuccess(t, result.stdout+result.stderr, []string{"secret-token-value"})
}

func checkMissingConfig(t *testing.T) {
	t.Helper()
	tmp := t.TempDir()
	missingPath := filepath.Join(tmp, "missing.yaml")
	result := runCLI(t, map[string]string{"GITCODE_MCP_CONFIG": missingPath}, "doctor", "--runtime-audit", "--repo", "fixture-repo", "--format", "json")
	requireSuccess(t, result)
	payload := decodeAuditPayload(t, result.stdout)
	if payload.Config.ConfigPath != missingPath || payload.Config.ConfigExists || payload.Config.ConfigSource != "explicit-yaml" || payload.Config.TokenPresent {
		t.Fatalf("missing config audit payload mismatch: %#v", payload.Config)
	}
	assertHasFailureClass(t, payload, "no-config")
	assertHasFailureClass(t, payload, "token-missing")
	assertHandoffFields(t, payload)
	assertNoJSONOwnerSections(t, result.stdout)
}

func checkMalformedConfig(t *testing.T) {
	t.Helper()
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "bad.yaml")
	writeFile(t, cfgPath, "file-contained-secret malformed line\n")
	result := runCLI(t, map[string]string{"GITCODE_MCP_CONFIG": cfgPath, "GITCODE_TOKEN": "secret-token-value"}, "doctor", "--runtime-audit", "--repo", "fixture-repo", "--format", "json")
	requireSuccess(t, result)
	payload := decodeAuditPayload(t, result.stdout)
	assertHasFailureClass(t, payload, "config-malformed")
	assertHandoffFields(t, payload)
	assertNoJSONOwnerSections(t, result.stdout)
	assertNoLeakOrSynthesizedSuccess(t, result.stdout+result.stderr, []string{"file-contained-secret", "secret-token-value"})
}

func checkLegacyJSON(t *testing.T) {
	t.Helper()
	tmp := t.TempDir()
	legacyPath := filepath.Join(tmp, "legacy.json")
	cachePath := filepath.Join(tmp, "legacy-cache.db")
	writeFile(t, legacyPath, `{"cache_path":"`+cachePath+`","credential":{"store":"env"}}`)
	result := runCLI(t, map[string]string{"GITCODE_CONFIG": legacyPath}, "doctor", "--runtime-audit", "--repo", "fixture-repo", "--format", "json")
	requireSuccess(t, result)
	payload := decodeAuditPayload(t, result.stdout)
	if payload.Config.ConfigSource != "legacy-json" || payload.Config.ConfigFormat != "json" || payload.Config.CachePath != cachePath {
		t.Fatalf("legacy audit payload mismatch: %#v", payload.Config)
	}
	assertHasFailureClass(t, payload, "legacy-config")
	assertHasFailureClass(t, payload, "token-missing")
	assertHandoffFields(t, payload)
	assertNoJSONOwnerSections(t, result.stdout)
}

func checkMissingToken(t *testing.T) {
	t.Helper()
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "missing-token.yaml")
	writeFile(t, cfgPath, "credential:\n  store: env\n")
	result := runCLI(t, map[string]string{"GITCODE_MCP_CONFIG": cfgPath}, "doctor", "--runtime-audit", "--repo", "fixture-repo", "--format", "json")
	requireSuccess(t, result)
	payload := decodeAuditPayload(t, result.stdout)
	if payload.Config.TokenPresent || payload.Config.CredentialSource != "missing" {
		t.Fatalf("missing token audit payload mismatch: %#v", payload.Config)
	}
	assertHasFailureClass(t, payload, "token-missing")
	assertHandoffFields(t, payload)
	assertNoJSONOwnerSections(t, result.stdout)
}

func checkKeyringUnavailable(t *testing.T) {
	t.Helper()
	result := runGoTest(t, "./internal/cli", "TestRuntimeAuditDoctorCommand/SCN-RUNTIME-AUDIT-CLI-JSON")
	requireSuccess(t, result)
	assertNoLeakOrSynthesizedSuccess(t, result.stdout+result.stderr, []string{"raw dbus failure details", "secret-provider-stack"})
}

func runCLI(t *testing.T, env map[string]string, args ...string) cliResult {
	t.Helper()
	var stdout, stderr bytes.Buffer
	withIsolatedEnv(t, env, func() {
		code := cli.Execute(args, &stdout, &stderr)
		if code != 0 {
			stderr.WriteString("\nexit_code_nonzero")
		}
	})
	return cliResult{stdout: stdout.String(), stderr: stderr.String(), code: exitCodeFromStderr(stderr.String())}
}

func runGoTest(t *testing.T, pkg string, run string) cliResult {
	t.Helper()
	cmd := exec.Command("go", "test", pkg, "-run", run, "-count=1", "-v")
	cmd.Dir = rootDir(t)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		code = 1
	}
	return cliResult{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

func withIsolatedEnv(t *testing.T, env map[string]string, fn func()) {
	t.Helper()
	keys := []string{"GITCODE_MCP_CONFIG", "GITCODE_CONFIG", "GITCODE_TOKEN", "GITCODE_MCP_CACHE_DIR", "GITCODE_API_URL", "GITCODE_MCP_TEST_KEYRING_UNAVAILABLE"}
	for _, key := range keys {
		old, had := os.LookupEnv(key)
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
		key, old := key, old
		if had {
			t.Cleanup(func() { _ = os.Setenv(key, old) })
		} else {
			t.Cleanup(func() { _ = os.Unsetenv(key) })
		}
	}
	for key, value := range env {
		if err := os.Setenv(key, value); err != nil {
			t.Fatal(err)
		}
	}
	fn()
}

type auditPayload struct {
	RepoID string `json:"repo_id"`
	Config config.RuntimeAuditConfigReport `json:"config"`
	Cache string `json:"cache,omitempty"`
	Repo string `json:"repo,omitempty"`
	MCP string `json:"mcp,omitempty"`
	Index string `json:"index,omitempty"`
}

func decodeAuditPayload(t *testing.T, data string) auditPayload {
	t.Helper()
	var payload auditPayload
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		t.Fatalf("invalid runtime audit json: %v\n%s", err, data)
	}
	return payload
}

func assertHandoffFields(t *testing.T, payload auditPayload) {
	t.Helper()
	if payload.Config.HandoffFields.ResolvedConfigPath == "" || payload.Config.HandoffFields.ResolvedCachePath == "" || payload.Config.HandoffFields.ConfigLocation.Format == "" || payload.Config.HandoffFields.CredentialStatus.Source == "" {
		t.Fatalf("missing typed handoff fields: %#v", payload.Config.HandoffFields)
	}
}

func assertHasFailureClass(t *testing.T, payload auditPayload, want string) {
	t.Helper()
	for _, got := range payload.Config.FailureClasses {
		if got == want {
			return
		}
	}
	t.Fatalf("missing failure class %q in %#v; payload=%#v", want, payload.Config.FailureClasses, payload.Config)
}

func assertNoJSONOwnerSections(t *testing.T, out string) {
	t.Helper()
	for _, forbidden := range []string{"\"cache\":", "\"repo\":", "\"mcp\":", "\"index\":"} {
		if strings.Contains(out, forbidden) {
			t.Fatalf("json synthesized non-config owner section %s in %s", forbidden, out)
		}
	}
}

func assertNoLeakOrSynthesizedSuccess(t *testing.T, out string, forbidden []string) {
	t.Helper()
	allForbidden := append([]string{"cache: ok", "repo: ok", "mcp: ok", "index: ok", "Authorization", "Bearer ", "file-contained-secret"}, forbidden...)
	for _, value := range allForbidden {
		if value != "" && strings.Contains(out, value) {
			t.Fatalf("runtime audit leaked forbidden value or synthesized success %q in %s", value, out)
		}
	}
}

func assertContainsAll(t *testing.T, out string, values []string) {
	t.Helper()
	for _, value := range values {
		if !strings.Contains(out, value) {
			t.Fatalf("missing %q in output:\n%s", value, out)
		}
	}
}

func requireSuccess(t *testing.T, result cliResult) {
	t.Helper()
	if result.code != 0 {
		t.Fatalf("command failed: code=%d stdout=%q stderr=%q", result.code, result.stdout, result.stderr)
	}
}

func exitCodeFromStderr(stderr string) int {
	if strings.Contains(stderr, "exit_code_nonzero") {
		return 1
	}
	return 0
}

func rootDir(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(wd, "go.mod")); err == nil {
			return wd
		}
		next := filepath.Dir(wd)
		if next == wd {
			t.Fatal("could not locate repository root")
		}
		wd = next
	}
}

func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
GOEOF

go test ./tests/design_package/011-config-credential-task-2-runtimeauditconfig-emit-report
