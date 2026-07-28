package buildinfo

import (
	"runtime/debug"
	"strings"
)

const defaultVersion = "0.1.0"

var (
	Version = defaultVersion
	Commit  = ""
	Date    = ""
)

type Info struct {
	Version string
	Commit  string
	Date    string
	Source  string
}

func Current() Info {
	build, _ := debug.ReadBuildInfo()
	return resolve(Version, Commit, Date, build)
}

func resolve(version, commit, date string, build *debug.BuildInfo) Info {
	info := Info{
		Version: strings.TrimPrefix(strings.TrimSpace(version), "v"),
		Commit:  strings.TrimSpace(commit),
		Date:    strings.TrimSpace(date),
		Source:  "linker",
	}
	linkerMetadata := info.Version != "" && (info.Version != defaultVersion || info.Commit != "" || info.Date != "")
	if linkerMetadata {
		return info
	}
	if build != nil {
		moduleVersion := strings.TrimPrefix(strings.TrimSpace(build.Main.Version), "v")
		if moduleVersion != "" && moduleVersion != "(devel)" {
			info.Version = moduleVersion
			info.Source = "go_build_info"
		}
		for _, setting := range build.Settings {
			switch setting.Key {
			case "vcs.revision":
				if info.Commit == "" {
					info.Commit = strings.TrimSpace(setting.Value)
				}
			case "vcs.time":
				if info.Date == "" {
					info.Date = strings.TrimSpace(setting.Value)
				}
			}
		}
		if info.Source == "linker" && (info.Commit != "" || info.Date != "") {
			info.Source = "go_build_info"
		}
	}
	if info.Version == "" {
		info.Version = defaultVersion
	}
	if info.Source == "linker" && !linkerMetadata {
		info.Source = "default"
	}
	return info
}

func (i Info) ShortCommit() string {
	if len(i.Commit) <= 12 {
		return i.Commit
	}
	return i.Commit[:12]
}
