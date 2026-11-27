package service

import "gitcode-mcp/internal/capability"

// BindCapabilityPolicy is trusted construction, not a command/RPC. A managed
// instance cannot be rebound to a different or permissive policy while serving.
func (s *Service) BindCapabilityPolicy(p *capability.Policy) error {
	if s.policy != nil && s.policy.Fingerprint() != p.Fingerprint() {
		return capability.PolicyError{Code: "capability_policy_mismatch"}
	}
	s.policy = p
	return nil
}

func (s *Service) authorizePolicy(operation string) error {
	return s.policy.Require("service", operation)
}
func (s *LocalProjects) authorizePolicy(operation string) error {
	ids := map[string]string{"Status": "local_project_status", "Get": "local_project_get", "List": "local_project_list", "History": "local_project_history", "Mutate": "local_project_mutate", "PlanMutation": "local_project_mutate", "ApplyMutationPlan": "local_project_mutate", "Search": "local_project_search", "Index": "local_project_index", "PlanIndex": "local_project_index", "ApplyIndexPlan": "local_project_index"}
	return s.searchConfig.CapabilityPolicy.Check(ids[operation])
}
func zeroPolicyValue[T any]() T { var value T; return value }
