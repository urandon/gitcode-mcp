package observability

import (
	"sort"
	"time"
)

const MaxIncidents = 128
const MaxHealthEvidence = 32768

// HealthEvidence is a trusted, content-free projection of durable authority.
// No log, counter, provider body or error string is an input. At and success
// are authority timestamps, not the time a polling reader happened to arrive.
type HealthEvidence struct {
	Cohort                             Ref
	Origin, Collection, Signal, Reason string
	Refs                               Context
	At, LastSuccess, RetryAfter        time.Time
}

type Incident struct {
	IncidentRef     string     `json:"incident_ref"`
	Origin          string     `json:"origin"`
	Collection      string     `json:"collection"`
	State           string     `json:"state"`
	Reason          string     `json:"reason"`
	FirstObservedAt time.Time  `json:"first_observed_at"`
	LastObservedAt  time.Time  `json:"last_observed_at"`
	LastSuccessAt   *time.Time `json:"last_success_at,omitempty"`
	RetryAfter      *time.Time `json:"retry_after,omitempty"`
	JobRef          string     `json:"job_ref,omitempty"`
	RegistrationRef string     `json:"registration_ref,omitempty"`
	CacheRef        string     `json:"cache_ref,omitempty"`
	RepoRef         string     `json:"repo_ref,omitempty"`
	Coverage        string     `json:"coverage"`
}
type ExecutionHealth struct {
	Schema     int        `json:"schema"`
	ObservedAt time.Time  `json:"observed_at"`
	State      string     `json:"state"`
	Coverage   string     `json:"coverage"`
	Reason     string     `json:"reason"`
	Source     string     `json:"source"`
	Limit      int        `json:"limit"`
	Omitted    int        `json:"omitted"`
	Active     int        `json:"active"`
	Recovered  int        `json:"recovered"`
	Incidents  []Incident `json:"incidents"`
}

func UnavailableExecutionHealth(at time.Time, reason string) ExecutionHealth {
	switch reason {
	case "authority_busy", "authority_missing", "evidence_limit":
	default:
		reason = "authority_unavailable"
	}
	return ExecutionHealth{Schema: 1, ObservedAt: at.UTC(), State: "unknown", Coverage: "unavailable", Reason: reason, Source: "durable_jobs_and_maintenance", Limit: MaxIncidents, Incidents: []Incident{}}
}

type incidentCohort struct {
	first, last, success time.Time
	problem              HealthEvidence
	hasProblem           bool
}

// ProjectExecutionHealth does not retain or mutate execution state. History is
// reconstructed from retained durable evidence on every read, also after boot.
// This deliberately makes no claim about pruned jobs or unobserved work.
func ProjectExecutionHealth(at time.Time, evidence []HealthEvidence) ExecutionHealth {
	if len(evidence) > MaxHealthEvidence {
		return UnavailableExecutionHealth(at, "evidence_limit")
	}
	out := ExecutionHealth{Schema: 1, ObservedAt: at.UTC(), State: "healthy", Coverage: "retained", Reason: "durable_retention_boundary", Source: "durable_jobs_and_maintenance", Limit: MaxIncidents, Incidents: []Incident{}}
	cohorts := make(map[string]*incidentCohort)
	for _, e := range evidence {
		if !validHealthEvidence(e) {
			out.Coverage, out.Reason = "partial", "invalid_authority_evidence"
			continue
		}
		key := e.Cohort.value
		c := cohorts[key]
		if c == nil {
			c = &incidentCohort{}
			cohorts[key] = c
		}
		if e.LastSuccess.After(c.success) {
			c.success = e.LastSuccess.UTC()
		}
		if e.Signal == "succeeded" {
			if e.At.After(c.success) {
				c.success = e.At.UTC()
			}
			continue
		}
		if !c.hasProblem || e.At.Before(c.first) {
			c.first = e.At.UTC()
		}
		if !c.hasProblem || e.At.After(c.last) || e.At.Equal(c.last) && laterHealthTie(e, c.problem) {
			c.last, c.problem = e.At.UTC(), e
		}
		c.hasProblem = true
	}
	for key, c := range cohorts {
		if !c.hasProblem {
			continue
		}
		e := c.problem
		i := Incident{IncidentRef: key, Origin: e.Origin, Collection: e.Collection, State: "active", Reason: e.Reason, FirstObservedAt: c.first, LastObservedAt: c.last,
			JobRef: e.Refs.Job.value, RegistrationRef: e.Refs.Registration.value, CacheRef: e.Refs.Cache.value, RepoRef: e.Refs.Repo.value, Coverage: "retained"}
		if !c.success.IsZero() {
			success := c.success
			i.LastSuccessAt = &success
		}
		if c.success.After(c.last) {
			i.State = "recovered"
			i.LastObservedAt = c.success
			out.Recovered++
		} else {
			out.Active++
			out.State = "degraded"
			switch e.Signal {
			case "retrying", "waiting_writer", "interrupted":
				i.State = e.Signal
			}
			if !e.RetryAfter.IsZero() {
				retry := e.RetryAfter.UTC()
				i.RetryAfter = &retry
			}
		}
		out.Incidents = append(out.Incidents, i)
	}
	sort.Slice(out.Incidents, func(i, j int) bool {
		a, b := out.Incidents[i], out.Incidents[j]
		if (a.State == "recovered") != (b.State == "recovered") {
			return a.State != "recovered"
		}
		if !a.LastObservedAt.Equal(b.LastObservedAt) {
			return a.LastObservedAt.After(b.LastObservedAt)
		}
		return a.IncidentRef < b.IncidentRef
	})
	if len(out.Incidents) > MaxIncidents {
		out.Omitted = len(out.Incidents) - MaxIncidents
		out.Incidents = out.Incidents[:MaxIncidents]
		out.Coverage, out.Reason = "partial", "incident_limit"
	}
	if out.State == "healthy" && out.Coverage == "partial" {
		out.State = "unknown"
	}
	return out
}

func laterHealthTie(a, b HealthEvidence) bool {
	if healthSignalRank(a.Signal) != healthSignalRank(b.Signal) {
		return healthSignalRank(a.Signal) > healthSignalRank(b.Signal)
	}
	if a.Reason != b.Reason {
		return a.Reason < b.Reason
	}
	return a.Refs.Job.value < b.Refs.Job.value
}

func healthSignalRank(signal string) int {
	switch signal {
	case "failed":
		return 4
	case "interrupted":
		return 3
	case "waiting_writer":
		return 2
	case "retrying":
		return 1
	}
	return 0
}
func validHealthEvidence(e HealthEvidence) bool {
	if e.Cohort.value == "" || !validRef(e.Cohort.value, "correlation") || e.At.IsZero() {
		return false
	}
	switch e.Origin {
	case "sync", "rag", "repository_docs", "maintenance", "other":
	default:
		return false
	}
	switch e.Collection {
	case "issues", "issue_comments", "wiki", "pulls", "pr_comments", "other":
	default:
		return false
	}
	switch e.Signal {
	case "succeeded", "failed", "interrupted", "waiting_writer", "retrying":
	default:
		return false
	}
	switch e.Reason {
	case "operation_failed", "provider_timeout", "provider_unavailable", "provider_forbidden", "provider_rate_limited", "provider_invalid", "cache_authority", "configuration_invalid", "embedding_failed", "interrupted", "writer_wait", "retry_wait", "succeeded":
	default:
		return false
	}
	return validRef(e.Refs.Job.value, "job") && validRef(e.Refs.Registration.value, "registration") && validRef(e.Refs.Cache.value, "cache") && validRef(e.Refs.Repo.value, "repo") && validRef(e.Refs.Correlation.value, "correlation")
}
