package gitcode

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestScenario005RouteSchemaMatrixDefaultShape(t *testing.T) {
	m := DefaultRouteSchemaMatrix()

	expectedAreas := []ProductArea{
		ProductAreaComments, ProductAreaIssues, ProductAreaLabels,
		ProductAreaMilestones, ProductAreaPullRequests, ProductAreaWiki,
	}
	areas := m.DeclaredAreas()
	if len(areas) != len(expectedAreas) {
		t.Fatalf("expected %d declared areas, got %d: %v", len(expectedAreas), len(areas), areas)
	}
	for i, a := range expectedAreas {
		if areas[i] != a {
			t.Fatalf("expected area[%d]=%q, got %q", i, a, areas[i])
		}
	}

	for _, area := range []ProductArea{
		ProductAreaIssues, ProductAreaLabels, ProductAreaMilestones, ProductAreaPullRequests, ProductAreaWiki,
	} {
		spec, ok := m.Spec(area)
		if !ok {
			t.Fatalf("area %q not found in default matrix", area)
		}
		if spec.Status != SupportStatusSupported {
			t.Fatalf("area %q: expected supported, got %s", area, spec.Status)
		}
		if spec.Route != RouteFamilyAPIV5 {
			t.Fatalf("area %q: expected route /api/v5, got %s", area, spec.Route)
		}
		if area == ProductAreaWiki {
			if spec.Evidence != EvidenceClassLiveProbe {
				t.Fatalf("area wiki: expected live_probe evidence, got %s", spec.Evidence)
			}
		} else {
			if spec.Evidence != EvidenceClassOpenAPI {
				t.Fatalf("area %q: expected OpenAPI evidence, got %s", area, spec.Evidence)
			}
		}
		if spec.Diagnostic != nil {
			t.Fatalf("area %q: supported spec must not have diagnostic", area)
		}
	}

	for _, area := range []ProductArea{ProductAreaComments} {
		spec, ok := m.Spec(area)
		if !ok {
			t.Fatalf("area %q not found in default matrix", area)
		}
		if spec.Status != SupportStatusDeferred {
			t.Fatalf("area %q: expected deferred, got %s", area, spec.Status)
		}
		if spec.Route != RouteFamilyAPIV5 {
			t.Fatalf("area %q: expected route /api/v5, got %s", area, spec.Route)
		}
		if spec.Evidence != EvidenceClassDeferred {
			t.Fatalf("area %q: expected deferred evidence, got %s", area, spec.Evidence)
		}
		if spec.Diagnostic == nil {
			t.Fatalf("area %q: deferred spec must have diagnostic", area)
		}
		if spec.Diagnostic.Code != "unsupported_capability" {
			t.Fatalf("area %q: diagnostic code %q, want unsupported_capability", area, spec.Diagnostic.Code)
		}
		if spec.Diagnostic.CapabilityKey == "" {
			t.Fatalf("area %q: capability key must not be empty", area)
		}
		if spec.Diagnostic.Message == "" {
			t.Fatalf("area %q: diagnostic message must not be empty", area)
		}
	}

	prSpec, _ := m.Spec(ProductAreaPullRequests)
	if prSpec.Status != SupportStatusSupported || prSpec.Diagnostic != nil {
		t.Fatalf("pull_requests should be supported without diagnostic: %+v", prSpec)
	}
	commentSpec, _ := m.Spec(ProductAreaComments)
	if commentSpec.Diagnostic.CapabilityKey != "comments_read" {
		t.Fatalf("comments capability key = %q, want comments_read", commentSpec.Diagnostic.CapabilityKey)
	}
}

func TestScenario005RouteSchemaMatrixSpecLookup(t *testing.T) {
	m := DefaultRouteSchemaMatrix()

	spec, ok := m.Spec(ProductAreaIssues)
	if !ok {
		t.Fatal("issues not found in default matrix")
	}
	if spec.Status != SupportStatusSupported {
		t.Fatalf("expected supported, got %s", spec.Status)
	}

	_, ok = m.Spec(ProductArea("nonexistent"))
	if ok {
		t.Fatal("nonexistent area should not be found")
	}
}

func TestScenario005RouteSchemaMatrixRequireDeclared(t *testing.T) {
	m := DefaultRouteSchemaMatrix()

	if err := m.RequireDeclared(ProductAreaIssues); err != nil {
		t.Fatalf("RequireDeclared(issues) should succeed: %v", err)
	}
	if err := m.RequireDeclared(ProductArea("nonexistent")); err == nil {
		t.Fatal("RequireDeclared(nonexistent) should fail")
	}
}

func TestScenario005RouteSchemaMatrixValidateCoverageSuccess(t *testing.T) {
	m := DefaultRouteSchemaMatrix()
	err := m.ValidateCoverage([]ProductArea{
		ProductAreaIssues, ProductAreaLabels, ProductAreaMilestones,
		ProductAreaWiki, ProductAreaPullRequests, ProductAreaComments,
	})
	if err != nil {
		t.Fatalf("ValidateCoverage with all declared areas failed: %v", err)
	}
}

func TestScenario005RouteSchemaMatrixValidateCoverageMissingArea(t *testing.T) {
	m := RouteSchemaMatrix{specs: map[ProductArea]SurfaceSpec{}}
	if err := m.RequireDeclared(ProductAreaIssues); err == nil {
		t.Fatal("RequireDeclared should fail for empty matrix")
	}
}

func TestScenario005RouteSchemaMatrixValidateCoverageBadEnums(t *testing.T) {
	makeMutated := func(fn func(spec *SurfaceSpec)) RouteSchemaMatrix {
		m := DefaultRouteSchemaMatrix()
		spec := m.specs[ProductAreaIssues]
		fn(&spec)
		m.specs[ProductAreaIssues] = spec
		return m
	}

	t.Run("unknown SupportStatus", func(t *testing.T) {
		m := makeMutated(func(s *SurfaceSpec) { s.Status = "invalid_status" })
		err := m.ValidateCoverage([]ProductArea{ProductAreaIssues})
		if err == nil {
			t.Fatal("expected validation failure for unknown SupportStatus")
		}
	})

	t.Run("unknown EvidenceClass", func(t *testing.T) {
		m := makeMutated(func(s *SurfaceSpec) { s.Evidence = "invalid_evidence" })
		err := m.ValidateCoverage([]ProductArea{ProductAreaIssues})
		if err == nil {
			t.Fatal("expected validation failure for unknown EvidenceClass")
		}
	})

	t.Run("non-api-v5 route family", func(t *testing.T) {
		m := makeMutated(func(s *SurfaceSpec) { s.Route = "/api/v4" })
		err := m.ValidateCoverage([]ProductArea{ProductAreaIssues})
		if err == nil {
			t.Fatal("expected validation failure for non-/api/v5 route family")
		}
	})

	t.Run("empty route family", func(t *testing.T) {
		m := makeMutated(func(s *SurfaceSpec) { s.Route = "" })
		err := m.ValidateCoverage([]ProductArea{ProductAreaIssues})
		if err == nil {
			t.Fatal("expected validation failure for empty route family")
		}
	})
}

func TestScenario005RouteSchemaMatrixValidateCoverageContradictorySpec(t *testing.T) {
	makeMutated := func(fn func(spec *SurfaceSpec)) RouteSchemaMatrix {
		m := DefaultRouteSchemaMatrix()
		spec := m.specs[ProductAreaIssues]
		fn(&spec)
		m.specs[ProductAreaIssues] = spec
		return m
	}

	t.Run("supported with deferred evidence", func(t *testing.T) {
		m := makeMutated(func(s *SurfaceSpec) {
			s.Status = SupportStatusSupported
			s.Evidence = EvidenceClassDeferred
		})
		err := m.ValidateCoverage([]ProductArea{ProductAreaIssues})
		if err == nil {
			t.Fatal("expected validation failure for supported spec with deferred evidence")
		}
	})

	t.Run("deferred without unsupported_capability code", func(t *testing.T) {
		m := DefaultRouteSchemaMatrix()
		mutated := m.specs[ProductAreaPullRequests]
		mutated.Status = SupportStatusDeferred
		mutated.Evidence = EvidenceClassDeferred
		mutated.Diagnostic = &UnsupportedDiagnostic{
			Code:          "other_code",
			CapabilityKey: "pr_read",
			Message:       "deferred",
		}
		m.specs[ProductAreaPullRequests] = mutated
		err := m.ValidateCoverage([]ProductArea{ProductAreaPullRequests})
		if err == nil {
			t.Fatal("expected validation failure for deferred spec without unsupported_capability code")
		}
	})

	t.Run("deferred without diagnostic", func(t *testing.T) {
		m := DefaultRouteSchemaMatrix()
		mutated := m.specs[ProductAreaPullRequests]
		mutated.Status = SupportStatusDeferred
		mutated.Evidence = EvidenceClassDeferred
		mutated.Diagnostic = nil
		m.specs[ProductAreaPullRequests] = mutated
		err := m.ValidateCoverage([]ProductArea{ProductAreaPullRequests})
		if err == nil {
			t.Fatal("expected validation failure for deferred spec without diagnostic")
		}
	})

	t.Run("deferred without capability key", func(t *testing.T) {
		m := DefaultRouteSchemaMatrix()
		mutated := m.specs[ProductAreaPullRequests]
		mutated.Status = SupportStatusDeferred
		mutated.Evidence = EvidenceClassDeferred
		mutated.Diagnostic = &UnsupportedDiagnostic{
			Code:          "unsupported_capability",
			CapabilityKey: "",
			Message:       "deferred",
		}
		m.specs[ProductAreaPullRequests] = mutated
		err := m.ValidateCoverage([]ProductArea{ProductAreaPullRequests})
		if err == nil {
			t.Fatal("expected validation failure for deferred spec without capability key")
		}
	})

	t.Run("deferred without message", func(t *testing.T) {
		m := DefaultRouteSchemaMatrix()
		mutated := m.specs[ProductAreaPullRequests]
		mutated.Status = SupportStatusDeferred
		mutated.Evidence = EvidenceClassDeferred
		mutated.Diagnostic = &UnsupportedDiagnostic{
			Code:          "unsupported_capability",
			CapabilityKey: "pr_read",
			Message:       "",
		}
		m.specs[ProductAreaPullRequests] = mutated
		err := m.ValidateCoverage([]ProductArea{ProductAreaPullRequests})
		if err == nil {
			t.Fatal("expected validation failure for deferred spec without message")
		}
	})

	t.Run("supported with diagnostic", func(t *testing.T) {
		m := makeMutated(func(s *SurfaceSpec) {
			s.Diagnostic = &UnsupportedDiagnostic{
				Code:          "unsupported_capability",
				CapabilityKey: "test",
				Message:       "test",
			}
		})
		err := m.ValidateCoverage([]ProductArea{ProductAreaIssues})
		if err == nil {
			t.Fatal("expected validation failure for supported spec with diagnostic")
		}
	})
}

func TestScenario005RouteSchemaMatrixPreflight(t *testing.T) {
	m := DefaultRouteSchemaMatrix()

	t.Run("issues supported", func(t *testing.T) {
		if err := m.Preflight(ProductAreaIssues); err != nil {
			t.Fatalf("Preflight(issues) should succeed: %v", err)
		}
	})

	t.Run("pull_requests supported", func(t *testing.T) {
		if err := m.Preflight(ProductAreaPullRequests); err != nil {
			t.Fatalf("Preflight(pull_requests) should succeed: %v", err)
		}
	})

	t.Run("comments deferred returns ErrUnsupportedCapability", func(t *testing.T) {
		err := m.Preflight(ProductAreaComments)
		var capErr ErrUnsupportedCapability
		if !errors.As(err, &capErr) {
			t.Fatalf("expected ErrUnsupportedCapability, got %T %v", err, err)
		}
		if capErr.CapabilityKey != "comments_read" {
			t.Fatalf("expected capability key comments_read, got %q", capErr.CapabilityKey)
		}
	})

	t.Run("unknown area", func(t *testing.T) {
		err := m.Preflight(ProductArea("nonexistent"))
		if err == nil {
			t.Fatal("Preflight(nonexistent) should fail")
		}
	})

	t.Run("unsupported area without diagnostic gets generated key", func(t *testing.T) {
		m2 := DefaultRouteSchemaMatrix()
		mutated := m2.specs[ProductAreaPullRequests]
		mutated.Status = SupportStatusUnsupported
		mutated.Diagnostic = nil
		m2.specs[ProductAreaPullRequests] = mutated
		err := m2.Preflight(ProductAreaPullRequests)
		var capErr ErrUnsupportedCapability
		if !errors.As(err, &capErr) {
			t.Fatalf("expected ErrUnsupportedCapability, got %T %v", err, err)
		}
		if !strings.Contains(capErr.CapabilityKey, "pull_requests") {
			t.Fatalf("expected generated key containing product area, got %q", capErr.CapabilityKey)
		}
	})
}

func TestScenario005RouteSchemaMatrixLiveProviderConstruction(t *testing.T) {
	t.Run("default matrix validates and constructs", func(t *testing.T) {
		_, err := newLiveProviderForTest(ProviderConfig{
			Mode:        ProviderModeLive,
			LiveAllowed: true,
			BaseURL:     "http://example.com",
			Token:       "test-token",
		})
		if err != nil {
			t.Fatalf("NewLiveProvider with default matrix failed: %v", err)
		}
	})

	t.Run("matrix with missing area fails construction", func(t *testing.T) {
		m := RouteSchemaMatrix{specs: map[ProductArea]SurfaceSpec{}}
		_, err := NewLiveProvider(ProviderConfig{
			Mode:        ProviderModeLive,
			LiveAllowed: true,
			BaseURL:     "http://example.com",
			Token:       "test-token",
		}, WithRouteSchemaMatrix(m))
		if err == nil {
			t.Fatal("expected construction failure for matrix with missing areas")
		}
		if !strings.Contains(err.Error(), "not declared") {
			t.Fatalf("expected 'not declared' in error: %v", err)
		}
	})

	t.Run("matrix with bad enum fails construction", func(t *testing.T) {
		m := DefaultRouteSchemaMatrix()
		mutated := m.specs[ProductAreaIssues]
		mutated.Status = "bad_status"
		m.specs[ProductAreaIssues] = mutated
		_, err := NewLiveProvider(ProviderConfig{
			Mode:        ProviderModeLive,
			LiveAllowed: true,
			BaseURL:     "http://example.com",
			Token:       "test-token",
		}, WithRouteSchemaMatrix(m))
		if err == nil {
			t.Fatal("expected construction failure for matrix with bad enum")
		}
	})
}

func TestScenario005RouteSchemaMatrixCommentsPreflightBlocksHTTP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("no HTTP request expected for deferred comments, got %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()

	p, err := NewLiveProvider(ProviderConfig{
		Mode:        ProviderModeLive,
		LiveAllowed: true,
		BaseURL:     server.URL,
		Token:       "test-token",
	})
	if err != nil {
		t.Fatalf("NewLiveProvider failed: %v", err)
	}

	_, err = p.ListIssueComments(context.Background(), IssueRequest{Owner: "o", Repo: "r", Number: 1})
	var capErr ErrUnsupportedCapability
	if !errors.As(err, &capErr) {
		t.Fatalf("expected ErrUnsupportedCapability, got %T %v", err, err)
	}
	if capErr.CapabilityKey != "comments_read" {
		t.Fatalf("expected comments_read capability key, got %q", capErr.CapabilityKey)
	}
}

func TestScenario011CreateIssueCommentPreflightBlocksHTTP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("no HTTP request expected for deferred comments write, got %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()

	p, err := NewLiveProvider(ProviderConfig{
		Mode:        ProviderModeLive,
		LiveAllowed: true,
		BaseURL:     server.URL,
		Token:       "test-token",
	})
	if err != nil {
		t.Fatalf("NewLiveProvider failed: %v", err)
	}

	_, err = p.CreateIssueComment(context.Background(), CreateIssueCommentRequest{Owner: "o", Repo: "r", Number: 1, Body: "test comment"}, WriteOptions{IdempotencyKey: "test-key"})
	var capErr ErrUnsupportedCapability
	if !errors.As(err, &capErr) {
		t.Fatalf("expected ErrUnsupportedCapability, got %T %v", err, err)
	}
	if capErr.CapabilityKey != "comments_read" {
		t.Fatalf("expected comments_read capability key, got %q", capErr.CapabilityKey)
	}
}

func TestScenario005RouteSchemaMatrixIssuesReachesHTTP(t *testing.T) {
	var hit bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		if r.URL.Path == "/api/v5/repos/example-owner/example-repo/issues" {
			fmt.Fprint(w, `[{"id":"ISSUE-1","number":1,"title":"test"}]`)
			return
		}
		t.Fatalf("unexpected path %s", r.URL.Path)
	}))
	defer server.Close()

	p, err := NewLiveProvider(ProviderConfig{
		Mode:        ProviderModeLive,
		LiveAllowed: true,
		BaseURL:     server.URL,
		Token:       "test-token",
	})
	if err != nil {
		t.Fatalf("NewLiveProvider failed: %v", err)
	}

	issues, err := p.ListIssues(context.Background(), IssueListRequest{Owner: "example-owner", Repo: "example-repo"})
	if err != nil {
		t.Fatalf("ListIssues failed: %v", err)
	}
	if !hit {
		t.Fatal("expected HTTP request for supported issues")
	}
	if len(issues.Items) != 1 || issues.Items[0].ID != "ISSUE-1" {
		t.Fatalf("unexpected issues: %+v", issues)
	}
}

func TestScenario005ErrUnsupportedCapabilityDiagnosticCode(t *testing.T) {
	e := ErrUnsupportedCapability{CapabilityKey: "test_key", Message: "test message"}
	if e.DiagnosticCode() != "unsupported_capability" {
		t.Fatalf("DiagnosticCode returned %q, want unsupported_capability", e.DiagnosticCode())
	}
	if !strings.Contains(e.Error(), "test_key") || !strings.Contains(e.Error(), "test message") {
		t.Fatalf("Error string missing fields: %s", e.Error())
	}
}

func TestScenario005IsUnsupportedCapability(t *testing.T) {
	e := ErrUnsupportedCapability{CapabilityKey: "test", Message: "test"}
	if !IsUnsupportedCapability(e) {
		t.Fatal("IsUnsupportedCapability should return true for ErrUnsupportedCapability")
	}
	var plain error = e
	if !IsUnsupportedCapability(plain) {
		t.Fatal("IsUnsupportedCapability should return true for wrapped ErrUnsupportedCapability")
	}
	if IsUnsupportedCapability(fmt.Errorf("other error")) {
		t.Fatal("IsUnsupportedCapability should return false for other errors")
	}
}

func newLiveProviderForTest(cfg ProviderConfig, opts ...LiveProviderOption) (Provider, error) {
	return NewLiveProvider(cfg, opts...)
}
