package mcp

import (
	"errors"
	"os"
	"strings"

	"gitcode-mcp/internal/cache"
)

type StartupDiagnostic struct {
	ErrorClass  string `json:"error_class"`
	Message     string `json:"message"`
	Remediation string `json:"remediation"`
}

func StartupDiagnosticFromError(err error) StartupDiagnostic {
	if err == nil {
		return StartupDiagnostic{}
	}
	var schemaErr *cache.SchemaVersionError
	var lockErr cache.ErrLockContention
	switch {
	case errors.As(err, &schemaErr):
		return StartupDiagnostic{ErrorClass: "schema_incompatible", Message: schemaErr.Compat.Message, Remediation: schemaErr.Compat.Remediation}
	case errors.As(err, &lockErr):
		return StartupDiagnostic{ErrorClass: "cache_lock_contention", Message: "cache writer lock is held", Remediation: "wait for the active gitcode-mcp operation to finish, then retry MCP startup"}
	case os.IsPermission(err):
		return StartupDiagnostic{ErrorClass: "cache_path_unwritable", Message: "cache path is not writable", Remediation: "run chmod on the cache directory or configure a writable --cache-path"}
	default:
		msg := strings.TrimSpace(err.Error())
		if msg == "" {
			msg = "MCP startup failed before service construction"
		}
		return StartupDiagnostic{ErrorClass: "startup-failure", Message: msg, Remediation: "run 'gitcode-mcp doctor' or retry with a writable cache path after fixing the startup error"}
	}
}

func (d StartupDiagnostic) present() bool {
	return strings.TrimSpace(d.ErrorClass) != ""
}
