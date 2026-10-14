//go:build !darwin && !linux

package localauthority

import "context"

func inspectProjectionRepair(context.Context, *Store) (projectionRepairImage, error) {
	return projectionRepairImage{}, Error{DurabilityUnavailable}
}
