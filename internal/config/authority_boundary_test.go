package config

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// Local intent must never become the legacy remote defaults merely because an
// old loader ignores a key. These are in-memory files, not real authority data.
func TestLocalProjectIntentCannotFallThroughRemoteConfig(t *testing.T) {
	for _, format := range []string{"yaml", "json"} {
		for _, loader := range []string{"effective", "legacy"} {
			t.Run(format+"/"+loader, func(t *testing.T) {
				src := newMemorySource(t)
				path := filepath.Join(src.configDir, "intent."+format)
				content := `{"project":{"authority":"local_authoritative"},"cache_mode":"global"}`
				if format == "yaml" {
					content = "project: {authority: local_authoritative}\ncache_mode: global\n"
				}
				src.files[path] = []byte(content)
				if loader == "legacy" && format == "json" {
					src.env[EnvConfigPath] = path
				} else {
					src.env[EnvMCPConfigPath] = path
				}
				var err error
				if loader == "legacy" {
					_, err = Load(src, Overrides{CacheMode: CacheModeGlobal})
				} else {
					_, err = LoadEffective(src, Overrides{CacheMode: CacheModeGlobal})
				}
				if err == nil {
					t.Fatal("local project intent silently selected remote cache defaults")
				}
			})
		}
	}
}

func TestLocalProjectCannotHideBeyondMergeScanDepth(t *testing.T) {
	var input strings.Builder
	input.WriteString("root0: &root0 {project: {authority: local_authoritative}}\n")
	for i := 1; i <= 12; i++ {
		fmt.Fprintf(&input, "root%d: &root%d {<<: *root%d}\n", i, i, i-1)
	}
	input.WriteString("<<: *root12\n")
	if _, _, err := parseYAMLConfig([]byte(input.String()), ""); err == nil {
		t.Fatal("finite deep YAML merge selected remote defaults")
	}
	remote := strings.Replace(input.String(), "project: {authority: local_authoritative}", "format: json", 1)
	cfg, _, err := parseYAMLConfig([]byte(remote), "")
	if err != nil || cfg.Format == nil || *cfg.Format != "json" {
		t.Fatal("valid deep legacy merge compatibility changed")
	}
}

func TestLocalProjectIntentCannotHideInYAMLMerge(t *testing.T) {
	for _, input := range []string{
		"intent: &intent {project: {authority: local_authoritative}}\n<<: *intent\n",
		"intent: &intent {local_projects: {example: {}}}\n<<: [*intent]\n",
	} {
		if _, _, err := parseYAMLConfig([]byte(input), ""); err == nil {
			t.Fatal("YAML merge hid local authority selector")
		}
	}
}

func TestLocalProjectCaseAliasCannotFallThroughRemoteConfig(t *testing.T) {
	if _, _, err := parseYAMLConfig([]byte("Project: {authority: local_authoritative}\n"), ""); err == nil {
		t.Fatal("case alias selected remote defaults")
	}
	if err := rejectProjectJSON([]byte(`{"PROJECT":{"authority":"local_authoritative"}}`)); err == nil {
		t.Fatal("case alias selected remote JSON defaults")
	}
}
