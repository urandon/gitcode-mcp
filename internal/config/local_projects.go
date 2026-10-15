package config

import (
	"bytes"
	"encoding/json"
	"gopkg.in/yaml.v3"
	"io"
	"path/filepath"
	"regexp"
	"strings"
)

const ServiceModeLocal = "local"

// Bindings contain opaque names only. Paths are resolved by machine-local CLI
// setup, never accepted from a project client or repository-owned config.
type LocalProjectConfig struct {
	Backend    string `json:"backend" yaml:"backend"`
	ProjectID  string `json:"project_id" yaml:"project_id"`
	StorageRef string `json:"storage_ref" yaml:"storage_ref"`
	Ref        string `json:"ref" yaml:"ref"`
	ReadOnly   bool   `json:"read_only" yaml:"read_only"`
	RAGProfile string `json:"rag_profile,omitempty" yaml:"rag_profile"`
}

func (p *LocalProjectConfig) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode || n.Anchor != "" {
		return LocalProjectConfigError{}
	}
	seen := map[string]bool{}
	for i := 0; i < len(n.Content); i += 2 {
		key, value := n.Content[i], n.Content[i+1]
		if seen[key.Value] || key.Kind != yaml.ScalarNode || value.Kind != yaml.ScalarNode || value.Anchor != "" {
			return LocalProjectConfigError{}
		}
		switch key.Value {
		case "backend", "project_id", "storage_ref", "ref", "read_only", "rag_profile":
		default:
			return LocalProjectConfigError{}
		}
		seen[key.Value] = true
		if key.Value == "read_only" && value.Tag != "!!bool" || key.Value != "read_only" && value.Tag != "!!str" {
			return LocalProjectConfigError{}
		}
	}
	type plain LocalProjectConfig
	if err := n.Decode((*plain)(p)); err != nil {
		return LocalProjectConfigError{}
	}
	return nil
}
func (p *LocalProjectConfig) UnmarshalJSON(data []byte) error {
	// A flat binding has fixed scalar fields; reject duplicates and JSON's
	// case-insensitive struct aliases before decoding the authoritative config.
	d := json.NewDecoder(bytes.NewReader(data))
	start, err := d.Token()
	if err != nil || start != json.Delim('{') {
		return LocalProjectConfigError{}
	}
	seen := map[string]bool{}
	for d.More() {
		key, err := d.Token()
		name, ok := key.(string)
		if err != nil || !ok || seen[name] {
			return LocalProjectConfigError{}
		}
		switch name {
		case "backend", "project_id", "storage_ref", "ref", "read_only", "rag_profile":
		default:
			return LocalProjectConfigError{}
		}
		seen[name] = true
		var v json.RawMessage
		if d.Decode(&v) != nil || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return LocalProjectConfigError{}
		}
	}
	if end, err := d.Token(); err != nil || end != json.Delim('}') {
		return LocalProjectConfigError{}
	}
	if _, err := d.Token(); err != io.EOF {
		return LocalProjectConfigError{}
	}
	type plain LocalProjectConfig
	if json.Unmarshal(data, (*plain)(p)) != nil {
		return LocalProjectConfigError{}
	}
	return nil
}

type LocalProjectConfigError struct{}

func (LocalProjectConfigError) Error() string          { return "config: invalid local project configuration" }
func (LocalProjectConfigError) DiagnosticCode() string { return "local_git_invalid_configuration" }

var localName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)
var localProjectID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

func ValidateLocalProjects(mode string, projects map[string]LocalProjectConfig) error {
	if mode != "" && mode != "cache" && mode != ServiceModeLocal || len(projects) > 32 || mode == ServiceModeLocal && len(projects) == 0 || len(projects) > 0 && mode != ServiceModeLocal {
		return LocalProjectConfigError{}
	}
	for selector, p := range projects {
		if !localName.MatchString(selector) || !localName.MatchString(p.StorageRef) || !localProjectID.MatchString(p.ProjectID) || p.Backend != "local_git" || len(p.Ref) > 256 || !strings.HasPrefix(p.Ref, "refs/heads/") || p.RAGProfile != "" && p.RAGProfile != "disabled" && !localName.MatchString(p.RAGProfile) {
			return LocalProjectConfigError{}
		}
	}
	return nil
}

func LocalProjectStorePath(src Source, storageRef string) (string, error) {
	if !localName.MatchString(storageRef) {
		return "", LocalProjectConfigError{}
	}
	if src == nil {
		src = OSSource{}
	}
	dir, err := src.UserConfigDir()
	if err != nil || !filepath.IsAbs(dir) {
		return "", LocalProjectConfigError{}
	}
	return filepath.Join(dir, "gitcode-mcp", "local-projects", storageRef+".git"), nil
}
