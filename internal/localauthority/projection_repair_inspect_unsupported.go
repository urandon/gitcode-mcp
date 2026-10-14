//go:build !darwin && !linux

package localauthority

import (
	"context"
	"os"
)

func inspectRepairDirectory(context.Context, *Store, string, *os.File) (projectionRepairImage, error) {
	return projectionRepairImage{}, Error{DurabilityUnavailable}
}

func projectionRepairNames(*os.File) ([]string, error) { return nil, Error{DurabilityUnavailable} }

func inspectProjectionRepair(context.Context, *Store) (projectionRepairImage, error) {
	return projectionRepairImage{}, Error{DurabilityUnavailable}
}
