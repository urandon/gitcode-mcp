//go:build !darwin && !linux

package localauthority

import "os"

const indexJobsDirectory = "runtime-index-jobs"

func indexJobFileName(string) bool { return false }
func openIndexJobNamespace(*os.File, bool) (*os.File, bool, error) {
	return nil, false, Error{DurabilityUnavailable}
}
func openIndexJobFile(*os.File, string, bool) (*os.File, bool, error) {
	return nil, false, Error{DurabilityUnavailable}
}
func scanIndexJobNamespace(*os.File) (*os.File, error) { return nil, Error{DurabilityUnavailable} }
func publishIndexJobFile(*os.File, string) error       { return Error{DurabilityUnavailable} }
