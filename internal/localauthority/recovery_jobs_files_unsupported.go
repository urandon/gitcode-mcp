//go:build !darwin && !linux

package localauthority

import "os"

const recoveryJobsDirectory = "runtime-jobs"

func recoveryJobFileName(string) bool { return false }
func openRecoveryJobNamespace(*os.File, bool) (*os.File, bool, error) {
	return nil, false, Error{DurabilityUnavailable}
}
func openRecoveryJobFile(*os.File, string, bool) (*os.File, bool, error) {
	return nil, false, Error{DurabilityUnavailable}
}
func scanRecoveryJobDirectory(*os.File) (*os.File, error) { return nil, Error{DurabilityUnavailable} }
func publishRecoveryJobFile(*os.File, string) error       { return Error{DurabilityUnavailable} }
