package adminui

import "embed"

// Files contains the production SvelteKit bundle. The checked-in output keeps
// `go install ./...` independent from a Node.js runtime.
//
//go:embed all:assets
var Files embed.FS
