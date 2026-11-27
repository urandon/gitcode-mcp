package adminhttp

import (
	"gitcode-mcp/internal/capability"
	"net/http"
	"strings"
	"time"
)

func CanObservePolicySnapshot(p *capability.Policy) bool {
	return p.Check("repo_status", "cache_status", "service_jobs", "maintenance_status", "repository_docs_status", "feedback_status") == nil
}

func (c *Controller) policySnapshotAllowed() bool {
	if len(c.cfg.PolicySnapshotRequirements) > 0 {
		return c.cfg.Policy.Check(c.cfg.PolicySnapshotRequirements...) == nil
	}
	return CanObservePolicySnapshot(c.cfg.Policy)
}

func PolicyOnlySnapshot(p *capability.Policy, service ServiceObservation) ObservationSnapshot {
	v := p.View()
	snapshot := ObservationSnapshot{Service: service, Policy: &v, PolicyRestricted: true}
	for _, item := range capability.Capabilities() {
		snapshot.Capabilities = append(snapshot.Capabilities, CapabilityObservation{ID: item.ID, Description: item.Description, UIEnabled: false, UIReason: "Operator data is withheld by the trusted instance policy."})
	}
	return FinalizeSnapshot(snapshot, time.Now().UTC())
}

func (c *Controller) authorizePolicyRoute(w http.ResponseWriter, r *http.Request, pattern string) bool {
	if c.cfg.Policy == nil {
		return true
	}
	switch pattern {
	case "POST /api/admin/v1/session", "GET /api/admin/v1/session", "DELETE /api/admin/v1/session":
		return true
	case "GET /api/admin/v1/snapshot", "GET /api/admin/v1/capabilities", "GET /api/admin/v1/policy":
		return true
	case "/":
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			return true
		}
	}
	if _, ok := c.authenticate(r); !ok {
		return true
	} // the existing handler returns 401
	if err := c.cfg.Policy.Require("admin_route", pattern); err != nil {
		code := "capability_policy_denied"
		if typed, ok := err.(capability.PolicyError); ok {
			code = typed.Code
		}
		writeAPIError(w, http.StatusForbidden, code, "The trusted instance policy denies this operation.", "Ask the operator to review its grants.")
		return false
	}
	return true
}

func (c *Controller) getPolicy(w http.ResponseWriter, r *http.Request) {
	if _, ok := c.authenticate(r); !ok {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized", "Admin session required.", "Run gitcode-mcp admin open.")
		return
	}
	writeJSON(w, http.StatusOK, c.cfg.Policy.View())
}
