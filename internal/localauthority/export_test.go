package localauthority

// NewFixtureRegistry exists only in augmented test builds so external native
// transport tests can use an isolated ownership root. No production path/FD
// constructor is introduced and no operator registry is touched.
func NewFixtureRegistry(root string) (*Registry, error) { return newRegistry(root) }
