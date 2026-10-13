//go:build !darwin && !linux

package localauthority

import "os"

const projectionDirectory = "runtime-projection"

func projectionFileName(string) bool { return false }
func openProjectionNamespace(*os.File, bool) (*os.File, bool, error) {
	return nil, false, Error{DurabilityUnavailable}
}
func openProjectionFile(*os.File, string, bool) (*os.File, bool, error) {
	return nil, false, Error{DurabilityUnavailable}
}
func scanProjectionNamespace(*os.File) (*os.File, error) { return nil, Error{DurabilityUnavailable} }
func publishProjectionFile(*os.File, string) error       { return Error{DurabilityUnavailable} }
