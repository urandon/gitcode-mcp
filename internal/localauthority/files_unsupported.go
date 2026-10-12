//go:build !darwin && !linux

package localauthority

import "os"

func directoryOwner(string) error { return nil } // Inspection only; writers refuse below.

func physicalIdentity(*os.File) (string, error) { return "", Error{DurabilityUnavailable} }

// No unqualified fallback lease or persistence acknowledgement.
func openPrivateFile(string) (*os.File, error)        { return nil, Error{DurabilityUnavailable} }
func openInitializationFile(string) (*os.File, error) { return nil, Error{DurabilityUnavailable} }
func openExistingFile(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, Error{Corrupt}
	}
	return os.Open(path)
}
func lockExclusive(*os.File) error   { return Error{DurabilityUnavailable} }
func qualifyFilesystem(string) error { return Error{DurabilityUnavailable} }
func syncFile(*os.File) error        { return Error{DurabilityUnavailable} }
