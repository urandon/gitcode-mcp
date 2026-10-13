package localauthority

import (
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

const accountMetadataBytes = 1 << 20

// Only explicit trusted setup invokes account resolution. Strict runtime
// consumes PreparedRegistry and never calls this or os/user/environment APIs.
func machineAccountHome() (string, error) {
	home, err := resolveAccountHome()
	if err != nil || !validAccountHome(home) {
		return "", Error{DurabilityUnavailable}
	}
	physical, err := filepath.EvalSymlinks(home)
	if err != nil || accountHomeOwner(physical) != nil {
		return "", Error{DurabilityUnavailable}
	}
	return physical, nil
}

func validAccountHome(home string) bool {
	if !utf8.ValidString(home) || len(home) == 0 || len(home) > 4096 || !filepath.IsAbs(home) || filepath.Clean(home) != home {
		return false
	}
	for _, r := range home {
		if r < 32 || r == 127 {
			return false
		}
	}
	return true
}

// Linux local account metadata has no NSS/USER/HOME fallback. Missing,
// duplicate or over-budget UID data refuses instead of choosing a new root.
func homeFromPasswd(reader io.Reader, uid int) (string, error) {
	if uid < 0 {
		return "", Error{DurabilityUnavailable}
	}
	data, err := io.ReadAll(io.LimitReader(reader, accountMetadataBytes+1))
	if err != nil || len(data) > accountMetadataBytes || !utf8.Valid(data) {
		return "", Error{DurabilityUnavailable}
	}
	home := ""
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) != 7 {
			return "", Error{DurabilityUnavailable}
		}
		id, err := strconv.ParseUint(fields[2], 10, 32)
		if err != nil {
			return "", Error{DurabilityUnavailable}
		}
		if id != uint64(uid) {
			continue
		}
		if home != "" || !validAccountHome(fields[5]) {
			return "", Error{DurabilityUnavailable}
		}
		home = fields[5]
	}
	if home == "" {
		return "", Error{DurabilityUnavailable}
	}
	return home, nil
}
