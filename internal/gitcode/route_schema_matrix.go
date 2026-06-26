package gitcode

import (
	"fmt"
	"sort"
)

type ProductArea string

const (
	ProductAreaIssues       ProductArea = "issues"
	ProductAreaLabels       ProductArea = "labels"
	ProductAreaMilestones   ProductArea = "milestones"
	ProductAreaPullRequests ProductArea = "pull_requests"
	ProductAreaComments     ProductArea = "comments"
	ProductAreaWiki         ProductArea = "wiki"
)

func validProductArea(area ProductArea) bool {
	switch area {
	case ProductAreaIssues, ProductAreaLabels, ProductAreaMilestones, ProductAreaPullRequests, ProductAreaComments, ProductAreaWiki:
		return true
	}
	return false
}

type SupportStatus string

const (
	SupportStatusSupported   SupportStatus = "supported"
	SupportStatusDeferred    SupportStatus = "deferred"
	SupportStatusUnsupported SupportStatus = "unsupported"
)

func validSupportStatus(status SupportStatus) bool {
	switch status {
	case SupportStatusSupported, SupportStatusDeferred, SupportStatusUnsupported:
		return true
	}
	return false
}

type EvidenceClass string

const (
	EvidenceClassOpenAPI   EvidenceClass = "OpenAPI"
	EvidenceClassLiveProbe EvidenceClass = "live_probe"
	EvidenceClassDeferred  EvidenceClass = "deferred"
)

func validEvidenceClass(ev EvidenceClass) bool {
	switch ev {
	case EvidenceClassOpenAPI, EvidenceClassLiveProbe, EvidenceClassDeferred:
		return true
	}
	return false
}

type RouteFamily string

const RouteFamilyAPIV5 RouteFamily = "/api/v5"

type UnsupportedDiagnostic struct {
	Code          string
	CapabilityKey string
	Message       string
}

type SurfaceSpec struct {
	Area       ProductArea
	Status     SupportStatus
	Route      RouteFamily
	Evidence   EvidenceClass
	Diagnostic *UnsupportedDiagnostic
}

type RouteSchemaMatrix struct {
	specs map[ProductArea]SurfaceSpec
}

func DefaultRouteSchemaMatrix() RouteSchemaMatrix {
	return RouteSchemaMatrix{
		specs: map[ProductArea]SurfaceSpec{
			ProductAreaIssues: {
				Area:     ProductAreaIssues,
				Status:   SupportStatusSupported,
				Route:    RouteFamilyAPIV5,
				Evidence: EvidenceClassOpenAPI,
			},
			ProductAreaLabels: {
				Area:     ProductAreaLabels,
				Status:   SupportStatusSupported,
				Route:    RouteFamilyAPIV5,
				Evidence: EvidenceClassOpenAPI,
			},
			ProductAreaMilestones: {
				Area:     ProductAreaMilestones,
				Status:   SupportStatusSupported,
				Route:    RouteFamilyAPIV5,
				Evidence: EvidenceClassOpenAPI,
			},
			ProductAreaWiki: {
				Area:     ProductAreaWiki,
				Status:   SupportStatusSupported,
				Route:    RouteFamilyAPIV5,
				Evidence: EvidenceClassLiveProbe,
			},
			ProductAreaPullRequests: {
				Area:     ProductAreaPullRequests,
				Status:   SupportStatusSupported,
				Route:    RouteFamilyAPIV5,
				Evidence: EvidenceClassOpenAPI,
			},
			ProductAreaComments: {
				Area:     ProductAreaComments,
				Status:   SupportStatusDeferred,
				Route:    RouteFamilyAPIV5,
				Evidence: EvidenceClassDeferred,
				Diagnostic: &UnsupportedDiagnostic{
					Code:          "unsupported_capability",
					CapabilityKey: "comments_read",
					Message:       "Comment reads are deferred for iteration 5 pending child-record shape validation",
				},
			},
		},
	}
}

func (m RouteSchemaMatrix) Spec(area ProductArea) (SurfaceSpec, bool) {
	spec, ok := m.specs[area]
	return spec, ok
}

func (m RouteSchemaMatrix) RequireDeclared(area ProductArea) error {
	_, ok := m.specs[area]
	if !ok {
		return fmt.Errorf("gitcode: product area %q is not declared in RouteSchemaMatrix", area)
	}
	return nil
}

func (m RouteSchemaMatrix) ValidateCoverage(required []ProductArea) error {
	for _, area := range required {
		if err := m.RequireDeclared(area); err != nil {
			return err
		}
		spec := m.specs[area]
		if err := validateSurfaceSpec(spec); err != nil {
			return fmt.Errorf("gitcode: invalid surface spec for %s: %w", area, err)
		}
	}
	return nil
}

func (m RouteSchemaMatrix) Preflight(area ProductArea) error {
	spec, ok := m.Spec(area)
	if !ok {
		return fmt.Errorf("gitcode: product area %q not declared in RouteSchemaMatrix", area)
	}
	if spec.Status == SupportStatusDeferred || spec.Status == SupportStatusUnsupported {
		diag := spec.Diagnostic
		if diag == nil {
			return ErrUnsupportedCapability{
				CapabilityKey: string(area) + "_read",
				Message:       fmt.Sprintf("product area %q is %s", area, spec.Status),
			}
		}
		return ErrUnsupportedCapability{
			CapabilityKey: diag.CapabilityKey,
			Message:       diag.Message,
		}
	}
	return nil
}

func (m RouteSchemaMatrix) DeclaredAreas() []ProductArea {
	areas := make([]ProductArea, 0, len(m.specs))
	for area := range m.specs {
		areas = append(areas, area)
	}
	sort.Slice(areas, func(i, j int) bool { return areas[i] < areas[j] })
	return areas
}

func validateSurfaceSpec(spec SurfaceSpec) error {
	if !validProductArea(spec.Area) {
		return fmt.Errorf("unknown ProductArea %q", spec.Area)
	}
	if !validSupportStatus(spec.Status) {
		return fmt.Errorf("unknown SupportStatus %q", spec.Status)
	}
	if !validEvidenceClass(spec.Evidence) {
		return fmt.Errorf("unknown EvidenceClass %q", spec.Evidence)
	}
	if spec.Route == "" {
		return fmt.Errorf("RouteFamily must not be empty")
	}
	if spec.Route != RouteFamilyAPIV5 {
		return fmt.Errorf("RouteFamily must be %q, got %q", RouteFamilyAPIV5, spec.Route)
	}
	if spec.Status == SupportStatusSupported && spec.Evidence == EvidenceClassDeferred {
		return fmt.Errorf("supported spec must not use deferred evidence")
	}
	if spec.Status == SupportStatusDeferred || spec.Status == SupportStatusUnsupported {
		if spec.Evidence != EvidenceClassDeferred {
			return fmt.Errorf("%s spec must use deferred evidence, got %q", spec.Status, spec.Evidence)
		}
		if spec.Diagnostic == nil {
			return fmt.Errorf("%s spec requires a diagnostic", spec.Status)
		}
		if spec.Diagnostic.Code != "unsupported_capability" {
			return fmt.Errorf("%s spec must have diagnostic code \"unsupported_capability\", got %q", spec.Status, spec.Diagnostic.Code)
		}
		if spec.Diagnostic.CapabilityKey == "" {
			return fmt.Errorf("%s spec diagnostic must have a non-empty capability key", spec.Status)
		}
		if spec.Diagnostic.Message == "" {
			return fmt.Errorf("%s spec diagnostic must have a non-empty message", spec.Status)
		}
	}
	if spec.Status == SupportStatusSupported && spec.Diagnostic != nil {
		return fmt.Errorf("supported spec must not have a diagnostic")
	}
	return nil
}
