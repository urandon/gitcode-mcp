package cli

import (
	"bytes"
	"gitcode-mcp/internal/service"
	"strings"
	"testing"
)

func TestMilestoneFieldErrorHasActionableSafeMessage(t *testing.T) {
	for _, format := range []string{"json", "text"} {
		var out bytes.Buffer
		err := service.ErrInvalidQuery{Field: "milestone.description", Message: "description exceeds GitCode's maximum of 2000 UTF-16 code units; shorten it explicitly"}
		if writeCommandError(&out, format, startupPlan{}, err) == 0 || !strings.Contains(out.String(), "2000 UTF-16") || !strings.Contains(out.String(), "shorten") {
			t.Fatalf("missing field guidance: %s", out.String())
		}
	}
}
