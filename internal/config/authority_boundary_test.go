package config

import (
	"path/filepath"
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
