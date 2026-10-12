// Package authority defines local-authoritative project contracts. It owns no
// storage, credential resolver, network client, or background work. Authority
// contracts are not an implementation of the later storage/runtime cohorts.
package authority

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

const Protocol = "authority.v1"
const Schema = 1

type Kind string

const (
	Remote Kind = "remote_authoritative"
	Local  Kind = "local_authoritative"
)

type Code string

const (
	InvalidContract        Code = "authority_invalid_contract"
	Unavailable            Code = "authority_unavailable"
	Unsupported            Code = "authority_unsupported"
	NoAuthority            Code = "authority_not_found"
	PolicyDenied           Code = "authority_policy_denied"
	Conflict               Code = "authority_conflict"
	RevisionConflict       Code = "authority_revision_conflict"
	InitializationRequired Code = "authority_initialization_required"
)

// Error text is fixed. Untrusted keys, coordinates, payloads and private paths
// never become diagnostic messages by interpolation.
type Error struct{ Code Code }

func (e Error) DiagnosticCode() string { return string(e.Code) }
func (e Error) Error() string {
	switch e.Code {
	case Unavailable:
		return "local authority is unavailable"
	case Unsupported:
		return "local authority contract is unsupported"
	case NoAuthority:
		return "local authority is not registered"
	case PolicyDenied:
		return "local authority policy denies this operation"
	case Conflict:
		return "local authority identity or intent conflicts"
	case RevisionConflict:
		return "local authority revision conflicts"
	case InitializationRequired:
		return "project intent requires explicit local initialization; remote fallback is forbidden"
	default:
		return "invalid local authority contract"
	}
}

type UUID string

func (id UUID) Valid() bool {
	s := string(id)
	if len(s) != 36 || s != strings.ToLower(s) || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return false
	}
	b, err := hex.DecodeString(strings.ReplaceAll(s, "-", ""))
	if err != nil || len(b) != 16 || b[6]>>4 != 4 || b[8]>>6 != 2 {
		return false
	}
	return true
}

// NewUUID is used only by explicit CLI initialization plans or authoritative
// entity allocation, never to infer authority from a cache or repository id.
func NewUUID() (UUID, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", Error{Unavailable}
	}
	b[6] = b[6]&15 | 64
	b[8] = b[8]&63 | 128
	h := hex.EncodeToString(b[:])
	return UUID(fmt.Sprintf("%s-%s-%s-%s-%s", h[:8], h[8:12], h[12:16], h[16:20], h[20:])), nil
}

type EntityKind string

const (
	Issue     EntityKind = "issue"
	Comment   EntityKind = "comment"
	Label     EntityKind = "label"
	Milestone EntityKind = "milestone"
	Document  EntityKind = "document"
	Link      EntityKind = "link"
)

func (k EntityKind) Valid() bool {
	switch k {
	case Issue, Comment, Label, Milestone, Document, Link:
		return true
	}
	return false
}

type Citation struct {
	ProjectID UUID       `json:"project_id"`
	EntityID  UUID       `json:"entity_id"`
	Kind      EntityKind `json:"kind"`
	Revision  uint64     `json:"revision"`
}

func (c Citation) Validate() error {
	if !c.ProjectID.Valid() || !c.EntityID.Valid() || !c.Kind.Valid() || c.Revision == 0 {
		return Error{InvalidContract}
	}
	return nil
}

type NetworkPolicy struct {
	DataEgress       string `json:"data_egress" yaml:"data_egress"`
	ControlTransport string `json:"control_transport" yaml:"control_transport"`
}

func (p NetworkPolicy) Validate() error {
	if p.DataEgress != "deny" && p.DataEgress != "loopback_embeddings" {
		return Error{InvalidContract}
	}
	if p.ControlTransport != "ipc_only" && p.ControlTransport != "loopback_admin" {
		return Error{InvalidContract}
	}
	return nil
}

func (p NetworkPolicy) Label() string {
	if p.DataEgress == "loopback_embeddings" {
		return "loopback_provider_network"
	}
	if p.ControlTransport == "loopback_admin" {
		return "loopback_control_only"
	}
	return "zero_network"
}

// RuntimeProfile is attested runtime state, never a per-request override.
// Actual process/network factory qualification is owned by the runtime cohort.
type RuntimeProfile struct {
	Protocol         string
	SchemaMin        int
	SchemaMax        int
	Strict           bool
	TCP              bool
	UDP              bool
	DNS              bool
	Admin            bool
	RemoteProjects   bool
	LoopbackProjects bool
}

func Preflight(p NetworkPolicy, r RuntimeProfile) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if r.Protocol != Protocol || r.SchemaMin < 1 || r.SchemaMax < r.SchemaMin || r.SchemaMin > Schema || r.SchemaMax < Schema {
		return Error{Unsupported}
	}
	if p.Label() == "zero_network" {
		if !r.Strict || r.TCP || r.UDP || r.DNS || r.Admin || r.RemoteProjects || r.LoopbackProjects {
			return Error{PolicyDenied}
		}
	} else if r.Strict || r.RemoteProjects {
		return Error{PolicyDenied}
	}
	if p.ControlTransport == "ipc_only" && r.Admin {
		return Error{PolicyDenied}
	}
	return nil
}
