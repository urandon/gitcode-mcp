package config

import (
	"gitcode-mcp/internal/capability"
	"strings"
)

// ExtractPolicyLaunchFlags is shared by the binary and direct CLI entrypoint.
// No environment variable or RPC parameter selects an instance/profile.
func ExtractPolicyLaunchFlags(args []string) ([]string, string, string, error) {
	clean := []string{}
	values := map[string]string{}
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			clean = append(clean, args[i:]...)
			break
		}
		key, value, hasValue := strings.Cut(args[i], "=")
		if key != "--deployment-config" && key != "--policy-fingerprint" {
			// These are trusted launch flags, not command arguments. Never
			// reinterpret a body/query or a flag value as policy selection.
			clean = append(clean, args[i:]...)
			break
		}
		if _, ok := values[key]; ok {
			return nil, "", "", capability.InvalidPolicy()
		}
		if !hasValue {
			if i+1 >= len(args) {
				return nil, "", "", capability.InvalidPolicy()
			}
			i++
			value = args[i]
		}
		if value == "" || strings.HasPrefix(value, "--") {
			return nil, "", "", capability.InvalidPolicy()
		}
		values[key] = value
	}
	return clean, values["--deployment-config"], values["--policy-fingerprint"], nil
}
