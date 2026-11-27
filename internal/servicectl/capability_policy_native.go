package servicectl

import (
	"encoding/xml"
	"errors"
	"io"
	"os"
	"strings"

	"gitcode-mcp/internal/capability"
	"gitcode-mcp/internal/config"
)

func (m Manager) policyInstallContent(kind, binary string, paths Paths) (string, error) {
	content := installFileContent(kind, binary, paths)
	p := m.instancePolicy()
	if p == nil {
		if old, err := os.ReadFile(paths.InstallPath); err == nil && strings.Contains(string(old), "--policy-fingerprint") {
			return "", capability.PolicyError{Code: "capability_policy_mismatch"}
		}
		return content, nil
	}
	args := []string{"--policy-fingerprint", p.Fingerprint()}
	if path := config.PolicyDeployment(m.Source); path != "" {
		args = append(args, "--deployment-config", path)
	}
	switch kind {
	case "launchagent":
		extra := ""
		for _, arg := range args {
			extra += "<string>" + escapeXMLText(arg) + "</string>"
		}
		content = strings.Replace(content, "<string>service</string><string>run</string>", extra+"<string>service</string><string>run</string>", 1)
	case "systemd-user":
		extra := ""
		for _, arg := range args {
			extra += " " + quoteSystemdArgument(arg)
		}
		content = strings.Replace(content, "ExecStart="+quoteSystemdArgument(binary)+" service run", "ExecStart="+quoteSystemdArgument(binary)+extra+" service run", 1)
	default:
		return "", capability.PolicyError{Code: "capability_policy_unavailable"}
	}
	return content, nil
}

func (m Manager) validateInstalledPolicy(paths Paths) error {
	if m.instancePolicy() == nil {
		if data, err := os.ReadFile(paths.InstallPath); err == nil && strings.Contains(string(data), "--policy-fingerprint") {
			return capability.PolicyError{Code: "capability_policy_mismatch"}
		}
		return nil
	}
	data, err := os.ReadFile(paths.InstallPath)
	if err != nil {
		return capability.PolicyError{Code: "capability_policy_unavailable"}
	}
	args, err := policyDefinitionArguments(paths.InstallKind, string(data))
	if err != nil {
		return capability.PolicyError{Code: "capability_policy_unavailable"}
	}
	if len(args) == 0 {
		return capability.PolicyError{Code: "capability_policy_unavailable"}
	}
	_, deployment, expected, err := config.ExtractPolicyLaunchFlags(args[1:])
	if err != nil || expected != m.instancePolicy().Fingerprint() || deployment != config.PolicyDeployment(m.Source) {
		return capability.PolicyError{Code: "capability_policy_mismatch"}
	}
	return nil
}

func policyDefinitionArguments(kind, data string) ([]string, error) {
	if kind == "launchagent" {
		decoder := xml.NewDecoder(strings.NewReader(data))
		key := ""
		inside := false
		args := []string{}
		for {
			tok, err := decoder.Token()
			if err != nil {
				if err == io.EOF {
					break
				}
				return nil, err
			}
			switch t := tok.(type) {
			case xml.StartElement:
				if t.Name.Local == "key" {
					if err := decoder.DecodeElement(&key, &t); err != nil {
						return nil, err
					}
				} else if key == "ProgramArguments" && t.Name.Local == "array" {
					inside = true
					key = ""
				} else if inside && t.Name.Local == "string" {
					var arg string
					if err := decoder.DecodeElement(&arg, &t); err != nil {
						return nil, err
					}
					args = append(args, arg)
				}
			case xml.EndElement:
				if inside && t.Name.Local == "array" {
					return args, nil
				}
			}
		}
	}
	if kind == "systemd-user" {
		for _, line := range strings.Split(data, "\n") {
			if strings.HasPrefix(line, "ExecStart=") {
				args, err := splitPolicyArguments(strings.TrimPrefix(line, "ExecStart="))
				if err != nil {
					return nil, err
				}
				for i, arg := range args {
					var decoded strings.Builder
					for j := 0; j < len(arg); j++ {
						if arg[j] == '$' || arg[j] == '%' {
							if j+1 == len(arg) || arg[j+1] != arg[j] {
								return nil, errors.New("unmanaged service expansion")
							}
							j++
						}
						decoded.WriteByte(arg[j])
					}
					args[i] = decoded.String()
				}
				return args, nil
			}
		}
	}
	return nil, errors.New("service arguments unavailable")
}

// Generated systemd argv uses quoted arguments, never shell interpolation.
func splitPolicyArguments(value string) ([]string, error) {
	var args []string
	var token strings.Builder
	quoted, escape, started := false, false, false
	for _, r := range value {
		if escape {
			token.WriteRune(r)
			escape = false
			started = true
			continue
		}
		if r == '\\' {
			escape = true
			started = true
			continue
		}
		if r == '"' {
			quoted = !quoted
			started = true
			continue
		}
		if (r == ' ' || r == '\t') && !quoted {
			if started {
				args = append(args, token.String())
				token.Reset()
				started = false
			}
			continue
		}
		token.WriteRune(r)
		started = true
	}
	if quoted || escape {
		return nil, errors.New("invalid service arguments")
	}
	if started {
		args = append(args, token.String())
	}
	return args, nil
}
