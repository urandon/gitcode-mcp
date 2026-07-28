package buildinfo

import (
	"runtime/debug"
	"testing"
)

func TestResolvePrefersLinkerMetadata(t *testing.T) {
	got := resolve("v1.2.3", "linker-commit", "2026-07-28", &debug.BuildInfo{
		Main: debug.Module{Version: "v9.9.9"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "build-commit"},
			{Key: "vcs.time", Value: "build-time"},
		},
	})
	if got.Version != "1.2.3" || got.Commit != "linker-commit" || got.Date != "2026-07-28" || got.Source != "linker" {
		t.Fatalf("resolve linker metadata = %#v", got)
	}
}

func TestResolveFallsBackToGoBuildInfo(t *testing.T) {
	got := resolve(defaultVersion, "", "", &debug.BuildInfo{
		Main: debug.Module{Version: "v0.1.4"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "77dc4907f91dc3a30cc6af13136d98db3b3df533"},
			{Key: "vcs.time", Value: "2026-07-02T15:22:36Z"},
		},
	})
	if got.Version != "0.1.4" || got.ShortCommit() != "77dc4907f91d" || got.Date != "2026-07-02T15:22:36Z" || got.Source != "go_build_info" {
		t.Fatalf("resolve Go build info = %#v", got)
	}
}

func TestResolveKeepsDefaultForDevelopmentBuild(t *testing.T) {
	got := resolve(defaultVersion, "", "", &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}})
	if got.Version != defaultVersion || got.Source != "default" {
		t.Fatalf("resolve development build = %#v", got)
	}
}
