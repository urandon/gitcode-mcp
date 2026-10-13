package localauthority

// TestRegistry is available only in this package's augmented test build. It
// lets external transport tests use isolated qualified roots without account
// lookup or changing the production machine-registry constructor boundary.
var TestRegistry = newRegistry
