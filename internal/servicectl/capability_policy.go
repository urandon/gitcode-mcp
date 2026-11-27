package servicectl

import (
	"context"
	"time"

	"gitcode-mcp/internal/capability"
	"gitcode-mcp/internal/config"
)

type policyHandshake struct {
	Schema      int    `json:"schema"`
	Fingerprint string `json:"fingerprint"`
}

func (m Manager) instancePolicy() *capability.Policy { return config.PolicyFromSource(m.Source) }
func (m Manager) requirePolicy(surface, operation string) error {
	return m.instancePolicy().Require(surface, operation)
}

func (m *Manager) prepareLifecyclePolicy() error {
	var err error
	m.Source, _, err = config.PrepareCapabilityPolicy(m.Source, "", "")
	if err != nil {
		return err
	}
	return m.instancePolicy().Check("service_lifecycle")
}

func (m Manager) requireMaintenanceChildren(p MaintenancePolicy) error {
	if p.SyncEnabled {
		if err := m.instancePolicy().Check("sync_live"); err != nil {
			return err
		}
	}
	if p.RAGEnabled {
		if err := m.instancePolicy().Check("rag_index"); err != nil {
			return err
		}
	}
	return nil
}

func (c *RPCClient) checkPolicyPeer(ctx context.Context) error {
	if c.Policy == nil {
		return nil
	}
	var handshake policyHandshake
	// A copy avoids bypass flags shared by concurrent RPC calls.
	peer := &RPCClient{Network: c.Network, Address: c.Address, SocketPath: c.SocketPath}
	probe, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := peer.Call(probe, "Policy.Handshake", nil, &handshake); err != nil {
		return capability.PolicyError{Code: "capability_policy_unavailable"}
	}
	if handshake.Schema != 1 {
		return capability.PolicyError{Code: "capability_policy_unavailable"}
	}
	if handshake.Fingerprint != c.Policy.Fingerprint() {
		return capability.PolicyError{Code: "capability_policy_mismatch"}
	}
	return nil
}

func (m *JobManager) blockPolicyJob(id string, err error) {
	m.updateJob(id, func(job *Job, now time.Time) {
		job.Status = JobStatusFailed
		job.UpdatedAt = now
		job.FinishedAt = &now
		job.ErrorClass = "capability_policy_denied"
		job.Error = err.Error()
	})
}
