//go:build darwin && !osusergo

package localauthority

import (
	"os"
	"os/user"
	"strconv"
)

// Darwin's qualified !osusergo implementation uses native getpwuid_r for both
// cgo and non-cgo builds; lookup_stubs' environment fallback is not compiled.
// This potentially NSS-backed call is explicit setup, never strict startup.
func resolveAccountHome() (string, error) {
	uid := strconv.Itoa(os.Geteuid())
	u, err := user.LookupId(uid)
	if err != nil || u.Uid != uid {
		return "", Error{DurabilityUnavailable}
	}
	return u.HomeDir, nil
}
