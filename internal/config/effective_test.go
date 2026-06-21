package config

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestEffectiveConfigScenarios(t *testing.T) {
	t.Run("SCN-CONFIG-LOCATE-DEFAULT-YAML", func(t *testing.T) {
		src := newMemorySource(t)
		loc := Locate(src)
		want := filepath.Join(src.configDir, "gitcode-mcp", "config.yaml")
		if loc.Path != want || loc.Source != "defaults" || loc.Format != "yaml" || loc.Exists {
			t.Fatalf("locate = %#v want path %q defaults yaml missing", loc, want)
		}
	})

	t.Run("SCN-CONFIG-LEGACY-JSON-COMPAT", func(t *testing.T) {
		src := newMemorySource(t)
		legacy := filepath.Join(src.homeDir, "legacy.json")
		src.env[EnvConfigPath] = legacy
		src.files[legacy] = []byte(`{"cache_path":"/tmp/legacy-cache.db","format":"json"}`)
		eff, err := LoadEffective(src, Overrides{})
		if err != nil {
			t.Fatalf("LoadEffective returned error: %v", err)
		}
		if eff.Location.Source != "legacy-json" || eff.Location.Format != "json" || eff.Config.CachePath != "/tmp/legacy-cache.db" || eff.FieldSources["cache_path"] != "legacy-json" {
			t.Fatalf("legacy effective config = %#v", eff)
		}
	})

	t.Run("SCN-AUTH-ENV-TOKEN-PRESENT", func(t *testing.T) {
		src := newMemorySource(t)
		src.env[EnvToken] = "secret-token-value"
		eff, err := LoadEffective(src, Overrides{})
		if err != nil {
			t.Fatalf("LoadEffective returned error: %v", err)
		}
		provider := EnvCredentialProvider{Source: src}
		secret, status, err := provider.Resolve(context.Background(), eff)
		if err != nil {
			t.Fatalf("Resolve returned error: %v", err)
		}
		if secret.Value() != "secret-token-value" || !status.Present || status.Source != "env:GITCODE_TOKEN" {
			t.Fatalf("status=%#v secret=%q", status, secret.Value())
		}
		rendered := RenderCredentialStatus(status)
		if strings.Contains(rendered, "secret-token-value") {
			t.Fatalf("rendered status leaked token: %q", rendered)
		}
	})

	t.Run("SCN-AUTH-KEYRING-PRESENT", func(t *testing.T) {
		src := newMemorySource(t)
		eff, err := LoadEffective(src, Overrides{})
		if err != nil {
			t.Fatalf("LoadEffective returned error: %v", err)
		}
		provider := StaticCredentialProvider{Source: "keyring", Token: "keyring-secret", StoreMode: "auto"}
		_, status, err := provider.Resolve(context.Background(), eff)
		if err != nil {
			t.Fatalf("Resolve returned error: %v", err)
		}
		if !status.Present || status.Source != "keyring" {
			t.Fatalf("status=%#v", status)
		}
		if strings.Contains(RenderCredentialStatus(status), "keyring-secret") {
			t.Fatalf("rendered keyring status leaked token")
		}
	})

	t.Run("SCN-AUTH-MISSING-TOKEN", func(t *testing.T) {
		src := newMemorySource(t)
		eff, err := LoadEffective(src, Overrides{})
		if err != nil {
			t.Fatalf("LoadEffective returned error: %v", err)
		}
		status := EnvCredentialProvider{Source: src}.Status(context.Background(), eff)
		if status.Present || status.ErrorClass != "token-missing" || status.Remediation == "" {
			t.Fatalf("status=%#v", status)
		}
	})

	t.Run("SCN-AUTH-DEFAULT-CHAIN-LISTS-SOURCES", func(t *testing.T) {
		src := newMemorySource(t)
		eff, err := LoadEffective(src, Overrides{})
		if err != nil {
			t.Fatalf("LoadEffective returned error: %v", err)
		}
		status := DefaultCredentialProvider(src).Status(context.Background(), eff)
		if status.Present || status.ErrorClass != "token-missing" || status.Source != "missing" {
			t.Fatalf("status=%#v", status)
		}
		rendered := RenderCredentialStatus(status)
		for _, want := range []string{"available_sources:", "env:GITCODE_TOKEN", "keychain"} {
			if !strings.Contains(rendered, want) {
				t.Fatalf("rendered missing %q in %q", want, rendered)
			}
		}
	})
}
