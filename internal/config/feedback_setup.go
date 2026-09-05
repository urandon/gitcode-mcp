package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"gitcode-mcp/internal/feedback"
	"gopkg.in/yaml.v3"
)

const feedbackSetupSchema = "gitcode-mcp.feedback-setup.v1"

type FeedbackSetupEffect struct {
	ID                   string `json:"id"`
	Class                string `json:"class"`
	Summary              string `json:"summary"`
	ConfirmationRequired bool   `json:"confirmation_required"`
}

type FeedbackSetupPlan struct {
	Status               string                `json:"status"`
	PlanID               string                `json:"plan_id"`
	RepoID               string                `json:"repo_id"`
	Sink                 string                `json:"sink"`
	Labels               []string              `json:"labels"`
	DuplicatePolicy      string                `json:"duplicate_policy"`
	Effects              []FeedbackSetupEffect `json:"effects"`
	ConfirmationRequired bool                  `json:"confirmation_required"`
	path                 string
	beforeDigest         string
	next                 []byte
}

type FeedbackSetupResult struct {
	Status          string    `json:"status"`
	PlanID          string    `json:"plan_id"`
	RepoID          string    `json:"repo_id"`
	Sink            string    `json:"sink"`
	Labels          []string  `json:"labels"`
	DuplicatePolicy string    `json:"duplicate_policy"`
	IdempotencyKey  string    `json:"idempotency_key"`
	Evidence        string    `json:"evidence"`
	GeneratedAt     time.Time `json:"generated_at"`
}

func PlanFeedbackSetup(src Source, repoID string) (FeedbackSetupPlan, error) {
	if src == nil {
		src = OSSource{}
	}
	repoID = strings.TrimSpace(repoID)
	parts := strings.Split(repoID, "/")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" || strings.ContainsAny(repoID, "\r\n\t ") {
		return FeedbackSetupPlan{}, fmt.Errorf("feedback setup: repo must be an exact owner/repository id")
	}
	location := Locate(src)
	if location.Format != "yaml" || strings.TrimSpace(location.Path) == "" {
		return FeedbackSetupPlan{}, fmt.Errorf("feedback setup: trusted global YAML configuration is unavailable")
	}
	current, err := readFeedbackSetupConfig(src, location)
	if err != nil {
		return FeedbackSetupPlan{}, err
	}
	next, already, labels, policy, err := renderFeedbackSetupYAML(current, repoID)
	if err != nil {
		return FeedbackSetupPlan{}, err
	}
	beforeDigest := digestBytes(current)
	intent := strings.Join([]string{feedbackSetupSchema, beforeDigest, repoID, feedback.SinkGitCodeIssues, strings.Join(labels, "|"), policy}, "\x00")
	planID := "feedback-plan-" + digestBytes([]byte(intent))[:24]
	status := "confirmation_required"
	effects := []FeedbackSetupEffect{{ID: "configure-feedback-sink", Class: "trusted_local_config_write", Summary: "enable the configured GitCode issue feedback sink", ConfirmationRequired: true}}
	confirmation := true
	if already {
		status = "already_configured"
		confirmation = false
		effects[0].ConfirmationRequired = false
	}
	return FeedbackSetupPlan{
		Status: status, PlanID: planID, RepoID: repoID, Sink: feedback.SinkGitCodeIssues,
		Labels: labels, DuplicatePolicy: policy, Effects: effects, ConfirmationRequired: confirmation,
		path: location.Path, beforeDigest: beforeDigest, next: next,
	}, nil
}

func ApplyFeedbackSetup(plan FeedbackSetupPlan, idempotencyKey string, now time.Time) (FeedbackSetupResult, error) {
	key := strings.TrimSpace(idempotencyKey)
	if key == "" {
		return FeedbackSetupResult{}, fmt.Errorf("feedback setup: idempotency key is required")
	}
	base := FeedbackSetupResult{PlanID: plan.PlanID, RepoID: plan.RepoID, Sink: plan.Sink, Labels: append([]string(nil), plan.Labels...), DuplicatePolicy: plan.DuplicatePolicy, IdempotencyKey: key, GeneratedAt: now.UTC()}
	if plan.Status == "already_configured" {
		base.Status = "already_configured"
		base.Evidence = "trusted feedback configuration already matched the rendered plan; no write performed"
		return base, nil
	}
	if plan.Status != "confirmation_required" || plan.path == "" || len(plan.next) == 0 {
		return FeedbackSetupResult{}, fmt.Errorf("feedback setup: invalid or incomplete plan")
	}
	release, err := acquireFeedbackSetupLock(plan.path)
	if err != nil {
		return FeedbackSetupResult{}, err
	}
	defer release()
	current, err := os.ReadFile(plan.path)
	if errors.Is(err, os.ErrNotExist) {
		current = nil
		err = nil
	}
	if err != nil {
		return FeedbackSetupResult{}, fmt.Errorf("feedback setup: trusted configuration cannot be read")
	}
	if digestBytes(current) != plan.beforeDigest {
		return FeedbackSetupResult{}, fmt.Errorf("feedback setup: configuration changed after planning; render a new plan")
	}
	if err := durableWriteFeedbackConfig(plan.path, plan.next); err != nil {
		return FeedbackSetupResult{}, fmt.Errorf("feedback setup: trusted configuration update failed")
	}
	base.Status = "configured"
	base.Evidence = "atomically applied trusted feedback configuration; no credential was written"
	return base, nil
}

func readFeedbackSetupConfig(src Source, location ConfigLocation) ([]byte, error) {
	data, err := src.ReadFile(location.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("feedback setup: trusted configuration cannot be read")
	}
	return data, nil
}

func renderFeedbackSetupYAML(data []byte, repoID string) ([]byte, bool, []string, string, error) {
	var document yaml.Node
	if len(bytes.TrimSpace(data)) > 0 {
		if err := yaml.Unmarshal(data, &document); err != nil {
			return nil, false, nil, "", fmt.Errorf("feedback setup: trusted YAML configuration is invalid")
		}
	}
	root, err := ensureYAMLMapping(&document)
	if err != nil {
		return nil, false, nil, "", err
	}
	section, err := ensureYAMLMapValue(root, "feedback")
	if err != nil {
		return nil, false, nil, "", err
	}
	labels := yamlScalar(section, "labels")
	if strings.TrimSpace(labels) == "" {
		labels = "feedback|dogfood"
	}
	policy := yamlScalar(section, "duplicate_policy")
	if policy != feedback.DuplicatePolicyReturn {
		policy = feedback.DuplicatePolicySuggest
	}
	already := yamlScalar(section, "enabled") == "true" && yamlScalar(section, "sink") == feedback.SinkGitCodeIssues && yamlScalar(section, "repo_id") == repoID && yamlScalar(section, "labels") == labels && yamlScalar(section, "duplicate_policy") == policy
	setYAMLScalar(section, "enabled", "true", "!!bool")
	setYAMLScalar(section, "sink", feedback.SinkGitCodeIssues, "!!str")
	setYAMLScalar(section, "repo_id", repoID, "!!str")
	setYAMLScalar(section, "labels", labels, "!!str")
	setYAMLScalar(section, "duplicate_policy", policy, "!!str")
	var out bytes.Buffer
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	if err := encoder.Encode(&document); err != nil {
		return nil, false, nil, "", fmt.Errorf("feedback setup: trusted YAML configuration cannot be rendered")
	}
	_ = encoder.Close()
	return out.Bytes(), already, splitSetupLabels(labels), policy, nil
}

func ensureYAMLMapping(document *yaml.Node) (*yaml.Node, error) {
	if document.Kind == 0 {
		document.Kind = yaml.DocumentNode
	}
	if len(document.Content) == 0 {
		document.Content = []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}
	}
	root := document.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("feedback setup: trusted YAML root must be a mapping")
	}
	return root, nil
}

func ensureYAMLMapValue(mapping *yaml.Node, key string) (*yaml.Node, error) {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			value := mapping.Content[i+1]
			if value.Kind != yaml.MappingNode {
				return nil, fmt.Errorf("feedback setup: trusted YAML %s section must be a mapping", key)
			}
			return value, nil
		}
	}
	mapping.Content = append(mapping.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"})
	return mapping.Content[len(mapping.Content)-1], nil
}

func yamlScalar(mapping *yaml.Node, key string) string {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return strings.TrimSpace(mapping.Content[i+1].Value)
		}
	}
	return ""
}

func setYAMLScalar(mapping *yaml.Node, key, value, tag string) {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			mapping.Content[i+1].Kind = yaml.ScalarNode
			mapping.Content[i+1].Tag = tag
			mapping.Content[i+1].Value = value
			return
		}
	}
	mapping.Content = append(mapping.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value})
}

func splitSetupLabels(value string) []string {
	var result []string
	for _, item := range strings.Split(value, "|") {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
}

func digestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func durableWriteFeedbackConfig(path string, data []byte) (err error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".feedback-config-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		if err != nil {
			_ = os.Remove(tmpPath)
		}
	}()
	if err = tmp.Chmod(0o600); err != nil {
		return err
	}
	if _, err = tmp.Write(data); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmpPath, path); err != nil {
		return err
	}
	directory, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer directory.Close()
	if err = directory.Sync(); err != nil && !feedbackDirectorySyncUnsupported(err) {
		return err
	}
	return nil
}

func feedbackDirectorySyncUnsupported(err error) bool {
	if runtime.GOOS == "windows" {
		return true
	}
	return errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.EBADF)
}
