//go:build !darwin && !linux

package localauthority

import "os"

func openQualifiedDirectory(string) (*os.File, error)       { return nil, Error{DurabilityUnavailable} }
func qualifyDirectoryFD(*os.File) error                     { return Error{DurabilityUnavailable} }
func duplicateRegistryDirectory(*os.File) (*os.File, error) { return nil, Error{DurabilityUnavailable} }
func registryExists(*os.File, string) (bool, error)         { return false, Error{DurabilityUnavailable} }
func registryOpen(*os.File, string, bool, bool, uint64) (*os.File, error) {
	return nil, Error{DurabilityUnavailable}
}
func registryLink(*os.File, string, string) error { return Error{DurabilityUnavailable} }
func registryUnlink(*os.File, string) error       { return Error{DurabilityUnavailable} }
func syncRegistryDirectory(*os.File) error        { return Error{DurabilityUnavailable} }
