package config

import (
	"bytes"
	"io"
	"os"

	"gopkg.in/yaml.v3"
)

// RegisterLocalProject is a confirmed CLI-only trusted-config write. Reuse the
// existing configuration lock and atomic replacement, not a new setup journal.
// The caller validates the complete candidate through the shared service.
func RegisterLocalProject(src Source, selector string, binding LocalProjectConfig, validate func(map[string]LocalProjectConfig) error) error {
	if ValidateLocalProjects(ServiceModeLocal, map[string]LocalProjectConfig{selector: binding}) != nil {
		return LocalProjectConfigError{}
	}
	if src == nil {
		src = OSSource{}
	}
	loc := Locate(src)
	if loc.Path == "" || loc.Format != "yaml" {
		return LocalProjectConfigError{}
	}
	release, err := acquireFeedbackSetupLock(loc.Path)
	if err != nil {
		return LocalProjectConfigError{}
	}
	defer release()
	var data []byte
	fileReader, err := os.Open(loc.Path)
	if err == nil {
		data, err = io.ReadAll(io.LimitReader(fileReader, (1<<20)+1))
		_ = fileReader.Close()
	}
	if os.IsNotExist(err) {
		data = nil
		err = nil
	}
	if err != nil || len(data) > 1<<20 {
		return LocalProjectConfigError{}
	}
	file, _, err := parseYAMLConfig(data, "")
	if err != nil {
		return LocalProjectConfigError{}
	}
	if previous, ok := file.LocalProjects[selector]; ok && previous != binding {
		return LocalProjectConfigError{}
	}
	var doc yaml.Node
	if len(bytes.TrimSpace(data)) > 0 && yaml.Unmarshal(data, &doc) != nil {
		return LocalProjectConfigError{}
	}
	root, err := ensureYAMLMapping(&doc)
	if err != nil {
		return LocalProjectConfigError{}
	}
	section, err := ensureYAMLMapValue(root, "service")
	if err != nil {
		return LocalProjectConfigError{}
	}
	setYAMLScalar(section, "mode", ServiceModeLocal, "!!str")
	projects, err := ensureYAMLMapValue(root, "local_projects")
	if err != nil {
		return LocalProjectConfigError{}
	}
	found := false
	for i := 0; i+1 < len(projects.Content); i += 2 {
		if projects.Content[i].Value == selector {
			found = true
		}
	}
	if !found {
		var node yaml.Node
		if node.Encode(binding) != nil {
			return LocalProjectConfigError{}
		}
		projects.Content = append(projects.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: selector}, &node)
	}
	out, err := yaml.Marshal(&doc)
	if err != nil {
		return LocalProjectConfigError{}
	}
	parsed, _, err := parseYAMLConfig(out, "")
	if err != nil || ValidateLocalProjects(ServiceModeLocal, parsed.LocalProjects) != nil {
		return LocalProjectConfigError{}
	}
	// Validate canonical identity/ref aliases across the complete candidate while
	// the same trusted-config lock still fences competing setup calls.
	if validate == nil {
		return LocalProjectConfigError{}
	}
	if err := validate(parsed.LocalProjects); err != nil {
		return err
	}
	if bytes.Equal(data, out) {
		return nil
	}
	if durableWriteFeedbackConfig(loc.Path, out) != nil {
		return LocalProjectConfigError{}
	}
	return nil
}
