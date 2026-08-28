package repositorydocs

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	PolicySchemaV1        = 1
	DefaultPolicyPreset   = "conventional-docs-v1"
	PolicyConfigPath      = ".gitcode/gitcode-mcp.yaml"
	policyMatcherRevision = "repo-docs-matcher-v1"
	conventionalPresetRev = "conventional-docs-v1"
	PolicySourceBuiltin   = "builtin"
	PolicySourceCommitted = "committed_config"
	PolicySourceWorktree  = "worktree_config"
	PolicyStatusReady     = "ready"
	PolicyStatusDisabled  = "disabled"
	PolicyStatusInvalid   = "repository_docs_policy_invalid"
)

var ErrPolicyInvalid = errors.New("repository documentation policy is invalid")

// Policy is repository-owned corpus intent. Machine-local provider and resource
// settings deliberately do not belong here.
type Policy struct {
	Schema  int      `json:"schema"`
	Enabled bool     `json:"enabled"`
	Preset  string   `json:"preset"`
	Include []string `json:"include,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
}

type PolicyResolution struct {
	Policy       Policy `json:"policy"`
	Source       string `json:"source"`
	Status       string `json:"status"`
	ConfigDigest string `json:"config_digest,omitempty"`
	PolicyHash   string `json:"policy_hash"`
	Message      string `json:"message,omitempty"`
}

type PolicyError struct {
	Reason string
}

func (e *PolicyError) Error() string {
	if e == nil || e.Reason == "" {
		return ErrPolicyInvalid.Error()
	}
	return ErrPolicyInvalid.Error() + ": " + e.Reason
}

func (e *PolicyError) Unwrap() error { return ErrPolicyInvalid }

func (e *PolicyError) DiagnosticCode() string { return PolicyStatusInvalid }

func BuiltinPolicy() Policy {
	return Policy{Schema: PolicySchemaV1, Enabled: true, Preset: DefaultPolicyPreset}
}

// ParsePolicy parses the small, intentionally bounded repository_docs YAML
// section. Other top-level product configuration remains owned by internal/config.
// A missing section resolves to the versioned built-in preset.
func ParsePolicy(data []byte, source string) (PolicyResolution, error) {
	if source == "" {
		source = PolicySourceCommitted
	}
	section, found, err := repositoryDocsSection(data)
	if err != nil {
		return invalidPolicy(source, data, err)
	}
	if !found {
		policy := BuiltinPolicy()
		return resolvedPolicy(policy, PolicySourceBuiltin, data), nil
	}
	policy := BuiltinPolicy()
	if section.Kind != yaml.MappingNode {
		return invalidPolicy(source, data, fmt.Errorf("repository_docs must be a YAML mapping"))
	}
	seen := map[string]bool{}
	for index := 0; index < len(section.Content); index += 2 {
		keyNode, valueNode := section.Content[index], section.Content[index+1]
		if keyNode.Kind != yaml.ScalarNode || keyNode.Tag != "!!str" {
			return invalidPolicy(source, data, fmt.Errorf("repository_docs keys must be strings"))
		}
		key := keyNode.Value
		if seen[key] {
			return invalidPolicy(source, data, fmt.Errorf("duplicate key %q", key))
		}
		seen[key] = true
		switch key {
		case "schema":
			if valueNode.Kind != yaml.ScalarNode || valueNode.Tag != "!!int" || valueNode.Decode(&policy.Schema) != nil {
				return invalidPolicy(source, data, fmt.Errorf("schema must be an integer"))
			}
		case "enabled":
			if valueNode.Kind != yaml.ScalarNode || valueNode.Tag != "!!bool" || valueNode.Decode(&policy.Enabled) != nil {
				return invalidPolicy(source, data, fmt.Errorf("enabled must be true or false"))
			}
		case "preset":
			if valueNode.Kind != yaml.ScalarNode || valueNode.Tag != "!!str" {
				return invalidPolicy(source, data, fmt.Errorf("invalid preset"))
			}
			policy.Preset = valueNode.Value
		case "include", "exclude":
			values, listErr := yamlStringList(valueNode)
			if listErr != nil {
				return invalidPolicy(source, data, fmt.Errorf("%s must be a YAML list", key))
			}
			if key == "include" {
				policy.Include = values
			} else {
				policy.Exclude = values
			}
		case "":
			return invalidPolicy(source, data, fmt.Errorf("empty key"))
		default:
			return invalidPolicy(source, data, fmt.Errorf("unknown repository_docs key %q", key))
		}
	}
	if err := normalizeAndValidatePolicy(&policy); err != nil {
		return invalidPolicy(source, data, err)
	}
	return resolvedPolicy(policy, source, data), nil
}

func repositoryDocsSection(data []byte) (*yaml.Node, bool, error) {
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, false, nil
	}
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return nil, false, fmt.Errorf("invalid repository configuration YAML: %w", err)
	}
	if len(document.Content) != 1 {
		return nil, false, fmt.Errorf("repository configuration must contain one YAML document")
	}
	root := document.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, false, nil
	}
	var section *yaml.Node
	for index := 0; index < len(root.Content); index += 2 {
		key, value := root.Content[index], root.Content[index+1]
		if key.Kind == yaml.ScalarNode && key.Tag == "!!str" && key.Value == "repository_docs" {
			if section != nil {
				return nil, false, fmt.Errorf("duplicate top-level repository_docs section")
			}
			section = value
		}
	}
	return section, section != nil, nil
}

func yamlStringList(node *yaml.Node) ([]string, error) {
	if node.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("expected sequence")
	}
	values := make([]string, 0, len(node.Content))
	for _, item := range node.Content {
		if item.Kind != yaml.ScalarNode || item.Tag != "!!str" || strings.TrimSpace(item.Value) == "" {
			return nil, fmt.Errorf("list entries must be non-empty strings")
		}
		values = append(values, item.Value)
	}
	return values, nil
}

func normalizeAndValidatePolicy(policy *Policy) error {
	if policy.Schema != PolicySchemaV1 {
		return fmt.Errorf("unsupported schema %d", policy.Schema)
	}
	policy.Preset = strings.TrimSpace(policy.Preset)
	if policy.Preset == "" {
		policy.Preset = DefaultPolicyPreset
	}
	if policy.Preset != DefaultPolicyPreset && policy.Preset != "none" {
		return fmt.Errorf("unknown preset %q", policy.Preset)
	}
	var err error
	policy.Include, err = normalizePatterns(policy.Include)
	if err != nil {
		return fmt.Errorf("include: %w", err)
	}
	policy.Exclude, err = normalizePatterns(policy.Exclude)
	if err != nil {
		return fmt.Errorf("exclude: %w", err)
	}
	if policy.Enabled && policy.Preset == "none" && len(policy.Include) == 0 {
		return fmt.Errorf("preset none requires at least one include pattern")
	}
	return nil
}

func normalizePatterns(patterns []string) ([]string, error) {
	set := map[string]struct{}{}
	for _, raw := range patterns {
		value := strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
		value = strings.TrimPrefix(value, "./")
		if value == "" || strings.HasPrefix(value, "/") || value == ".." || strings.HasPrefix(value, "../") || strings.Contains(value, "/../") {
			return nil, fmt.Errorf("unsafe pattern %q", raw)
		}
		for _, segment := range strings.Split(value, "/") {
			if segment == "**" {
				continue
			}
			if _, err := path.Match(segment, "probe"); err != nil {
				return nil, fmt.Errorf("invalid glob %q: %w", raw, err)
			}
		}
		set[value] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Strings(out)
	return out, nil
}

func resolvedPolicy(policy Policy, source string, data []byte) PolicyResolution {
	status := PolicyStatusReady
	if !policy.Enabled {
		status = PolicyStatusDisabled
	}
	return PolicyResolution{
		Policy:       policy,
		Source:       source,
		Status:       status,
		ConfigDigest: digestBytes(data),
		PolicyHash:   PolicyHash(policy),
	}
}

func invalidPolicy(source string, data []byte, err error) (PolicyResolution, error) {
	policyErr := &PolicyError{Reason: err.Error()}
	return PolicyResolution{Source: source, Status: PolicyStatusInvalid, ConfigDigest: digestBytes(data), Message: policyErr.Error()}, policyErr
}

func PolicyHash(policy Policy) string {
	canonical := struct {
		Schema          int      `json:"schema"`
		Enabled         bool     `json:"enabled"`
		Preset          string   `json:"preset"`
		PresetRevision  string   `json:"preset_revision"`
		Include         []string `json:"include"`
		Exclude         []string `json:"exclude"`
		MatcherRevision string   `json:"matcher_revision"`
	}{policy.Schema, policy.Enabled, policy.Preset, conventionalPresetRev, policy.Include, policy.Exclude, policyMatcherRevision}
	data, _ := json.Marshal(canonical)
	return "policy-" + digestBytes(data)
}

func digestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (p Policy) Matches(repoPath string) bool {
	if !p.Enabled {
		return false
	}
	repoPath = strings.TrimPrefix(strings.ReplaceAll(repoPath, "\\", "/"), "./")
	if repoPath == "" || strings.HasPrefix(repoPath, ".git/") || repoPath == ".git" || strings.HasPrefix(repoPath, ".gitcode/mcp/") {
		return false
	}
	matched := false
	if p.Preset == DefaultPolicyPreset {
		matched = globMatch("README*", repoPath) || globMatch("AGENTS.md", repoPath) || globMatch("docs/**", repoPath)
	}
	for _, pattern := range p.Include {
		matched = matched || globMatch(pattern, repoPath)
	}
	if !matched {
		return false
	}
	for _, pattern := range p.Exclude {
		if globMatch(pattern, repoPath) {
			return false
		}
	}
	return true
}

func globMatch(pattern, value string) bool {
	return matchSegments(strings.Split(pattern, "/"), strings.Split(value, "/"))
}

func matchSegments(pattern, value []string) bool {
	if len(pattern) == 0 {
		return len(value) == 0
	}
	if pattern[0] == "**" {
		if matchSegments(pattern[1:], value) {
			return true
		}
		return len(value) > 0 && matchSegments(pattern, value[1:])
	}
	if len(value) == 0 {
		return false
	}
	ok, err := path.Match(pattern[0], value[0])
	return err == nil && ok && matchSegments(pattern[1:], value[1:])
}
