package authority

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
)

var opaqueName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

type Intent struct {
	Project Manifest `json:"project" yaml:"project"`
}

type Manifest struct {
	Schema    int           `json:"schema" yaml:"schema"`
	ID        UUID          `json:"id,omitempty" yaml:"id,omitempty"`
	Slug      string        `json:"slug" yaml:"slug"`
	Authority Kind          `json:"authority" yaml:"authority"`
	Entities  []EntityKind  `json:"entities" yaml:"entities"`
	Network   NetworkPolicy `json:"network" yaml:"network"`
	Search    SearchIntent  `json:"search" yaml:"search"`
}

type SearchIntent struct {
	Default  string `json:"default" yaml:"default"`
	Fallback string `json:"fallback" yaml:"fallback"`
}

func ParseIntent(data []byte) (Manifest, error) {
	var i Intent
	if err := decodeYAML(data, &i); err != nil {
		return Manifest{}, err
	}
	if err := i.Project.Validate(); err != nil {
		return Manifest{}, err
	}
	return i.Project, nil
}

func (m Manifest) Validate() error {
	if m.Schema != Schema {
		return Error{Unsupported}
	}
	if m.Authority != Local || !opaqueName.MatchString(m.Slug) || m.ID != "" && !m.ID.Valid() {
		return Error{InvalidContract}
	}
	if err := m.Network.Validate(); err != nil {
		return err
	}
	if m.Search.Default != "hybrid" && m.Search.Default != "full_text" || m.Search.Fallback != "full_text" {
		return Error{InvalidContract}
	}
	if len(m.Entities) != 6 {
		return Error{InvalidContract}
	}
	seen := map[EntityKind]bool{}
	for _, k := range m.Entities {
		if !k.Valid() || seen[k] {
			return Error{InvalidContract}
		}
		seen[k] = true
	}
	return nil
}

// Registration is machine-local intent, not a path or authority selected by
// cache placement, a GitCode repository binding or provider configuration.
type Registration struct {
	StorageRef string       `json:"storage_ref" yaml:"storage_ref"`
	Writer     string       `json:"writer" yaml:"writer"`
	Backup     BackupPolicy `json:"backup" yaml:"backup"`
	Indexing   IndexPolicy  `json:"indexing" yaml:"indexing"`
}

type BackupPolicy struct {
	Retain   int   `json:"retain" yaml:"retain"`
	MaxBytes int64 `json:"max_bytes" yaml:"max_bytes"`
}

type IndexPolicy struct {
	IntervalSeconds  int    `json:"interval_seconds" yaml:"interval_seconds"`
	MaxChunks        int    `json:"max_chunks" yaml:"max_chunks"`
	EmbeddingProfile string `json:"embedding_profile" yaml:"embedding_profile"`
}

func ParseRegistration(data []byte, m Manifest) (Registration, error) {
	var r Registration
	if err := decodeYAML(data, &r); err != nil {
		return Registration{}, err
	}
	if err := r.Validate(m); err != nil {
		return Registration{}, err
	}
	return r, nil
}

func (r Registration) Validate(m Manifest) error {
	if err := m.Validate(); err != nil {
		return err
	}
	if !opaqueName.MatchString(r.StorageRef) || r.Writer != "coordinator" || r.Backup.Retain < 1 || r.Backup.Retain > 128 || r.Backup.MaxBytes < 1<<20 || r.Backup.MaxBytes > 1<<40 || r.Indexing.IntervalSeconds < 60 || r.Indexing.IntervalSeconds > 86400 || r.Indexing.MaxChunks < 1 || r.Indexing.MaxChunks > 4096 {
		return Error{InvalidContract}
	}
	if m.Network.DataEgress == "deny" {
		if r.Indexing.EmbeddingProfile != "disabled" {
			return Error{PolicyDenied}
		}
	} else if !opaqueName.MatchString(r.Indexing.EmbeddingProfile) || r.Indexing.EmbeddingProfile == "disabled" {
		return Error{InvalidContract}
	}
	return nil
}

type InitializationPlan struct {
	ProjectID    UUID     `json:"project_id"`
	StorageRef   string   `json:"storage_ref"`
	IntentDigest string   `json:"intent_digest"`
	NetworkState string   `json:"network_state"`
	Status       string   `json:"status"`
	Manifest     Manifest `json:"manifest"`
}

// Initialization planning is invoked only by a CLI control path; it has no
// filesystem/network effects. The later workflow resolves storage_ref privately
// and generation-fences confirmation before durable publication. A plan is not
// enrollment or evidence of a writer/runtime/storage capability.
func PlanInitialization(origin string, m Manifest, r Registration) (InitializationPlan, error) {
	if origin != "cli" {
		return InitializationPlan{}, Error{PolicyDenied}
	}
	if err := r.Validate(m); err != nil {
		return InitializationPlan{}, err
	}
	if m.ID == "" {
		id, err := NewUUID()
		if err != nil {
			return InitializationPlan{}, err
		}
		m.ID = id
	}
	data, err := json.Marshal(struct {
		Manifest     Manifest
		Registration Registration
	}{m, r})
	if err != nil {
		return InitializationPlan{}, Error{InvalidContract}
	}
	digest := sha256.Sum256(data)
	return InitializationPlan{ProjectID: m.ID, StorageRef: r.StorageRef, IntentDigest: hex.EncodeToString(digest[:]), NetworkState: m.Network.Label(), Status: "planned_not_enrolled", Manifest: m}, nil
}
