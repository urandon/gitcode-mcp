package feedback

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"gitcode-mcp/internal/diagnostics"
)

const (
	SinkGitCodeIssues       = "gitcode_issues"
	DuplicatePolicySuggest  = "suggest"
	DuplicatePolicyReturn   = "return_existing"
	DuplicateOverrideCreate = "create"
)

var (
	feedbackURLPattern        = regexp.MustCompile(`https?://[^\s<>"']+`)
	unixPrivatePathPattern    = regexp.MustCompile(`(^|[\s("'])/(?:Users|home|private|var|tmp|Volumes)/[^\s)"']+`)
	windowsPrivatePathPattern = regexp.MustCompile(`(?i)\b[A-Z]:\\(?:Users|Documents and Settings|Temp)\\[^\s)"']+`)
	feedbackMarkerPattern     = regexp.MustCompile(`<!--\s*gitcode-mcp-feedback:([a-f0-9]{64})\s*-->`)
)

type Config struct {
	Enabled         bool     `json:"enabled"`
	Sink            string   `json:"sink"`
	RepoID          string   `json:"repo_id"`
	Labels          []string `json:"labels,omitempty"`
	DuplicatePolicy string   `json:"duplicate_policy"`
}

func DefaultConfig() Config {
	return Config{Sink: SinkGitCodeIssues, DuplicatePolicy: DuplicatePolicySuggest}
}

type Draft struct {
	Summary           string   `json:"summary"`
	Category          string   `json:"category"`
	Surface           string   `json:"surface"`
	ReporterType      string   `json:"reporter_type"`
	Observed          string   `json:"observed"`
	Expected          string   `json:"expected"`
	Impact            string   `json:"impact"`
	ReproductionSteps []string `json:"reproduction_steps,omitempty"`
	FallbackUsed      string   `json:"fallback_used,omitempty"`
	Workaround        string   `json:"workaround,omitempty"`
	RelatedTask       string   `json:"related_task,omitempty"`
	AcceptanceSignal  string   `json:"acceptance_signal,omitempty"`
	Proposal          string   `json:"proposal,omitempty"`
	Evidence          []string `json:"evidence,omitempty"`
	ToolName          string   `json:"tool_name,omitempty"`
	ErrorCode         string   `json:"error_code,omitempty"`
	FailureClass      string   `json:"failure_class,omitempty"`
	CorrelationID     string   `json:"correlation_id,omitempty"`
	JobID             string   `json:"job_id,omitempty"`
	DuplicateOverride string   `json:"duplicate_override,omitempty"`
}

type RuntimeContext struct {
	Version            string    `json:"version,omitempty"`
	Commit             string    `json:"commit,omitempty"`
	ProviderMode       string    `json:"provider_mode,omitempty"`
	CacheSchemaVersion int       `json:"cache_schema_version,omitempty"`
	ExpectedSchema     int       `json:"expected_cache_schema,omitempty"`
	SchemaCompatible   bool      `json:"schema_compatible"`
	SinkBindingState   string    `json:"sink_binding_state,omitempty"`
	OSFamily           string    `json:"os_family,omitempty"`
	ObservedAt         time.Time `json:"observed_at"`
}

func DefaultRuntimeContext(now time.Time) RuntimeContext {
	return RuntimeContext{OSFamily: runtime.GOOS, ObservedAt: now.UTC()}
}

type ExistingIssue struct {
	ID     string `json:"id"`
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"-"`
	URL    string `json:"url,omitempty"`
	Status string `json:"status,omitempty"`
}

type Candidate struct {
	ID          string  `json:"id"`
	Number      int     `json:"number,omitempty"`
	Title       string  `json:"title"`
	URL         string  `json:"url,omitempty"`
	Score       float64 `json:"score"`
	MatchReason string  `json:"match_reason"`
}

type PreparedReport struct {
	Status            string         `json:"status"`
	Configured        bool           `json:"configured"`
	Sink              string         `json:"sink,omitempty"`
	RepoID            string         `json:"repo_id,omitempty"`
	Title             string         `json:"title"`
	Body              string         `json:"body"`
	Fingerprint       string         `json:"fingerprint"`
	DedupeDecision    string         `json:"dedupe_decision"`
	Candidates        []Candidate    `json:"candidates,omitempty"`
	Context           RuntimeContext `json:"context"`
	RedactionsApplied int            `json:"redactions_applied,omitempty"`
	Remediation       string         `json:"remediation,omitempty"`
}

type SubmissionResult struct {
	Status         string      `json:"status"`
	Sink           string      `json:"sink,omitempty"`
	RepoID         string      `json:"repo_id,omitempty"`
	TicketID       string      `json:"ticket_id,omitempty"`
	TicketNumber   int         `json:"ticket_number,omitempty"`
	TicketURL      string      `json:"ticket_url,omitempty"`
	Fingerprint    string      `json:"fingerprint"`
	DedupeDecision string      `json:"dedupe_decision"`
	Candidates     []Candidate `json:"candidates,omitempty"`
	IdempotencyKey string      `json:"idempotency_key,omitempty"`
	Replayed       bool        `json:"replayed,omitempty"`
	Evidence       string      `json:"evidence,omitempty"`
	Remediation    string      `json:"remediation,omitempty"`
	GeneratedAt    time.Time   `json:"generated_at"`
}

type ValidationError struct {
	Field   string
	Message string
}

func (e ValidationError) Error() string          { return "feedback: " + e.Field + ": " + e.Message }
func (e ValidationError) DiagnosticCode() string { return "invalid_feedback" }

func NormalizeConfig(cfg Config) (Config, error) {
	if strings.TrimSpace(cfg.Sink) == "" {
		cfg.Sink = SinkGitCodeIssues
	}
	if cfg.Sink != SinkGitCodeIssues {
		return Config{}, fmt.Errorf("feedback: unsupported sink %q", cfg.Sink)
	}
	if strings.TrimSpace(cfg.DuplicatePolicy) == "" {
		cfg.DuplicatePolicy = DuplicatePolicySuggest
	}
	if cfg.DuplicatePolicy != DuplicatePolicySuggest && cfg.DuplicatePolicy != DuplicatePolicyReturn {
		return Config{}, fmt.Errorf("feedback: duplicate_policy must be suggest or return_existing")
	}
	cfg.RepoID = strings.TrimSpace(cfg.RepoID)
	cfg.Labels = uniqueStrings(cfg.Labels)
	if cfg.Enabled && cfg.RepoID == "" {
		return Config{}, fmt.Errorf("feedback: repo_id is required when feedback is enabled")
	}
	return cfg, nil
}

func Prepare(draft Draft, context RuntimeContext, cfg Config, existing []ExistingIssue) (PreparedReport, error) {
	normalized, redactions, err := normalizeDraft(draft)
	if err != nil {
		return PreparedReport{}, err
	}
	cfg, err = NormalizeConfig(cfg)
	if err != nil {
		return PreparedReport{}, err
	}
	if context.OSFamily == "" {
		context.OSFamily = runtime.GOOS
	}
	if context.ObservedAt.IsZero() {
		context.ObservedAt = time.Now().UTC()
	} else {
		context.ObservedAt = context.ObservedAt.UTC()
	}
	fingerprint := fingerprint(normalized)
	title := renderTitle(normalized)
	body := renderBody(normalized, context, fingerprint)
	candidates, decision := findCandidates(fingerprint, normalized, existing)
	configured := cfg.Enabled && cfg.RepoID != ""
	status := "prepared"
	remediation := ""
	if !configured {
		status = "configuration_required"
		remediation = "configure feedback.enabled=true and feedback.repo_id in the trusted gitcode-mcp config"
	} else if decision == "likely_match" && normalized.DuplicateOverride != DuplicateOverrideCreate {
		status = "duplicate_candidates"
		remediation = "review candidates; pass duplicate_override=create only when this is a distinct report"
	} else if decision == "exact_match" {
		status = "duplicate"
	}
	return PreparedReport{Status: status, Configured: configured, Sink: cfg.Sink, RepoID: cfg.RepoID, Title: title, Body: body, Fingerprint: fingerprint, DedupeDecision: decision, Candidates: candidates, Context: context, RedactionsApplied: redactions, Remediation: remediation}, nil
}

func FingerprintMarker(value string) string {
	return "<!-- gitcode-mcp-feedback:" + strings.TrimSpace(value) + " -->"
}

func ExistingFingerprint(body string) string {
	match := feedbackMarkerPattern.FindStringSubmatch(body)
	if len(match) != 2 {
		return ""
	}
	return match[1]
}

func normalizeDraft(draft Draft) (Draft, int, error) {
	allowedCategories := map[string]bool{"bug": true, "feature_gap": true, "ux_friction": true, "diagnostics": true, "docs": true, "performance": true, "other": true}
	allowedSurfaces := map[string]bool{"mcp": true, "cli": true, "daemon": true, "cache": true, "sync": true, "rag": true, "gitcode_adapter": true, "setup": true, "other": true}
	allowedReporters := map[string]bool{"agent": true, "human": true, "mixed": true}
	draft.Category = strings.ToLower(strings.TrimSpace(draft.Category))
	draft.Surface = strings.ToLower(strings.TrimSpace(draft.Surface))
	draft.ReporterType = strings.ToLower(strings.TrimSpace(draft.ReporterType))
	if !allowedCategories[draft.Category] {
		return Draft{}, 0, ValidationError{Field: "category", Message: "must be bug, feature_gap, ux_friction, diagnostics, docs, performance, or other"}
	}
	if !allowedSurfaces[draft.Surface] {
		return Draft{}, 0, ValidationError{Field: "surface", Message: "must be mcp, cli, daemon, cache, sync, rag, gitcode_adapter, setup, or other"}
	}
	if !allowedReporters[draft.ReporterType] {
		return Draft{}, 0, ValidationError{Field: "reporter_type", Message: "must be agent, human, or mixed"}
	}
	if draft.DuplicateOverride != "" && draft.DuplicateOverride != DuplicateOverrideCreate {
		return Draft{}, 0, ValidationError{Field: "duplicate_override", Message: "must be empty or create"}
	}
	redactions := 0
	fields := []struct {
		name     string
		value    *string
		required bool
		max      int
	}{
		{"summary", &draft.Summary, true, 180},
		{"observed", &draft.Observed, true, 4000},
		{"expected", &draft.Expected, true, 4000},
		{"impact", &draft.Impact, true, 4000},
		{"fallback_used", &draft.FallbackUsed, false, 1000},
		{"workaround", &draft.Workaround, false, 2000},
		{"related_task", &draft.RelatedTask, false, 1000},
		{"acceptance_signal", &draft.AcceptanceSignal, false, 2000},
		{"proposal", &draft.Proposal, false, 3000},
		{"tool_name", &draft.ToolName, false, 200},
		{"error_code", &draft.ErrorCode, false, 200},
		{"failure_class", &draft.FailureClass, false, 200},
		{"correlation_id", &draft.CorrelationID, false, 200},
		{"job_id", &draft.JobID, false, 200},
	}
	for _, field := range fields {
		clean, count, err := sanitizeText(field.name, *field.value, field.max, false)
		if err != nil {
			return Draft{}, 0, err
		}
		*field.value = clean
		redactions += count
		if field.required && clean == "" {
			return Draft{}, 0, ValidationError{Field: field.name, Message: "is required"}
		}
	}
	for i, step := range draft.ReproductionSteps {
		clean, count, err := sanitizeText("reproduction_steps", step, 2000, false)
		if err != nil {
			return Draft{}, 0, err
		}
		if clean != "" {
			draft.ReproductionSteps[i] = clean
		}
		redactions += count
	}
	draft.ReproductionSteps = compactStrings(draft.ReproductionSteps)
	if len(draft.ReproductionSteps) > 20 {
		return Draft{}, 0, ValidationError{Field: "reproduction_steps", Message: "must contain at most 20 steps"}
	}
	for i, evidence := range draft.Evidence {
		clean, count, err := sanitizeText("evidence", evidence, 2000, true)
		if err != nil {
			return Draft{}, 0, err
		}
		draft.Evidence[i] = clean
		redactions += count
	}
	draft.Evidence = compactStrings(draft.Evidence)
	if len(draft.Evidence) > 20 {
		return Draft{}, 0, ValidationError{Field: "evidence", Message: "must contain at most 20 bounded facts"}
	}
	return draft, redactions, nil
}

func sanitizeText(field, value string, max int, strictEvidence bool) (string, int, error) {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\x00", ""))
	if strictEvidence {
		lower := strings.ToLower(value)
		for _, forbidden := range []string{"raw prompt", "full prompt", "conversation transcript", "full transcript", "raw api response", "api response body", "environment dump", "begin private key"} {
			if strings.Contains(lower, forbidden) {
				return "", 0, ValidationError{Field: field, Message: "contains forbidden raw prompt, transcript, payload, environment, or private-key material"}
			}
		}
	}
	before := value
	value = diagnostics.RedactText(value)
	value = feedbackURLPattern.ReplaceAllStringFunc(value, sanitizeURL)
	value = unixPrivatePathPattern.ReplaceAllString(value, "${1}[REDACTED_PATH]")
	value = windowsPrivatePathPattern.ReplaceAllString(value, "[REDACTED_PATH]")
	if len(value) > max {
		return "", 0, ValidationError{Field: field, Message: fmt.Sprintf("exceeds %d bytes", max)}
	}
	count := 0
	if value != before {
		count = 1
	}
	return value, count, nil
}

func sanitizeURL(raw string) string {
	trimmed := strings.TrimRight(raw, ".,;:!?)")
	suffix := strings.TrimPrefix(raw, trimmed)
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Host == "" {
		return diagnostics.RedactText(raw)
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String() + suffix
}

func fingerprint(draft Draft) string {
	parts := []string{draft.Category, draft.Surface, normalizeFingerprintText(draft.Summary), strings.ToLower(draft.ErrorCode), strings.ToLower(draft.FailureClass), strings.ToLower(draft.ToolName)}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

func renderTitle(draft Draft) string {
	prefix := "[Feedback/" + draft.Category + "][" + draft.Surface + "] "
	title := prefix + strings.TrimSpace(draft.Summary)
	if len(title) > 180 {
		title = strings.TrimSpace(title[:177]) + "..."
	}
	return title
}

func renderBody(draft Draft, context RuntimeContext, fingerprint string) string {
	var b strings.Builder
	b.WriteString(FingerprintMarker(fingerprint))
	b.WriteString("\n\n## Feedback summary\n\n")
	b.WriteString(draft.Summary)
	b.WriteString("\n\n## Classification\n\n")
	fmt.Fprintf(&b, "- Category: `%s`\n- Surface: `%s`\n- Reporter: `%s`\n", draft.Category, draft.Surface, draft.ReporterType)
	if draft.ToolName != "" {
		fmt.Fprintf(&b, "- Tool/command: `%s`\n", draft.ToolName)
	}
	if draft.ErrorCode != "" {
		fmt.Fprintf(&b, "- Error code: `%s`\n", draft.ErrorCode)
	}
	if draft.FailureClass != "" {
		fmt.Fprintf(&b, "- Failure class: `%s`\n", draft.FailureClass)
	}
	b.WriteString("\n## Observed behavior\n\n" + draft.Observed)
	b.WriteString("\n\n## Expected behavior\n\n" + draft.Expected)
	b.WriteString("\n\n## Impact\n\n" + draft.Impact)
	b.WriteString("\n\n## Reproduction\n\n")
	if len(draft.ReproductionSteps) == 0 {
		b.WriteString("Not provided.")
	} else {
		for i, step := range draft.ReproductionSteps {
			fmt.Fprintf(&b, "%d. %s\n", i+1, step)
		}
	}
	b.WriteString("\n## Workaround or fallback\n\n")
	if draft.FallbackUsed == "" && draft.Workaround == "" {
		b.WriteString("None reported.")
	} else {
		if draft.FallbackUsed != "" {
			b.WriteString("Fallback used: " + draft.FallbackUsed + "\n")
		}
		if draft.Workaround != "" {
			b.WriteString("\nWorkaround: " + draft.Workaround)
		}
	}
	b.WriteString("\n\n## Sanitized runtime evidence\n\n")
	fmt.Fprintf(&b, "- gitcode-mcp version: `%s`\n- commit: `%s`\n- provider mode: `%s`\n- cache schema: `%d/%d` (compatible: `%t`)\n- sink binding: `%s`\n- OS family: `%s`\n- observed at: `%s`\n", valueOrUnknown(context.Version), valueOrUnknown(context.Commit), valueOrUnknown(context.ProviderMode), context.CacheSchemaVersion, context.ExpectedSchema, context.SchemaCompatible, valueOrUnknown(context.SinkBindingState), valueOrUnknown(context.OSFamily), context.ObservedAt.UTC().Format(time.RFC3339))
	if draft.CorrelationID != "" {
		fmt.Fprintf(&b, "- correlation id: `%s`\n", draft.CorrelationID)
	}
	if draft.JobID != "" {
		fmt.Fprintf(&b, "- job id: `%s`\n", draft.JobID)
	}
	for _, evidence := range draft.Evidence {
		b.WriteString("- " + evidence + "\n")
	}
	b.WriteString("\n## Related work\n\n")
	if draft.RelatedTask == "" {
		b.WriteString("None provided.")
	} else {
		b.WriteString(draft.RelatedTask)
	}
	b.WriteString("\n\n## Acceptance signal\n\n")
	if draft.AcceptanceSignal == "" {
		b.WriteString("The reported workflow completes without the observed friction or fallback.")
	} else {
		b.WriteString(draft.AcceptanceSignal)
	}
	if draft.Proposal != "" {
		b.WriteString("\n\n## Proposal\n\n" + draft.Proposal)
	}
	b.WriteString("\n\n---\nGenerated from structured, sanitized feedback. Do not edit this issue description after intake; add later evidence and design decisions as comments.\n")
	return b.String()
}

func findCandidates(fingerprint string, draft Draft, existing []ExistingIssue) ([]Candidate, string) {
	var exact []Candidate
	var likely []Candidate
	wanted := tokenSet(draft.Summary)
	for _, issue := range existing {
		if issue.Status != "" && issue.Status != "open" {
			continue
		}
		marker := ExistingFingerprint(issue.Body)
		if marker == fingerprint {
			exact = append(exact, Candidate{ID: issue.ID, Number: issue.Number, Title: issue.Title, URL: issue.URL, Score: 1, MatchReason: "exact_fingerprint"})
			continue
		}
		score := overlapScore(wanted, tokenSet(issue.Title))
		if score >= 0.5 {
			likely = append(likely, Candidate{ID: issue.ID, Number: issue.Number, Title: issue.Title, URL: issue.URL, Score: score, MatchReason: "token_similarity"})
		}
	}
	if len(exact) > 0 {
		sortCandidates(exact)
		return exact, "exact_match"
	}
	if len(likely) > 0 {
		sortCandidates(likely)
		return likely, "likely_match"
	}
	return nil, "none"
}

func sortCandidates(values []Candidate) {
	sort.SliceStable(values, func(i, j int) bool {
		if values[i].Score != values[j].Score {
			return values[i].Score > values[j].Score
		}
		return values[i].Number < values[j].Number
	})
}

func normalizeFingerprintText(value string) string {
	return strings.Join(strings.Fields(strings.ToLower(value)), " ")
}
func valueOrUnknown(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unknown"
	}
	return value
}

func tokenSet(value string) map[string]struct{} {
	set := map[string]struct{}{}
	for _, token := range strings.FieldsFunc(strings.ToLower(value), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' }) {
		if len([]rune(token)) >= 3 {
			set[token] = struct{}{}
		}
	}
	return set
}

func overlapScore(a, b map[string]struct{}) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	common := 0
	for token := range a {
		if _, ok := b[token]; ok {
			common++
		}
	}
	union := len(a) + len(b) - common
	return float64(common) / float64(union)
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}

func compactStrings(values []string) []string {
	out := values[:0]
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			out = append(out, strings.TrimSpace(value))
		}
	}
	return out
}

func ParseIssueNumber(id string) int {
	for _, prefix := range []string{"ISSUE-", "issue:"} {
		if strings.HasPrefix(id, prefix) {
			n, _ := strconv.Atoi(strings.TrimPrefix(id, prefix))
			return n
		}
	}
	return 0
}

func IsValidationError(err error) bool {
	var target ValidationError
	return errors.As(err, &target)
}
