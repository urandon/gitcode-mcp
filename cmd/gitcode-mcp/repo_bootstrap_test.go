package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/config"
)

func bootstrapSource(t *testing.T, selection string) (*testSource, string) {
	t.Helper()
	src := newTestSource(t)
	root := src.cwd
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o700); err != nil {
		t.Fatal("cannot create fixture Git marker")
	}
	src.dirs[filepath.Join(root, ".git")] = true
	src.cwd = filepath.Join(root, "nested")
	if err := os.Mkdir(src.cwd, 0o700); err != nil {
		t.Fatal("cannot create fixture working directory")
	}
	switch selection {
	case "environment":
		src.env[config.EnvMCPCacheDir] = filepath.Join(root, "inherited-cache")
	case "config":
		path := filepath.Join(root, "global.json")
		src.env[config.EnvConfigPath] = path
		data, err := json.Marshal(map[string]string{"cache_path": filepath.Join(root, "inherited-cache", "cache.db")})
		if err != nil {
			t.Fatal("cannot encode fixture configuration")
		}
		src.files[path] = data
	}
	return src, root
}

func bootstrapArgs() []string {
	return []string{"repo", "init-local", "--repo", "example-owner/example-repo", "--owner", "example-owner", "--name", "example-repo", "--format", "json"}
}

func TestEntrypointRepoInitLocalKeepsInheritedCacheImplicit(t *testing.T) {
	for _, selection := range []string{"default", "environment", "config"} {
		for _, form := range []string{"omitted", "empty", "equals-empty", "global-equals-empty", "flags-before-subcommand"} {
			t.Run(selection+"/"+form, func(t *testing.T) {
				src, root := bootstrapSource(t, selection)
				eff, err := config.LoadEffective(src, config.Overrides{})
				if err != nil {
					t.Fatal("cannot load isolated fixture config")
				}
				args := bootstrapArgs()
				switch form {
				case "empty":
					args = append(args, "--cache-path", "")
				case "equals-empty":
					args = append(args, "--cache-path=")
				case "global-equals-empty":
					args = append([]string{"--cache-path="}, args...)
				case "flags-before-subcommand":
					args = append([]string{"repo"}, append(args[2:], "init-local")...)
				}
				for attempt := 0; attempt < 2; attempt++ {
					var out, diag bytes.Buffer
					if code := run(args, strings.NewReader(""), &out, &diag, src); code != 0 {
						t.Fatalf("bootstrap attempt %d failed with exit %d", attempt, code)
					}
					var result struct {
						CachePath     string `json:"cache_path"`
						BindingStatus string `json:"binding_status"`
					}
					if json.Unmarshal(out.Bytes(), &result) != nil || result.CachePath != filepath.Join(root, ".gitcode", "mcp", "cache.db") {
						t.Fatal("bootstrap did not select the fixed worktree cache")
					}
					if attempt == 1 && result.BindingStatus != "existing" {
						t.Fatal("repeat bootstrap did not reuse the binding")
					}
				}
				if _, err := os.Stat(eff.Config.CachePath); !os.IsNotExist(err) {
					t.Fatal("bootstrap touched the inherited cache")
				}
				assertBootstrapBinding(t, filepath.Join(root, ".gitcode", "mcp", "cache.db"))
			})
		}
	}
}

func TestEntrypointRepoInitLocalRetainsGlobalValueValidation(t *testing.T) {
	src, root := bootstrapSource(t, "default")
	var out, diag bytes.Buffer
	args := append([]string{"--cache-path", ""}, bootstrapArgs()...)
	if run(args, strings.NewReader(""), &out, &diag, src) != 2 {
		t.Fatal("empty standalone global value no longer rejected by startup parser")
	}
	if _, err := os.Stat(filepath.Join(root, ".gitcode")); !os.IsNotExist(err) {
		t.Fatal("invalid global flag produced bootstrap effects")
	}
}

func TestEntrypointRepoInitLocalRejectsExplicitCacheBeforeEffects(t *testing.T) {
	for _, form := range []string{"local", "local-equals", "global", "global-equals"} {
		t.Run(form, func(t *testing.T) {
			src, root := bootstrapSource(t, "default")
			path := filepath.Join(root, "explicit-cache.db")
			args := bootstrapArgs()
			switch form {
			case "local":
				args = append(args, "--cache-path", path)
			case "local-equals":
				args = append(args, "--cache-path="+path)
			case "global":
				args = append([]string{"--cache-path", path}, args...)
			case "global-equals":
				args = append([]string{"--cache-path=" + path}, args...)
			}
			var out, diag bytes.Buffer
			if run(args, strings.NewReader(""), &out, &diag, src) == 0 || !strings.Contains(diag.String(), "omit --cache-path") {
				t.Fatal("explicit nonempty cache override was not rejected")
			}
			for _, p := range []string{path, filepath.Join(root, ".gitcode"), filepath.Join(root, ".gitignore")} {
				if _, err := os.Stat(p); !os.IsNotExist(err) {
					t.Fatal("rejected bootstrap produced local effects")
				}
			}
		})
	}
}

func TestEntrypointRepoInitLocalPreservesExistingConfig(t *testing.T) {
	src, root := bootstrapSource(t, "default")
	path := filepath.Join(root, ".gitcode", "gitcode-mcp.yaml")
	if err := os.Mkdir(filepath.Dir(path), 0o700); err != nil {
		t.Fatal("cannot create fixture config directory")
	}
	original := []byte("cache_mode: global\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal("cannot create existing fixture configuration")
	}
	src.files[path] = original
	var out, diag bytes.Buffer
	if run(bootstrapArgs(), strings.NewReader(""), &out, &diag, src) == 0 {
		t.Fatal("conflicting configuration overwritten without confirmation")
	}
	data, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, original) {
		t.Fatal("conflicting configuration changed")
	}
	for _, p := range []string{filepath.Join(root, ".gitignore"), filepath.Join(root, ".gitcode", "mcp")} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatal("conflicting configuration caused bootstrap effects")
		}
	}
	out.Reset()
	diag.Reset()
	if run(append(bootstrapArgs(), "--overwrite"), strings.NewReader(""), &out, &diag, src) != 0 {
		t.Fatal("explicit configuration overwrite failed")
	}
	data, err = os.ReadFile(path)
	if err != nil || strings.TrimSpace(string(data)) != "cache_mode: repo-local" {
		t.Fatal("confirmed bootstrap did not set repo-local mode")
	}
}

func TestEntrypointRepoInitLocalRejectsOverrideEqualToInheritedCache(t *testing.T) {
	for _, selection := range []string{"default", "environment", "config"} {
		for _, form := range []string{"global", "global-equals"} {
			t.Run(selection+"/"+form, func(t *testing.T) {
				src, root := bootstrapSource(t, selection)
				eff, err := config.LoadEffective(src, config.Overrides{})
				if err != nil {
					t.Fatal("cannot load isolated inherited cache config")
				}
				args := bootstrapArgs()
				if form == "global" {
					args = append([]string{"--cache-path", eff.Config.CachePath}, args...)
				} else {
					args = append([]string{"--cache-path=" + eff.Config.CachePath}, args...)
				}
				var out, diag bytes.Buffer
				if run(args, strings.NewReader(""), &out, &diag, src) == 0 || !strings.Contains(diag.String(), "omit --cache-path") {
					t.Fatal("equal-to-inherited explicit override was not rejected")
				}
				for _, p := range []string{eff.Config.CachePath, filepath.Join(root, ".gitcode"), filepath.Join(root, ".gitignore")} {
					if _, err := os.Stat(p); !os.IsNotExist(err) {
						t.Fatal("rejected equal-to-inherited override produced effects")
					}
				}
			})
		}
	}
}

func TestEntrypointRepositoryCommandsRetainInheritedCache(t *testing.T) {
	for _, selection := range []string{"default", "environment", "config"} {
		t.Run(selection, func(t *testing.T) {
			src, _ := bootstrapSource(t, selection)
			eff, err := config.LoadEffective(src, config.Overrides{})
			if err != nil {
				t.Fatal("cannot load fixture config")
			}
			for _, args := range [][]string{
				{"repo", "add", "--repo", "example-owner/example-repo", "--owner", "example-owner", "--name", "example-repo", "--scopes", "issues,wiki", "--format", "json"},
				{"repo", "status", "--repo", "example-owner/example-repo", "--format", "json"},
			} {
				var out, diag bytes.Buffer
				if run(args, strings.NewReader(""), &out, &diag, src) != 0 {
					t.Fatal("sibling repository command failed")
				}
			}
			assertBootstrapBinding(t, eff.Config.CachePath)
		})
	}
}

func assertBootstrapBinding(t *testing.T, path string) {
	t.Helper()
	store, err := cache.NewSQLiteStore(context.Background(), path)
	if err != nil {
		t.Fatal("cannot open fixture cache")
	}
	defer store.Close()
	binding, err := store.GetRepository(context.Background(), "example-owner/example-repo")
	if err != nil || binding.Owner != "example-owner" || binding.Name != "example-repo" {
		t.Fatal("fixture repository binding missing or changed")
	}
}
