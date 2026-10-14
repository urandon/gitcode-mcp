//go:build !darwin && !linux

package localauthority

import (
	"context"
	"os"
)

func walkSemanticPhysical(context.Context, *os.File, string, *semanticPhysicalPass) error {
	return Error{DurabilityUnavailable}
}
