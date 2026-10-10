package servicectl

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/observability"
)

type ObservationUpgradePlan struct {
	PlanID   string                     `json:"plan_id"`
	State    string                     `json:"state"`
	Effects  []string                   `json:"effects"`
	Legacy   []observability.LegacyFile `json:"legacy"`
	Recovery string                     `json:"recovery"`
}

func observationUpgradeError(code string) error {
	return RPCDomainError{Code: "observation_" + code, Message: "Observation upgrade requires a fresh service upgrade-observation plan and explicit --yes --plan-id confirmation (" + code + ")."}
}

func boundedDefinition(kind, path string) bool {
	b, err := observability.ReadDefinition(path)
	if err != nil {
		return false
	}
	switch kind {
	case "systemd-user":
		out, stderr, marker := "", "", false
		section := ""
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "[") {
				section = line
				continue
			}
			if section != "[Service]" {
				continue
			}
			if strings.HasPrefix(line, "StandardOutput=") {
				if out != "" {
					return false
				}
				out = strings.TrimPrefix(line, "StandardOutput=")
			}
			if strings.HasPrefix(line, "StandardError=") {
				if stderr != "" {
					return false
				}
				stderr = strings.TrimPrefix(line, "StandardError=")
			}
			if line == `Environment="GITCODE_MCP_OBSERVATION_OUTPUT=bounded-v1"` {
				marker = true
			}
		}
		return out == "null" && stderr == "null" && marker
	case "launchagent":
		decoder := xml.NewDecoder(bytes.NewReader(b))
		depth, roots := 0, 0
		for {
			token, err := decoder.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				return false
			}
			switch token.(type) {
			case xml.StartElement:
				if depth == 0 {
					roots++
				}
				depth++
				if depth > 16 || roots > 1 {
					return false
				}
			case xml.EndElement:
				depth--
			}
		}
		var root observationPlistNode
		if xml.Unmarshal(b, &root) != nil || root.XMLName.Local != "plist" || len(root.Children) != 1 {
			return false
		}
		values, ok := observationPlistDict(root.Children[0])
		if !ok {
			return false
		}
		env, ok := observationPlistDict(values["EnvironmentVariables"])
		if !ok {
			return false
		}
		out, stderr, marker := values["StandardOutPath"], values["StandardErrorPath"], env["GITCODE_MCP_OBSERVATION_OUTPUT"]
		return out.XMLName.Local == "string" && stderr.XMLName.Local == "string" && marker.XMLName.Local == "string" && out.Text == "/dev/null" && stderr.Text == "/dev/null" && marker.Text == "bounded-v1"
	}
	return false
}

func (m Manager) managedObservationOutput(paths Paths) bool {
	src := m.Source
	if src == nil {
		src = config.OSSource{}
	}
	// A disk definition can differ from the one launchd/systemd loaded.
	return src.Env("GITCODE_MCP_OBSERVATION_OUTPUT") == "bounded-v1" && boundedDefinition(paths.InstallKind, paths.InstallPath)
}

func (m Manager) stopObservationOwner(ctx context.Context, paths Paths) error {
	if err := m.runStopCommand(ctx, paths); err == nil {
		return nil
	}
	if paths.InstallKind == "launchagent" {
		target := fmt.Sprintf("gui/%d/com.gitcode.gitcode-mcp", os.Getuid())
		output, err := m.runCommandOutput(ctx, "launchctl", "print", target)
		var exited *exec.ExitError
		// Captured local launchctl compatibility: specific service absence has
		// exit113 AND the fixed service-name diagnostic. Generic errors are unknown.
		if len(output) <= 4096 && errors.As(err, &exited) && exited.ExitCode() == 113 && strings.Contains(output, `Could not find service "com.gitcode.gitcode-mcp"`) {
			return nil
		}
	}
	return observationUpgradeError("quiesce_failed")
}

type observationPlistNode struct {
	XMLName  xml.Name
	Text     string                 `xml:",chardata"`
	Children []observationPlistNode `xml:",any"`
}

func observationPlistDict(node observationPlistNode) (map[string]observationPlistNode, bool) {
	if node.XMLName.Local != "dict" || len(node.Children)%2 != 0 {
		return nil, false
	}
	out := map[string]observationPlistNode{}
	for i := 0; i < len(node.Children); i += 2 {
		k, v := node.Children[i], node.Children[i+1]
		if k.XMLName.Local != "key" {
			return nil, false
		}
		if _, duplicate := out[k.Text]; duplicate {
			return nil, false
		}
		out[k.Text] = v
	}
	return out, true
}

// InstallForCacheMigration changes the compatible binary only after the old
// owner was quiesced. A known legacy definition retains its old output routing;
// cache migration is not consent to delete logs or change output ownership.
func (m Manager) InstallForCacheMigration() (Status, error) {
	paths, err := m.ResolvePaths()
	if err != nil {
		return Status{}, err
	}
	status, err := m.Status()
	if err != nil {
		return Status{}, err
	}
	if status.PIDAlive || status.SocketPresent {
		return Status{}, observationUpgradeError("owner_still_active")
	}
	if boundedDefinition(paths.InstallKind, paths.InstallPath) {
		return m.Install(true)
	}
	old, err := observability.ReadDefinition(paths.InstallPath)
	if err != nil {
		return Status{}, observationUpgradeError("definition_unsafe_or_absent")
	}
	var oldBinary string
	switch paths.InstallKind {
	case "launchagent":
		oldBinary, err = launchAgentProgram(old)
	case "systemd-user":
		oldBinary, err = systemdProgram(old)
	default:
		return Status{}, observationUpgradeError("platform_unsupported")
	}
	if err != nil || string(old) != legacyInstallFileContent(paths.InstallKind, oldBinary, paths) {
		return Status{}, observationUpgradeError("legacy_definition_unknown")
	}
	binary, err := resolveInstallExecutable(m.BinaryPath)
	if err != nil {
		return Status{}, observationUpgradeError("executable_unavailable")
	}
	if err := observability.WriteDefinition(paths.InstallPath, []byte(legacyInstallFileContent(paths.InstallKind, binary, paths))); err != nil {
		return Status{}, observationUpgradeError("replacement_incomplete")
	}
	return m.Status()
}
func legacyInstallFileContent(kind, binary string, paths Paths) string {
	content := installFileContent(kind, binary, paths)
	if kind == "launchagent" {
		content = strings.Replace(content, "<key>GITCODE_MCP_OBSERVATION_OUTPUT</key><string>bounded-v1</string>", "", 1)
		content = strings.Replace(content, "<key>StandardOutPath</key><string>/dev/null</string>", "<key>StandardOutPath</key><string>"+escapeXMLText(paths.LogDir+"/service.out.log")+"</string>", 1)
		content = strings.Replace(content, "<key>StandardErrorPath</key><string>/dev/null</string>", "<key>StandardErrorPath</key><string>"+escapeXMLText(paths.LogDir+"/service.err.log")+"</string>", 1)
	} else {
		content = strings.Replace(content, "Environment=\"GITCODE_MCP_OBSERVATION_OUTPUT=bounded-v1\"\n", "", 1)
		content = strings.Replace(content, "StandardOutput=null\nStandardError=null\n", "", 1)
	}
	return content
}

// PlanObservationUpgrade is a local read-only effect ledger. No platform,
// output cleanup, definition replacement, or provider action happens here.
func (m Manager) PlanObservationUpgrade() (ObservationUpgradePlan, error) {
	paths, err := m.ResolvePaths()
	if err != nil {
		return ObservationUpgradePlan{}, observationUpgradeError("paths_unavailable")
	}
	if paths.InstallKind != "launchagent" && paths.InstallKind != "systemd-user" {
		return ObservationUpgradePlan{}, observationUpgradeError("platform_unsupported")
	}
	binary, err := resolveInstallExecutable(m.BinaryPath)
	if err != nil {
		return ObservationUpgradePlan{}, observationUpgradeError("executable_unavailable")
	}
	definition, err := observability.ReadDefinition(paths.InstallPath)
	if err != nil {
		return ObservationUpgradePlan{}, observationUpgradeError("definition_unsafe_or_absent")
	}
	legacy, err := observability.LegacyInventory(paths.LogDir)
	if err != nil {
		return ObservationUpgradePlan{}, observationUpgradeError("legacy_unsafe")
	}
	proposed := installFileContent(paths.InstallKind, binary, paths)
	f, err := os.Open(binary)
	if err != nil {
		return ObservationUpgradePlan{}, observationUpgradeError("executable_unavailable")
	}
	hash := sha256.New()
	bytesRead, hashErr := io.Copy(hash, io.LimitReader(f, 128<<20+1))
	f.Close()
	if hashErr != nil || bytesRead > 128<<20 {
		return ObservationUpgradePlan{}, observationUpgradeError("executable_unavailable")
	}
	// Byte counts are observations: the confirmed effect removes the entire
	// fixed streams after quiescence. Pin identity, not active append position.
	identities := make([]observability.LegacyFile, len(legacy))
	copy(identities, legacy)
	for i := range identities {
		identities[i].Bytes = 0
	}
	input, _ := json.Marshal(struct {
		Old, New   string
		Executable string
		Legacy     []observability.LegacyFile
	}{string(definition), proposed, hex.EncodeToString(hash.Sum(nil)), identities})
	sum := sha256.Sum256(input)
	return ObservationUpgradePlan{PlanID: "observation-upgrade-" + hex.EncodeToString(sum[:16]), State: "confirmation_required", Legacy: legacy,
		Effects:  []string{"stop and prove the installed platform owner is quiescent", "remove only the two validated fixed legacy output files", "replace the installed definition with null unmanaged stdout/stderr", "restart the installed service and check readiness"},
		Recovery: "If interrupted, render a new plan for remaining local effects; readiness stays migration_required until cleanup and replacement finish."}, nil
}
func (m Manager) ApplyObservationUpgrade(ctx context.Context, planID string) (ObservationUpgradePlan, error) {
	plan, err := m.PlanObservationUpgrade()
	if err != nil {
		return plan, err
	}
	if planID == "" || plan.PlanID != planID {
		return plan, observationUpgradeError("stale_plan")
	}
	paths, _ := m.ResolvePaths()
	// Generic inspection failure is unknown. Require a successful stop or
	// positively classified already-unloaded owner before legacy cleanup.
	if err := m.stopObservationOwner(ctx, paths); err != nil {
		return plan, observationUpgradeError("quiesce_failed")
	}
	if _, err := m.waitForStopped(ctx, paths); err != nil {
		return plan, observationUpgradeError("owner_still_active")
	}
	fresh, err := m.PlanObservationUpgrade()
	if err != nil || fresh.PlanID != planID {
		return plan, observationUpgradeError("stale_plan")
	}
	if err := observability.RemoveLegacy(paths.LogDir, fresh.Legacy); err != nil {
		return plan, observationUpgradeError("cleanup_incomplete")
	}
	binary, err := resolveInstallExecutable(m.BinaryPath)
	if err != nil {
		return plan, observationUpgradeError("executable_unavailable")
	}
	if err := observability.WriteDefinition(paths.InstallPath, []byte(installFileContent(paths.InstallKind, binary, paths))); err != nil {
		return plan, observationUpgradeError("replacement_incomplete")
	}
	if _, err := m.Start(ctx); err != nil {
		return plan, observationUpgradeError("restart_incomplete")
	}
	plan.State = "applied"
	return plan, nil
}
