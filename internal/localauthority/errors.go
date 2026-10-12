// Package localauthority owns durable local tracker state, never a cache or
// provider. Runtime enrollment and transport rollout are separate cohorts.
package localauthority

import "context"

type Code string

const (
	ReadOnly              Code = "authority_read_only"
	Corrupt               Code = "authority_corrupt"
	DurabilityUnavailable Code = "authority_durability_unavailable"
	Capacity              Code = "authority_capacity"
	StorageUnavailable    Code = "authority_storage_unavailable"
	CommitUncertain       Code = "authority_commit_uncertain"
	InitializationPending Code = "authority_initialization_pending"
)

// Error is deliberately content-free; SQL, paths, keys and payloads are private.
type Error struct{ Code Code }

func (e Error) DiagnosticCode() string { return string(e.Code) }
func (e Error) Error() string {
	switch e.Code {
	case ReadOnly:
		return "local authority is read-only"
	case Corrupt:
		return "local authority integrity check failed"
	case DurabilityUnavailable:
		return "local authority durability is unavailable"
	case Capacity:
		return "local authority operation exceeds capacity"
	case CommitUncertain:
		return "local authority commit acknowledgement is uncertain; retain the original caller key"
	case InitializationPending:
		return "local authority initialization is pending; retry the original initialization intent"
	default:
		return "local authority storage is unavailable"
	}
}

func storageError(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return Error{StorageUnavailable}
}
