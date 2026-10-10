//go:build !darwin && !linux

package observability

type LegacyFile struct {
	Stream   string `json:"stream"`
	Present  bool   `json:"present"`
	Identity string `json:"identity,omitempty"`
	Bytes    int64  `json:"bytes"`
}
type LegacySample struct {
	Cursor  LegacyCursor `json:"cursor"`
	HasMore bool         `json:"has_more"`
	Codes   []Code       `json:"codes"`
	Bytes   int          `json:"bytes"`
	Partial bool         `json:"partial"`
	State   string       `json:"state"`
}
type LegacyCursor struct {
	Identity    string `json:"identity,omitempty"`
	Offset      int64  `json:"offset"`
	SkipPartial bool   `json:"skip_partial"`
}

func ReadLegacyFrom(string, string, LegacyCursor) (LegacySample, error) {
	return LegacySample{State: "unsupported"}, errPlatform
}

func LegacyInventory(string) ([]LegacyFile, error) { return nil, errPlatform }
func ReadLegacy(string, string) (LegacySample, error) {
	return LegacySample{State: "unsupported"}, errPlatform
}
func RemoveLegacy(string, []LegacyFile) error { return errPlatform }
