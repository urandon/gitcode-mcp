package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"gitcode-mcp/internal/feedback"
	"gopkg.in/yaml.v3"
)

const (
	feedbackSetupSchema       = "gitcode-mcp.feedback-setup.v1"
	feedbackSetupJournalLimit = 256
	feedbackSetupReceiptTTL   = 90 * 24 * time.Hour
)

// ErrFeedbackSetupStalePlan identifies a reviewed setup plan whose trusted
// configuration snapshot changed before apply. Transports use this sentinel
// to return a recoverable stale-plan response instead of treating the race as
// malformed caller input.
var ErrFeedbackSetupStalePlan = errors.New("feedback setup: stale plan")

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
	afterDigest          string
	intentDigest         string
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
	Replayed        bool      `json:"replayed"`
}

type feedbackSetupJournal struct {
	Schema string                        `json:"schema"`
	Claims map[string]feedbackSetupClaim `json:"claims"`
}

type feedbackSetupClaim struct {
	PlanID       string    `json:"plan_id"`
	IntentDigest string    `json:"intent_digest"`
	BeforeDigest string    `json:"before_digest"`
	AfterDigest  string    `json:"after_digest"`
	Status       string    `json:"status"`
	ResultStatus string    `json:"result_status"`
	GeneratedAt  time.Time `json:"generated_at"`
}

func PlanFeedbackSetup(src Source, repoID string) (FeedbackSetupPlan, error) {
	if src == nil {
		src = OSSource{}
	}
	repoID = strings.TrimSpace(repoID)
	if !feedback.ValidRepositoryID(repoID) {
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
	afterDigest := digestBytes(next)
	desiredIntent := strings.Join([]string{feedbackSetupSchema, repoID, feedback.SinkGitCodeIssues, strings.Join(labels, "|"), policy}, "\x00")
	intentDigest := digestBytes([]byte(desiredIntent))
	planID := "feedback-plan-" + digestBytes([]byte(strings.Join([]string{beforeDigest, intentDigest}, "\x00")))[:24]
	status := "confirmation_required"
	effects := []FeedbackSetupEffect{{ID: "configure-feedback-sink", Class: "trusted_local_config_write", Summary: "enable the configured GitCode issue feedback sink", ConfirmationRequired: true}}
	confirmation := true
	if already {
		status = "already_configured"
		confirmation = false
		effects[0].ConfirmationRequired = false
		afterDigest = beforeDigest
	}
	return FeedbackSetupPlan{
		Status: status, PlanID: planID, RepoID: repoID, Sink: feedback.SinkGitCodeIssues,
		Labels: labels, DuplicatePolicy: policy, Effects: effects, ConfirmationRequired: confirmation,
		path: location.Path, beforeDigest: beforeDigest, afterDigest: afterDigest, intentDigest: intentDigest, next: next,
	}, nil
}

func ApplyFeedbackSetup(plan FeedbackSetupPlan, idempotencyKey string, now time.Time) (FeedbackSetupResult, error) {
	return ApplyFeedbackSetupWithExpectedPlan(plan, plan.PlanID, idempotencyKey, now)
}

// ApplyFeedbackSetupWithExpectedPlan permits an interrupted caller to present
// the exact id of a retained succeeded claim even after the current config has
// advanced to the already-configured plan. A stale id with no matching receipt
// is rejected before any config or journal mutation for the new intent.
func ApplyFeedbackSetupWithExpectedPlan(plan FeedbackSetupPlan, expectedPlanID, idempotencyKey string, now time.Time) (FeedbackSetupResult, error) {
	key := strings.TrimSpace(idempotencyKey)
	if key == "" {
		return FeedbackSetupResult{}, fmt.Errorf("feedback setup: idempotency key is required")
	}
	base := FeedbackSetupResult{PlanID: plan.PlanID, RepoID: plan.RepoID, Sink: plan.Sink, Labels: append([]string(nil), plan.Labels...), DuplicatePolicy: plan.DuplicatePolicy, IdempotencyKey: key, GeneratedAt: now.UTC()}
	if (plan.Status != "confirmation_required" && plan.Status != "already_configured") || plan.path == "" || len(plan.next) == 0 || plan.intentDigest == "" || plan.afterDigest == "" {
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
	currentDigest := digestBytes(current)
	journalPath := plan.path + ".feedback-setup-receipts.json"
	journal, err := readFeedbackSetupJournal(journalPath)
	if err != nil {
		return FeedbackSetupResult{}, err
	}
	journalChanged, err := reconcileFeedbackSetupJournal(&journal, currentDigest)
	if err != nil {
		return FeedbackSetupResult{}, err
	}
	journalChanged = pruneExpiredFeedbackSetupClaims(&journal, now.UTC()) || journalChanged
	keyDigest := digestBytes([]byte(key))
	if claim, exists := journal.Claims[keyDigest]; exists {
		if claim.IntentDigest != plan.intentDigest {
			return FeedbackSetupResult{}, fmt.Errorf("feedback setup: idempotency key is already claimed for a different setup intent")
		}
		if claim.Status == "succeeded" {
			if strings.TrimSpace(expectedPlanID) != plan.PlanID && strings.TrimSpace(expectedPlanID) != claim.PlanID {
				return FeedbackSetupResult{}, fmt.Errorf("%w: confirmed plan id no longer matches current state or the retained receipt", ErrFeedbackSetupStalePlan)
			}
			base.PlanID = claim.PlanID
			base.Status = claim.ResultStatus
			base.GeneratedAt = claim.GeneratedAt
			base.Replayed = true
			base.Evidence = "durable idempotency receipt matched the setup intent; no write performed"
			if journalChanged {
				if err := writeFeedbackSetupJournal(journalPath, journal); err != nil {
					return FeedbackSetupResult{}, err
				}
			}
			return base, nil
		}
	}
	if strings.TrimSpace(expectedPlanID) == "" || strings.TrimSpace(expectedPlanID) != plan.PlanID {
		return FeedbackSetupResult{}, fmt.Errorf("%w: confirmed plan id no longer matches current state or a retained receipt", ErrFeedbackSetupStalePlan)
	}
	if currentDigest != plan.beforeDigest {
		return FeedbackSetupResult{}, fmt.Errorf("%w: configuration changed after planning; render a new plan", ErrFeedbackSetupStalePlan)
	}
	if _, exists := journal.Claims[keyDigest]; !exists {
		if _, err := reserveFeedbackSetupJournalSlot(&journal); err != nil {
			return FeedbackSetupResult{}, err
		}
	}
	resultStatus := "configured"
	if plan.Status == "already_configured" {
		resultStatus = "already_configured"
	}
	claim := feedbackSetupClaim{PlanID: plan.PlanID, IntentDigest: plan.intentDigest, BeforeDigest: plan.beforeDigest, AfterDigest: plan.afterDigest, Status: "pending", ResultStatus: resultStatus, GeneratedAt: now.UTC()}
	journal.Claims[keyDigest] = claim
	if err := writeFeedbackSetupJournal(journalPath, journal); err != nil {
		return FeedbackSetupResult{}, err
	}
	if plan.Status == "confirmation_required" {
		if err := durableWriteFeedbackConfig(plan.path, plan.next); err != nil {
			return FeedbackSetupResult{}, fmt.Errorf("feedback setup: trusted configuration update failed")
		}
	}
	claim.Status = "succeeded"
	journal.Claims[keyDigest] = claim
	if err := writeFeedbackSetupJournal(journalPath, journal); err != nil {
		return FeedbackSetupResult{}, fmt.Errorf("feedback setup: configuration state is ambiguous; retry with the same idempotency key")
	}
	base.Status = resultStatus
	if resultStatus == "already_configured" {
		base.Evidence = "trusted feedback configuration already matched the rendered plan; durable no-op receipt recorded"
		return base, nil
	}
	base.Evidence = "atomically applied trusted feedback configuration with a durable idempotency receipt; no credential was written"
	return base, nil
}

func readFeedbackSetupJournal(path string) (feedbackSetupJournal, error) {
	journal := feedbackSetupJournal{Schema: feedbackSetupSchema, Claims: map[string]feedbackSetupClaim{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return journal, nil
	}
	if err != nil {
		return feedbackSetupJournal{}, fmt.Errorf("feedback setup: idempotency journal cannot be read")
	}
	if err := json.Unmarshal(data, &journal); err != nil || journal.Schema != feedbackSetupSchema || journal.Claims == nil {
		return feedbackSetupJournal{}, fmt.Errorf("feedback setup: idempotency journal is invalid")
	}
	if len(journal.Claims) > feedbackSetupJournalLimit {
		return feedbackSetupJournal{}, fmt.Errorf("feedback setup: idempotency journal exceeds its bounded capacity")
	}
	for key, claim := range journal.Claims {
		if !isFeedbackSetupPlanID(claim.PlanID) || !isFeedbackSetupDigest(key) || !isFeedbackSetupDigest(claim.IntentDigest) || !isFeedbackSetupDigest(claim.BeforeDigest) || !isFeedbackSetupDigest(claim.AfterDigest) || (claim.Status != "pending" && claim.Status != "succeeded" && claim.Status != "abandoned") || (claim.ResultStatus != "configured" && claim.ResultStatus != "already_configured") || claim.GeneratedAt.IsZero() {
			return feedbackSetupJournal{}, fmt.Errorf("feedback setup: idempotency journal is invalid")
		}
	}
	return journal, nil
}

func isFeedbackSetupPlanID(value string) bool {
	const prefix = "feedback-plan-"
	return strings.HasPrefix(value, prefix) && len(value) == len(prefix)+24 && isFeedbackSetupHex(value[len(prefix):])
}

func isFeedbackSetupDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	return isFeedbackSetupHex(value)
}

func isFeedbackSetupHex(value string) bool {
	_, err := hex.DecodeString(value)
	return err == nil
}

func writeFeedbackSetupJournal(path string, journal feedbackSetupJournal) error {
	data, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		return fmt.Errorf("feedback setup: idempotency journal cannot be rendered")
	}
	data = append(data, '\n')
	if err := durableWriteFeedbackConfig(path, data); err != nil {
		return fmt.Errorf("feedback setup: idempotency journal update failed")
	}
	return nil
}

func reconcileFeedbackSetupJournal(journal *feedbackSetupJournal, currentDigest string) (bool, error) {
	changed := false
	for key, claim := range journal.Claims {
		if claim.Status != "pending" {
			continue
		}
		switch currentDigest {
		case claim.AfterDigest:
			claim.Status = "succeeded"
		case claim.BeforeDigest:
			claim.Status = "abandoned"
		default:
			return false, fmt.Errorf("feedback setup: prior configuration state is ambiguous; retry the original operation or inspect local diagnostics")
		}
		journal.Claims[key] = claim
		changed = true
	}
	return changed, nil
}

func pruneExpiredFeedbackSetupClaims(journal *feedbackSetupJournal, now time.Time) bool {
	cutoff := now.Add(-feedbackSetupReceiptTTL)
	changed := false
	for key, claim := range journal.Claims {
		if claim.Status == "pending" || !claim.GeneratedAt.Before(cutoff) {
			continue
		}
		delete(journal.Claims, key)
		changed = true
	}
	return changed
}

func reserveFeedbackSetupJournalSlot(journal *feedbackSetupJournal) (bool, error) {
	if len(journal.Claims) < feedbackSetupJournalLimit {
		return false, nil
	}
	type terminalClaim struct {
		key         string
		generatedAt time.Time
	}
	candidates := make([]terminalClaim, 0, len(journal.Claims))
	for key, claim := range journal.Claims {
		if claim.Status == "pending" {
			continue
		}
		candidates = append(candidates, terminalClaim{key: key, generatedAt: claim.GeneratedAt})
	}
	if len(candidates) == 0 {
		return false, fmt.Errorf("feedback setup: idempotency journal has no safely compactable terminal receipt")
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].generatedAt.Equal(candidates[j].generatedAt) {
			return candidates[i].key < candidates[j].key
		}
		return candidates[i].generatedAt.Before(candidates[j].generatedAt)
	})
	delete(journal.Claims, candidates[0].key)
	return true, nil
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
