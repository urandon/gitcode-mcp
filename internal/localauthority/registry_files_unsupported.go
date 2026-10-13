//go:build !darwin && !linux

package localauthority

import "os"

func openPrivateDirectory(string) (*os.File, error) { return nil, Error{DurabilityUnavailable} }
func openExistingOwnerAt(*os.File, string) (*os.File, error) {
	return nil, Error{DurabilityUnavailable}
}
func accountHomeOwner(string) error { return Error{DurabilityUnavailable} }
