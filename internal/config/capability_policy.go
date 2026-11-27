package config

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"

	"gitcode-mcp/internal/capability"
	"gopkg.in/yaml.v3"
)

// policySource pins one policy for a process lifetime. Runtime configuration
// reloads and retained job config cannot replace its authority.
type policySource struct {
	Source
	policy              *capability.Policy
	machine, deployment string
}

func (OSSource) TrustedUserConfigDir() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", err
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(u.HomeDir, "Library", "Application Support"), nil
	case "windows":
		return filepath.Join(u.HomeDir, "AppData", "Roaming"), nil
	default:
		return filepath.Join(u.HomeDir, ".config"), nil
	}
}

func (OSSource) ReadPolicyFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, capability.MaxPolicyDocumentBytes+1))
}

func trustedPolicyPath(src Source) (string, error) {
	var dir string
	var err error
	if trusted, ok := src.(interface{ TrustedUserConfigDir() (string, error) }); ok {
		dir, err = trusted.TrustedUserConfigDir()
	} else {
		dir, err = src.UserConfigDir()
	}
	if err != nil || dir == "" {
		return "", capability.InvalidPolicy()
	}
	return filepath.Join(dir, "gitcode-mcp", "config.yaml"), nil
}

func policyBytes(src Source, path string, required bool) ([]byte, error) {
	var data []byte
	var err error
	if reader, ok := src.(interface{ ReadPolicyFile(string) ([]byte, error) }); ok {
		data, err = reader.ReadPolicyFile(path)
	} else {
		data, err = src.ReadFile(path)
	}
	if errors.Is(err, os.ErrNotExist) && !required {
		return nil, nil
	}
	if err != nil || len(data) > capability.MaxPolicyDocumentBytes {
		return nil, capability.InvalidPolicy()
	}
	return data, nil
}

// PrepareCapabilityPolicy must precede credential/cache/provider construction.
// Only the canonical user file and an explicit operator launch flag are roots.
func PrepareCapabilityPolicy(src Source, deployment, expected string) (Source, *capability.Policy, error) {
	if src == nil {
		src = OSSource{}
	}
	if deployment != "" {
		if !filepath.IsAbs(deployment) {
			var cwd string
			var err error
			if working, ok := src.(WorkingDirSource); ok {
				cwd, err = working.WorkingDir()
			} else {
				cwd, err = os.Getwd()
			}
			if err != nil || !filepath.IsAbs(cwd) {
				return nil, nil, capability.InvalidPolicy()
			}
			deployment = filepath.Join(cwd, deployment)
		}
		deployment = filepath.Clean(deployment)
	}
	if pinned, ok := src.(*policySource); ok {
		if deployment != "" && deployment != pinned.deployment {
			return nil, nil, capability.InvalidPolicy()
		}
		if expected != "" && pinned.policy.Fingerprint() != expected {
			return nil, nil, capability.PolicyError{Code: "capability_policy_mismatch"}
		}
		return pinned, pinned.policy, nil
	}
	machine, err := trustedPolicyPath(src)
	if err != nil {
		return nil, nil, err
	}
	layers := []capability.PolicyLayer{}
	readOnly := strings.EqualFold(strings.TrimSpace(src.Env(EnvMCPToolAccess)), "read")
	for _, item := range []struct {
		path, source string
		required     bool
	}{{machine, "machine", false}, {deployment, "deployment", true}} {
		if item.path == "" {
			continue
		}
		data, err := policyBytes(src, item.path, item.required)
		if err != nil {
			return nil, nil, err
		}
		spec, _, err := capability.ParsePolicyDocument(data)
		if err != nil {
			return nil, nil, err
		}
		layers = append(layers, capability.PolicyLayer{Specification: spec, Source: item.source})
		readOnly = readOnly || explicitPolicyRead(data)
	}
	// Lower-trust environment config can tighten a read ceiling, never define
	// or replace a policy. Repository files are checked when discovered/read.
	for _, key := range []string{EnvMCPConfigPath, EnvConfigPath} {
		path := src.Env(key)
		if path == "" || path == machine || path == deployment {
			continue
		}
		// This may be the not-yet-created target of config/project init.
		// Missing lower-trust runtime input is diagnosed by that command, not
		// mistaken for a missing trusted deployment authority.
		data, err := policyBytes(src, path, false)
		if err != nil {
			return nil, nil, err
		}
		_, present, err := capability.ParsePolicyDocument(data)
		if present || err != nil && bytes.Contains(data, []byte("capabilities")) {
			return nil, nil, capability.InvalidPolicy()
		}
		readOnly = readOnly || explicitPolicyRead(data)
	}
	policy, err := capability.CompilePolicy(readOnly, layers...)
	if err != nil {
		return nil, nil, err
	}
	if expected != "" && policy.Fingerprint() != expected {
		return nil, nil, capability.PolicyError{Code: "capability_policy_mismatch"}
	}
	pinned := &policySource{Source: src, policy: policy, machine: machine, deployment: deployment}
	return pinned, policy, nil
}

func explicitPolicyRead(data []byte) bool {
	var doc yaml.Node
	if yaml.Unmarshal(data, &doc) != nil || len(doc.Content) != 1 {
		return false
	}
	lookup := func(n *yaml.Node, key string) *yaml.Node {
		if n == nil || n.Kind != yaml.MappingNode {
			return nil
		}
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value == key {
				return n.Content[i+1]
			}
		}
		return nil
	}
	root := doc.Content[0]
	if n := lookup(root, "mcp_tool_access"); n != nil && strings.EqualFold(strings.TrimSpace(n.Value), "read") {
		return true
	}
	n := lookup(lookup(lookup(root, "mcp"), "tools"), "access")
	return n != nil && strings.EqualFold(strings.TrimSpace(n.Value), "read")
}

func PolicyFromSource(src Source) *capability.Policy {
	if pinned, ok := src.(*policySource); ok {
		return pinned.policy
	}
	return nil
}
func PolicyDeployment(src Source) string {
	if pinned, ok := src.(*policySource); ok {
		return pinned.deployment
	}
	return ""
}

func (s *policySource) ReadFile(path string) ([]byte, error) {
	data, err := s.Source.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if path != s.machine && path != s.deployment && (filepath.Ext(path) == ".yaml" || filepath.Ext(path) == ".yml" || filepath.Ext(path) == ".json") {
		_, present, err := capability.ParsePolicyDocument(data)
		if present || err != nil && bytes.Contains(data, []byte("capabilities")) {
			return nil, capability.InvalidPolicy()
		}
		// A newly discovered explicit read restriction cannot widen the pinned
		// policy; reject mixed-policy startup rather than mutate a live snapshot.
		if s.policy != nil && explicitPolicyRead(data) {
			for _, d := range s.policy.View().Decisions {
				if d.Allowed {
					for _, c := range capability.Capabilities() {
						if c.ID == d.ID && c.Safety != capability.SafetyReadOnly {
							return nil, capability.PolicyError{Code: "capability_policy_mismatch"}
						}
					}
				}
			}
		}
	}
	return data, nil
}

func (s *policySource) WorkingDir() (string, error) {
	if source, ok := s.Source.(WorkingDirSource); ok {
		return source.WorkingDir()
	}
	return "", os.ErrNotExist
}
func (s *policySource) Stat(path string) (os.FileInfo, error) {
	if source, ok := s.Source.(StatSource); ok {
		return source.Stat(path)
	}
	return nil, os.ErrNotExist
}
