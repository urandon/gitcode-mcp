package config

import (
	"context"
	"errors"
	"os"
	"sort"
	"strings"
)

type RuntimeAuditConfigReport struct {
	Version             string                    `json:"version"`
	ConfigPath          string                    `json:"config_path"`
	ConfigSource        string                    `json:"config_source"`
	ConfigFormat        string                    `json:"config_format"`
	ConfigExists        bool                      `json:"config_exists"`
	CachePath           string                    `json:"cache_path"`
	CachePathSource     string                    `json:"cache_path_source"`
	CredentialSource    string                    `json:"credential_source"`
	TokenPresent        bool                      `json:"token_present"`
	CredentialStoreMode string                    `json:"credential_store_mode"`
	FailureClasses      []string                  `json:"failure_classes"`
	Remediation         []string                  `json:"remediation,omitempty"`
	HandoffFields       RuntimeAuditHandoffFields `json:"handoff_fields"`
}

type RuntimeAuditHandoffFields struct {
	ResolvedConfigPath string           `json:"resolved_config_path"`
	ResolvedCachePath  string           `json:"resolved_cache_path"`
	ConfigLocation     ConfigLocation   `json:"config_location"`
	CredentialStatus   CredentialStatus `json:"credential_status"`
}

func BuildRuntimeAuditConfigReport(src Source, overrides Overrides, reporter CredentialStatusReporter, version string) RuntimeAuditConfigReport {
	if src == nil {
		src = OSSource{}
	}
	loc := Locate(src)
	report := RuntimeAuditConfigReport{
		Version:        version,
		ConfigPath:     loc.Path,
		ConfigSource:   loc.Source,
		ConfigFormat:   loc.Format,
		ConfigExists:   loc.Exists,
		FailureClasses: configLocationFailureClasses(src, loc),
		HandoffFields:  RuntimeAuditHandoffFields{ResolvedConfigPath: loc.Path, ConfigLocation: loc},
	}
	eff, err := LoadEffective(src, overrides)
	if err != nil {
		report.FailureClasses = append(report.FailureClasses, classifyConfigLoadFailure(src, loc))
		eff = EffectiveConfig{
			Config:           defaultWithSource(src),
			Location:         loc,
			FieldSources:     defaultFieldSources(),
			CredentialPolicy: CredentialConfig{Store: "auto"},
			CachePathSource:  "default",
		}
	} else {
		report.ConfigPath = eff.Location.Path
		report.ConfigSource = eff.Location.Source
		report.ConfigFormat = eff.Location.Format
		report.ConfigExists = eff.Location.Exists
	}
	report.CachePath = eff.Config.CachePath
	report.CachePathSource = eff.CachePathSource
	if reporter == nil {
		reporter = DefaultCredentialProvider(src)
	}
	status := reporter.Status(context.Background(), eff)
	status.Source = RedactDiagnostic(status.Source, src)
	status.ErrorClass = RedactDiagnostic(status.ErrorClass, src)
	status.Remediation = RedactDiagnostic(status.Remediation, src)
	report.CredentialSource = status.Source
	if !status.Present && status.ErrorClass == "token-missing" {
		report.CredentialSource = "missing"
	}
	report.TokenPresent = status.Present
	report.CredentialStoreMode = status.StoreMode
	if status.ErrorClass != "" {
		report.FailureClasses = append(report.FailureClasses, status.ErrorClass)
	}
	if status.Remediation != "" {
		report.Remediation = append(report.Remediation, status.Remediation)
	}
	report.HandoffFields.ResolvedCachePath = eff.Config.CachePath
	report.HandoffFields.CredentialStatus = status
	report.FailureClasses = uniqueNonEmpty(report.FailureClasses)
	report.Remediation = uniqueNonEmpty(report.Remediation)
	return report
}

func configLocationFailureClasses(src Source, loc ConfigLocation) []string {
	classes := []string{}
	if !loc.Exists && (loc.Source == "defaults" || loc.Explicit) {
		classes = append(classes, "no-config")
		if loc.Explicit && loc.Path != "" {
			if _, err := src.ReadFile(loc.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
				classes = append(classes, "config-unreadable")
			}
		}
	}
	if loc.Source == "legacy-json" {
		classes = append(classes, "legacy-config")
	}
	return classes
}

func classifyConfigLoadFailure(src Source, loc ConfigLocation) string {
	if loc.Path == "" || !loc.Exists {
		return "no-config"
	}
	if _, err := src.ReadFile(loc.Path); err != nil {
		if errors.Is(err, os.ErrPermission) {
			return "config-unreadable"
		}
		return "config-unreadable"
	}
	return "config-malformed"
}

func uniqueNonEmpty(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
