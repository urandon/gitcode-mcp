package capability

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// Policy is a compiled, immutable instance ceiling. A nil policy preserves the
// legacy transport contract. It is never decoded from a client or job snapshot.
type Policy struct {
	grants map[string]Decision
	view   PolicyView
}

type Rule struct {
	Allow []string
	Deny  []string
}

type Specification struct {
	Profile  string
	Ceiling  Rule
	Profiles map[string]Rule
}

type PolicyLayer struct {
	Specification *Specification
	Source        string // finite authority class, never a filename
}

type Decision struct {
	ID      string `json:"id"`
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason"`
}

type PolicyView struct {
	Schema      int        `json:"schema"`
	Managed     bool       `json:"managed"`
	Profile     string     `json:"profile,omitempty"`
	ProfileRef  string     `json:"profile_ref,omitempty"`
	Fingerprint string     `json:"fingerprint"`
	Sources     []string   `json:"sources"`
	Decisions   []Decision `json:"decisions"`
}

type PolicyError struct {
	Code       string
	Capability string
}

func (e PolicyError) Error() string {
	return e.Code + ": ask the operator to review the trusted instance policy"
}
func (e PolicyError) DiagnosticCode() string { return e.Code }

func InvalidPolicy() error { return PolicyError{Code: "capability_policy_invalid"} }

func CompilePolicy(readOnly bool, layers ...PolicyLayer) (*Policy, error) {
	return compilePolicy(Capabilities(), readOnly, layers...)
}

func compilePolicy(catalog []Capability, readOnly bool, layers ...PolicyLayer) (*Policy, error) {
	managed := false
	for _, layer := range layers {
		managed = managed || layer.Specification != nil
	}
	if !managed {
		return nil, nil
	}
	p := &Policy{grants: map[string]Decision{}, view: PolicyView{Schema: 1, Managed: true, Sources: []string{}, Decisions: []Decision{}}}
	universe := map[string]Capability{}
	for _, item := range catalog {
		universe[item.ID] = item
	}
	for id := range universe {
		p.grants[id] = Decision{ID: id, Allowed: true, Reason: "allowed"}
	}
	apply := func(rule Rule) error {
		for _, list := range [][]string{rule.Allow, rule.Deny} {
			seen := map[string]bool{}
			for _, id := range list {
				if _, ok := universe[id]; !ok || seen[id] {
					return InvalidPolicy()
				}
				seen[id] = true
			}
		}
		allow, deny := map[string]bool{}, map[string]bool{}
		for _, id := range rule.Allow {
			allow[id] = true
		}
		for _, id := range rule.Deny {
			deny[id] = true
		}
		for id, decision := range p.grants {
			if rule.Allow != nil && !allow[id] {
				decision.Allowed = false
				if decision.Reason == "allowed" {
					decision.Reason = "not_allowlisted"
				}
			}
			if deny[id] {
				decision.Allowed = false
				decision.Reason = "explicit_deny"
			}
			p.grants[id] = decision
		}
		return nil
	}
	for _, layer := range layers {
		spec := layer.Specification
		if spec == nil {
			continue
		}
		if layer.Source != "machine" && layer.Source != "deployment" {
			return nil, InvalidPolicy()
		}
		p.view.Sources = append(p.view.Sources, layer.Source)
		// Validate every profile, not just the active one.
		for _, rule := range spec.Profiles {
			for _, list := range [][]string{rule.Allow, rule.Deny} {
				seen := map[string]bool{}
				for _, id := range list {
					if _, ok := universe[id]; !ok || seen[id] {
						return nil, InvalidPolicy()
					}
					seen[id] = true
				}
			}
		}
		if err := apply(spec.Ceiling); err != nil {
			return nil, err
		}
		if spec.Profile != "" {
			rule, ok := spec.Profiles[spec.Profile]
			if !ok {
				return nil, InvalidPolicy()
			}
			if err := apply(rule); err != nil {
				return nil, err
			}
			p.view.Profile = spec.Profile
		}
	}
	if readOnly {
		for id, item := range universe {
			if item.Safety != SafetyReadOnly {
				d := p.grants[id]
				d.Allowed = false
				d.Reason = "read_ceiling"
				p.grants[id] = d
			}
		}
	}
	// Current live operations resolve repository routing through the shared
	// repo_status boundary. Propagate this dependency before any transport can
	// bootstrap credentials/cache and before publishing effective discovery.
	for changed := true; changed; {
		changed = false
		for id, dependencies := range capabilityDependencies {
			for _, dependency := range dependencies {
				if !p.grants[dependency].Allowed {
					d := p.grants[id]
					if d.Allowed {
						d.Allowed = false
						d.Reason = "dependency_denied"
						p.grants[id] = d
						changed = true
					}
				}
			}
		}
	}
	// Content-free policy inspection is a frozen control-plane exception.
	if _, ok := universe["capability_policy_status"]; ok {
		p.grants["capability_policy_status"] = Decision{ID: "capability_policy_status", Allowed: true, Reason: "control_plane"}
	}
	for _, decision := range p.grants {
		p.view.Decisions = append(p.view.Decisions, decision)
	}
	sort.Slice(p.view.Decisions, func(i, j int) bool { return p.view.Decisions[i].ID < p.view.Decisions[j].ID })
	if p.view.Profile != "" {
		h := sha256.Sum256([]byte(p.view.Profile))
		p.view.ProfileRef = "profile-" + hex.EncodeToString(h[:8])
	}
	encoded, _ := json.Marshal(p.view)
	h := sha256.Sum256(encoded)
	p.view.Fingerprint = "policy-" + hex.EncodeToString(h[:])
	return p, nil
}

func (p *Policy) Check(ids ...string) error {
	if p == nil {
		return nil
	}
	if len(ids) == 0 {
		return PolicyError{Code: "capability_policy_unmapped"}
	}
	for _, id := range ids {
		decision, ok := p.grants[id]
		if !ok {
			return PolicyError{Code: "capability_policy_unmapped"}
		}
		if !decision.Allowed {
			return PolicyError{Code: "capability_policy_denied", Capability: id}
		}
	}
	return nil
}

func (p *Policy) View() PolicyView {
	if p == nil {
		return PolicyView{Schema: 1, Fingerprint: "legacy", Sources: []string{}, Decisions: []Decision{}}
	}
	v := p.view
	v.Sources = append([]string{}, v.Sources...)
	v.Decisions = append([]Decision{}, v.Decisions...)
	return v
}

func (p *Policy) Fingerprint() string { return p.View().Fingerprint }

func (p *Policy) Require(surface, operation string) error {
	if p == nil {
		return nil
	}
	ids := Requirements(surface, operation)
	return p.Check(ids...)
}
