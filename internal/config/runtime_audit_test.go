package config

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuntimeAuditConfigReportScenarios(t *testing.T) {
	t.Run("SCN-RUNTIME-AUDIT-VALID-YAML", func(t *testing.T) {
		src := newMemorySource(t)
		path := filepath.Join(src.configDir, "active.yaml")
		src.env[EnvMCPConfigPath] = path
		src.env[EnvToken] = "secret-token-value"
		src.files[path] = []byte("cache_path: /tmp/runtime-cache.db\ncredential:\n  store: env\n")
		report := BuildRuntimeAuditConfigReport(src, Overrides{}, EnvCredentialProvider{Source: src}, "test-version")
		if report.Version != "test-version" || report.ConfigPath != path || report.ConfigSource != "explicit-yaml" || !report.ConfigExists || report.CachePath != "/tmp/runtime-cache.db" || report.CachePathSource != "explicit-yaml" || !report.TokenPresent {
			t.Fatalf("report = %#v", report)
		}
		assertRuntimeAuditNoLeak(t, report, "secret-token-value")
	})

	t.Run("SCN-RUNTIME-AUDIT-MISSING-CONFIG", func(t *testing.T) {
		src := newMemorySource(t)
		report := BuildRuntimeAuditConfigReport(src, Overrides{}, EnvCredentialProvider{Source: src}, "test-version")
		assertHasClass(t, report, "no-config")
		assertHasClass(t, report, "token-missing")
		if report.HandoffFields.ResolvedCachePath == "" || report.HandoffFields.ResolvedConfigPath == "" {
			t.Fatalf("missing handoff fields: %#v", report.HandoffFields)
		}
	})

	t.Run("SCN-RUNTIME-AUDIT-MALFORMED-CONFIG", func(t *testing.T) {
		src := newMemorySource(t)
		path := filepath.Join(src.homeDir, "bad.yaml")
		src.env[EnvMCPConfigPath] = path
		src.files[path] = []byte("malformed line with file-contained-secret")
		report := BuildRuntimeAuditConfigReport(src, Overrides{}, EnvCredentialProvider{Source: src}, "test-version")
		assertHasClass(t, report, "config-malformed")
		assertRuntimeAuditNoLeak(t, report, "file-contained-secret")
	})

	t.Run("SCN-RUNTIME-AUDIT-LEGACY-JSON", func(t *testing.T) {
		src := newMemorySource(t)
		path := filepath.Join(src.homeDir, "legacy.json")
		src.env[EnvConfigPath] = path
		src.files[path] = []byte(`{"cache_path":"/tmp/legacy-cache.db"}`)
		report := BuildRuntimeAuditConfigReport(src, Overrides{}, EnvCredentialProvider{Source: src}, "test-version")
		if report.ConfigSource != "legacy-json" || report.ConfigFormat != "json" || report.CachePath != "/tmp/legacy-cache.db" {
			t.Fatalf("legacy report = %#v", report)
		}
		assertHasClass(t, report, "legacy-config")
	})

	t.Run("SCN-RUNTIME-AUDIT-MISSING-TOKEN", func(t *testing.T) {
		src := newMemorySource(t)
		report := BuildRuntimeAuditConfigReport(src, Overrides{}, EnvCredentialProvider{Source: src}, "test-version")
		if report.TokenPresent || report.CredentialSource != "missing" {
			t.Fatalf("credential report = %#v", report)
		}
		assertHasClass(t, report, "token-missing")
	})

	t.Run("SCN-RUNTIME-AUDIT-KEYRING-UNAVAILABLE", func(t *testing.T) {
		src := newMemorySource(t)
		reporter := StaticCredentialProvider{Source: "keyring", StoreMode: "auto", ErrorClass: "credential-store-unavailable", Remediation: "Use GITCODE_TOKEN or credential.store env."}
		report := BuildRuntimeAuditConfigReport(src, Overrides{}, reporter, "test-version")
		assertHasClass(t, report, "credential-store-unavailable")
		if report.CredentialSource != "keyring" || report.TokenPresent {
			t.Fatalf("credential report = %#v", report)
		}
	})

	t.Run("SCN-RUNTIME-AUDIT-CONFIG-UNREADABLE", func(t *testing.T) {
		src := newMemorySource(t)
		path := filepath.Join(src.homeDir, "unreadable.yaml")
		src.env[EnvMCPConfigPath] = path
		src.readErr[path] = errors.New("permission denied raw provider details")
		report := BuildRuntimeAuditConfigReport(src, Overrides{}, EnvCredentialProvider{Source: src}, "test-version")
		assertHasClass(t, report, "no-config")
		assertHasClass(t, report, "config-unreadable")
		assertRuntimeAuditNoLeak(t, report, "permission denied raw provider details")
	})
}

func assertHasClass(t *testing.T, report RuntimeAuditConfigReport, want string) {
	t.Helper()
	for _, got := range report.FailureClasses {
		if got == want {
			return
		}
	}
	t.Fatalf("missing failure class %q in %#v", want, report.FailureClasses)
}

func assertRuntimeAuditNoLeak(t *testing.T, report RuntimeAuditConfigReport, forbidden string) {
	t.Helper()
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if forbidden != "" && strings.Contains(string(data), forbidden) {
		t.Fatalf("runtime audit leaked %q in %s", forbidden, data)
	}
}
