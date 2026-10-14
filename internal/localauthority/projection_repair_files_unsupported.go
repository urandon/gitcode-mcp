//go:build !darwin && !linux

package localauthority

import (
	"gitcode-mcp/internal/authority"
	"os"
)

const repairDirectory = "runtime-projection-repair"

func repairMetadataName(string) bool             { return false }
func repairQuarantineName(authority.UUID) string { return "" }
func repairQuarantineID(string) authority.UUID   { return "" }
func openRepairSubdirectory(*os.File, string, bool) (*os.File, bool, error) {
	return nil, false, Error{DurabilityUnavailable}
}
func openRepairMetadata(*os.File, string, bool) (*os.File, bool, error) {
	return nil, false, Error{DurabilityUnavailable}
}
func publishRepairMetadata(*os.File, string) error { return Error{DurabilityUnavailable} }
func moveProjectionToQuarantine(*os.File, *os.File, authority.UUID) error {
	return Error{DurabilityUnavailable}
}
