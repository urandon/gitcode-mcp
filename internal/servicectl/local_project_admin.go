package servicectl

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"time"

	"gitcode-mcp/internal/adminhttp"
	"gitcode-mcp/internal/adminui"
	"gitcode-mcp/internal/capability"
	"gitcode-mcp/internal/localgit"
	"gitcode-mcp/internal/service"
)

func (m Manager) newLocalProjectAdmin(host *service.LocalProjects, paths Paths, started time.Time) (*adminhttp.Controller, error) {
	if !m.AdminAutoStart {
		return nil, nil
	}
	bind := m.AdminBind
	if bind == "" {
		bind = "127.0.0.1:0"
	}
	hostname, _, err := net.SplitHostPort(bind)
	ip := net.ParseIP(hostname)
	if err != nil || ip == nil || !ip.IsLoopback() || m.AdminAllowNonLoopback {
		return nil, localHostError("private_ipc_required")
	}
	assets, err := fs.Sub(adminui.Files, "assets")
	if err != nil {
		return nil, localHostError("unavailable")
	}
	// The existing receipt manager stores only derived index result metadata.
	// Project mutations never use it: their sole receipt authority is Git.
	receipts := NewAdminControlReceiptManager(paths.JobsPath + ".controls")
	if err := receipts.Load(); err != nil {
		return nil, localHostError("unavailable")
	}
	return adminhttp.New(adminhttp.Config{
		Bind: bind, Assets: assets, SessionTTL: m.AdminSessionTTL,
		Readiness: func(context.Context) adminhttp.Readiness {
			return adminhttp.Readiness{APIVersion: "1", Version: m.Version, DaemonRunning: true, SessionSecure: true, CheckedAt: time.Now().UTC()}
		},
		Snapshot: func(context.Context) (adminhttp.ObservationSnapshot, error) {
			v := adminhttp.ObservationSnapshot{Service: adminhttp.ServiceObservation{Version: m.Version, Commit: m.Commit, Protocol: "admin.v1", Running: true, StartedAt: &started, AdminSecure: true}}
			for _, cap := range capability.LocalProjectCapabilities() {
				v.Capabilities = append(v.Capabilities, adminCapabilityObservation(cap))
			}
			return adminhttp.FinalizeSnapshot(v, time.Now().UTC()), nil
		},
		LocalProjects: localProjectAdminProvider(host, receipts),
	}), nil
}

func localProjectAdminProvider(host *service.LocalProjects, receipts *AdminControlReceiptManager) adminhttp.LocalProjectProvider {
	return func(ctx context.Context, operation string, raw json.RawMessage) (result any, err error) {
		defer func() {
			if err != nil {
				err = localProjectAdminError(err)
			}
		}()
		switch operation {
		case "status":
			return host.Status(ctx)
		case "list", "get", "history":
			var q service.LocalProjectQuery
			if err = DecodeLocalProjectJSON(raw, &q); err != nil {
				return nil, err
			}
			switch operation {
			case "list":
				return host.List(ctx, q)
			case "get":
				return host.Get(ctx, q)
			default:
				return host.History(ctx, q)
			}
		case "search":
			var q service.LocalProjectSearchRequest
			if err = DecodeLocalProjectJSON(raw, &q); err != nil {
				return nil, err
			}
			return host.Search(ctx, q)
		case "mutation-plan", "mutation-apply":
			var q service.LocalProjectMutationPlanRequest
			if err = DecodeLocalProjectJSON(raw, &q); err != nil {
				return nil, err
			}
			if operation == "mutation-plan" {
				return host.PlanMutation(ctx, q)
			}
			return host.ApplyMutationPlan(ctx, q)
		case "index-plan", "index-apply":
			var q service.LocalProjectIndexPlanRequest
			if err = DecodeLocalProjectJSON(raw, &q); err != nil {
				return nil, err
			}
			if operation == "index-plan" {
				return host.PlanIndex(ctx, q)
			}
			if len(q.IdempotencyKey) > 256 || strings.TrimSpace(q.IdempotencyKey) == "" {
				return nil, localHostError("invalid_argument")
			}
			return receipts.Apply(ctx, "local_project_index", q.Project, q.IdempotencyKey, q, func() (map[string]any, error) {
				indexed, err := host.ApplyIndexPlan(ctx, q)
				if err != nil {
					return nil, err
				}
				encoded, _ := json.Marshal(indexed)
				var value map[string]any
				_ = json.Unmarshal(encoded, &value)
				return value, nil
			})
		default:
			return nil, localHostError("unsupported_capability")
		}
	}
}

func localProjectAdminError(err error) error {
	var existing adminhttp.ControlError
	if errors.As(err, &existing) {
		return existing
	}
	status, code, message := http.StatusServiceUnavailable, "local_git_unavailable", "Local project operation is unavailable."
	var local *localgit.Error
	if errors.As(err, &local) {
		code = local.DiagnosticCode()
		switch local.Code {
		case "invalid_argument", "invalid_schema", "unsupported_schema":
			status, message = http.StatusBadRequest, "Correct the typed local project fields."
		case "limit_exceeded":
			status, message = http.StatusBadRequest, "The bounded project request or response exceeds its limit."
		case "unknown_project", "not_found":
			status, message = http.StatusNotFound, "The selected local project or entity is unavailable."
		case "read_only":
			status, message = http.StatusForbidden, "This project registration is read-only."
		case "write_conflict", "stale_plan":
			status, message = http.StatusConflict, "Current Git revision or confirmed intent changed; your draft is retained."
		case "idempotency_conflict":
			status, message = http.StatusConflict, "The key belongs to another intent; review the draft before generating a new key."
		case "writer_busy":
			status, message = http.StatusConflict, "The project writer is busy; retain the same confirmed intent and key."
		case "publication_ambiguous":
			message = "Publication is uncertain; retry the exact confirmed intent and key, never a new key."
		case "invalid_configuration":
			status, message = http.StatusConflict, "Trusted local configuration is unavailable; use the CLI handoff."
		case "history_unavailable":
			message = "Bounded Git history cannot establish a complete result."
		}
	}
	return adminhttp.ControlError{Status: status, Code: code, Message: message, Remediation: "Retain the draft and confirmed key. Inspect current project state before another action.", CLIHandoff: "gitcode-mcp project status"}
}
