//go:build (!darwin && !linux) || (darwin && osusergo)

package localauthority

func resolveAccountHome() (string, error) { return "", Error{DurabilityUnavailable} }
