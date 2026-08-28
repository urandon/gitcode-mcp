package repositorydocs

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
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
	seen := map[string]bool{}
	var listKey string
	for _, line := range section {
		trimmed := strings.TrimSpace(line.text)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "-") {
			if listKey == "" {
				return invalidPolicy(source, data, fmt.Errorf("list item without include or exclude key"))
			}
			value, err := yamlScalar(strings.TrimSpace(strings.TrimPrefix(trimmed, "-")))
			if err != nil || value == "" {
				return invalidPolicy(source, data, fmt.Errorf("invalid %s entry", listKey))
			}
			if listKey == "include" {
				policy.Include = append(policy.Include, value)
			} else {
				policy.Exclude = append(policy.Exclude, value)
			}
			continue
		}
		key, raw, ok := strings.Cut(trimmed, ":")
		if !ok {
			return invalidPolicy(source, data, fmt.Errorf("expected key: value"))
		}
		key = strings.TrimSpace(key)
		raw = strings.TrimSpace(raw)
		if seen[key] {
			return invalidPolicy(source, data, fmt.Errorf("duplicate key %q", key))
		}
		seen[key] = true
		listKey = ""
		switch key {
		case "schema":
			value, err := strconv.Atoi(raw)
			if err != nil {
				return invalidPolicy(source, data, fmt.Errorf("schema must be an integer"))
			}
			policy.Schema = value
		case "enabled":
			value, err := strconv.ParseBool(strings.ToLower(raw))
			if err != nil {
				return invalidPolicy(source, data, fmt.Errorf("enabled must be true or false"))
			}
			policy.Enabled = value
		case "preset":
			value, err := yamlScalar(raw)
			if err != nil {
				return invalidPolicy(source, data, fmt.Errorf("invalid preset"))
			}
			policy.Preset = value
		case "include", "exclude":
			if raw != "" && raw != "[]" {
				return invalidPolicy(source, data, fmt.Errorf("%s must be a YAML list", key))
			}
			listKey = key
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

type yamlLine struct {
	indent int
	text   string
}

func repositoryDocsSection(data []byte) ([]yamlLine, bool, error) {
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	var section []yamlLine
	found := false
	sectionIndent := -1
	for scanner.Scan() {
		raw := strings.TrimRight(scanner.Text(), " \t\r")
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			if found {
				section = append(section, yamlLine{indent: sectionIndent + 2, text: trimmed})
			}
			continue
		}
		if strings.Contains(raw[:len(raw)-len(strings.TrimLeft(raw, " \t"))], "\t") {
			return nil, false, fmt.Errorf("tabs are not supported in repository_docs indentation")
		}
		indent := len(raw) - len(strings.TrimLeft(raw, " "))
		if !found {
			if trimmed == "repository_docs:" {
				found = true
				sectionIndent = indent
			}
			continue
		}
		if indent <= sectionIndent {
			break
		}
		section = append(section, yamlLine{indent: indent, text: raw})
	}
	if err := scanner.Err(); err != nil {
		return nil, false, err
	}
	return section, found, nil
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

func yamlScalar(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if (strings.HasPrefix(raw, `"`) && strings.HasSuffix(raw, `"`)) || (strings.HasPrefix(raw, `'`) && strings.HasSuffix(raw, `'`)) {
		if raw[0] == '\'' {
			return strings.ReplaceAll(raw[1:len(raw)-1], "''", "'"), nil
		}
		return strconv.Unquote(raw)
	}
	if strings.ContainsAny(raw, "#{}[]") {
		return "", fmt.Errorf("quote scalar values containing YAML control characters")
	}
	return raw, nil
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
