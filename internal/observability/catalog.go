// Package observability owns bounded, content-free diagnostic evidence. It is
// not an execution authority and never accepts an error or a free-form message.
package observability

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"time"
)

const (
	MaxEvents     = 1024
	MaxQueueBytes = 4 << 20
	MaxEventBytes = 8 << 10
	MaxPageEvents = 200
	MaxPageBytes  = 64 << 10
	SegmentBytes  = 1 << 20
	LedgerBytes   = 4 * SegmentBytes
	StreamBytes   = 4 * SegmentBytes
	MetadataBytes = 8 << 10 // two fixed 4 KiB metadata slots; lock is empty
)

type Code string

const (
	Boot                    Code = "daemon_boot"
	RecoveryStarted         Code = "recovery_started"
	RecoveryFinished        Code = "recovery_finished"
	RecoveryFailed          Code = "recovery_failed"
	Shutdown                Code = "daemon_shutdown"
	ShutdownIncomplete      Code = "shutdown_incomplete"
	PriorBootUnclean        Code = "prior_boot_unclean"
	LegacyOmitted           Code = "legacy_text_omitted"
	JobAdmitted             Code = "job_admitted"
	JobTerminal             Code = "job_terminal"
	JobRecovered            Code = "job_recovered"
	ProviderAttempt         Code = "provider_attempt"
	SyncCommitted           Code = "sync_committed"
	RetryAdmitted           Code = "retry_admitted"
	WriterContended         Code = "writer_contended"
	SyncStageChanged        Code = "sync_stage_changed"
	AdminRequestCompleted   Code = "admin_request_completed"
	MaintenanceStateChanged Code = "maintenance_state_changed"
	LocalAuthorityCommitted Code = "local_authority_committed"
)

type template struct{ stream, severity, component, message string }

// EventTemplate exposes reviewed product text, not retained output. UI clients
// use this closed catalog for filters and safe context rather than raw messages.
type EventTemplate struct {
	Stream    string `json:"stream"`
	Severity  string `json:"severity"`
	Component string `json:"component"`
	Message   string `json:"message"`
}

func EventTemplates() map[Code]EventTemplate {
	out := make(map[Code]EventTemplate, len(catalog))
	for code, value := range catalog {
		out[code] = EventTemplate{value.stream, value.severity, value.component, value.message}
	}
	return out
}

var catalog = map[Code]template{
	Boot:                    {"stdout", "info", "daemon", "Daemon observation started."},
	RecoveryStarted:         {"stdout", "info", "recovery", "Recovery inspection started."},
	RecoveryFinished:        {"stdout", "info", "recovery", "Recovery inspection finished."},
	RecoveryFailed:          {"stderr", "error", "recovery", "Recovery inspection failed."},
	Shutdown:                {"stdout", "info", "daemon", "Daemon workers unwound before observation stopped."},
	ShutdownIncomplete:      {"stderr", "warning", "daemon", "Daemon teardown was incomplete."},
	PriorBootUnclean:        {"stderr", "warning", "daemon", "The preceding observed boot has no clean shutdown marker."},
	LegacyOmitted:           {"stderr", "warning", "legacy", "Unrecognized legacy output was omitted."},
	JobAdmitted:             {"stdout", "info", "jobs", "A new job was durably admitted."},
	JobTerminal:             {"stdout", "info", "jobs", "A job reached a confirmed terminal state."},
	JobRecovered:            {"stdout", "info", "recovery", "An actual job recovery reached a confirmed state."},
	ProviderAttempt:         {"stdout", "info", "provider", "An actual bounded provider attempt finished."},
	SyncCommitted:           {"stdout", "info", "sync", "Fetched source graphs reached a confirmed cache commit."},
	RetryAdmitted:           {"stdout", "info", "sync", "A collection entered an actual retry attempt."},
	WriterContended:         {"stderr", "warning", "writer", "An actual cache writer attempt was denied."},
	SyncStageChanged:        {"stdout", "info", "sync", "A sync stage reached a confirmed journal state."},
	AdminRequestCompleted:   {"stdout", "info", "admin", "An Admin HTTP request completed."},
	MaintenanceStateChanged: {"stdout", "info", "maintenance", "Maintenance reached a confirmed state transition."},
	LocalAuthorityCommitted: {"stdout", "info", "authority", "A local project source commit was confirmed."},
}

// Ref is opaque and cannot be constructed by assigning an unchecked string.
// KnownRef is for identifiers resolved from local execution authority, NOT a
// request's arbitrary labels, headers, paths or error text.
type Ref struct{ value string }

func KnownRef(kind, authorityID string) Ref {
	if authorityID == "" || len(authorityID) > 4096 {
		return Ref{}
	}
	switch kind {
	case "job", "registration", "cache", "repo", "correlation", "stage", "source", "project":
	default:
		return Ref{}
	}
	sum := sha256.Sum256([]byte(kind + "\x00" + authorityID))
	return Ref{kind + "-" + hex.EncodeToString(sum[:16])}
}

type Context struct {
	Job, Registration, Cache, Repo, Correlation Ref
	Stage, Source                               Ref
	PreviousRegistration                        Ref
	Project                                     Ref
	ExecutionDetails
}
type Event struct {
	ExecutionDetails
	Legacy                  bool      `json:"legacy,omitempty"`
	ObservedBytes           int       `json:"observed_bytes,omitempty"`
	Schema                  int       `json:"schema"`
	EventID                 string    `json:"event_id"`
	OccurredAt              time.Time `json:"occurred_at"`
	BootID                  string    `json:"boot_id"`
	Sequence                uint64    `json:"sequence"`
	Stream                  string    `json:"stream"`
	Severity                string    `json:"severity"`
	Component               string    `json:"component"`
	Code                    Code      `json:"code"`
	Message                 string    `json:"message"`
	JobRef                  string    `json:"job_ref,omitempty"`
	RegistrationRef         string    `json:"registration_ref,omitempty"`
	CacheRef                string    `json:"cache_ref,omitempty"`
	RepoRef                 string    `json:"repo_ref,omitempty"`
	CorrelationRef          string    `json:"correlation_ref,omitempty"`
	StageRef                string    `json:"stage_ref,omitempty"`
	SourceRef               string    `json:"source_ref,omitempty"`
	PreviousRegistrationRef string    `json:"previous_registration_ref,omitempty"`
	ProjectRef              string    `json:"project_ref,omitempty"`
}

var hexID = regexp.MustCompile(`^[a-f0-9]{32}$`)

func validRef(value, kind string) bool {
	return value == "" || (len(value) == len(kind)+33 && value[:len(kind)+1] == kind+"-" && hexID.MatchString(value[len(kind)+1:]))
}
func (e Event) valid() bool {
	t, ok := catalog[e.Code]
	streamOK := e.Stream == t.stream
	if e.Legacy {
		streamOK = e.Stream == "stdout" || e.Stream == "stderr"
	}
	return ok && e.validExecution() && e.ObservedBytes >= 0 && e.ObservedBytes <= MaxPageBytes && (e.Legacy || e.ObservedBytes == 0) && e.Schema == 1 && e.Sequence > 0 && hexID.MatchString(e.BootID) && hexID.MatchString(e.EventID) &&
		!e.OccurredAt.IsZero() && streamOK && e.Severity == t.severity && e.Component == t.component && e.Message == t.message &&
		validRef(e.JobRef, "job") && validRef(e.RegistrationRef, "registration") && validRef(e.CacheRef, "cache") && validRef(e.RepoRef, "repo") && validRef(e.CorrelationRef, "correlation") && validRef(e.StageRef, "stage") && validRef(e.SourceRef, "source") && validRef(e.PreviousRegistrationRef, "registration") && validRef(e.ProjectRef, "project")
}
func marshalEvent(e Event) ([]byte, error) {
	if !e.valid() {
		return nil, errors.New("invalid observation event")
	}
	b, err := json.Marshal(e)
	if len(b)+1 > MaxEventBytes {
		return nil, errors.New("observation event exceeds bound")
	}
	return append(b, '\n'), err
}
