package gitcode

import (
	"strings"
	"unicode/utf8"
)

// MilestoneDescriptionMaxUTF16 is the v5 limit verified by bounded live probes.
const MilestoneDescriptionMaxUTF16 = 2000

// ValidateMilestoneWriteFields is shared by dry-run/service and HTTP validation.
// GitCode requires title and due_on for updates as well as creation. It counts
// description length in UTF-16 code units and always creates an open milestone.
func ValidateMilestoneWriteFields(req MilestoneWriteRequest, create bool) error {
	if strings.TrimSpace(req.Title) == "" {
		return ErrValidationFailed{Field: "milestone.title", Message: "title is required by GitCode for create and update; supply the current title to preserve it"}
	}
	if strings.TrimSpace(req.DueOn) == "" {
		return ErrValidationFailed{Field: "milestone.due_on", Message: "due_on is required by GitCode for create and update; supply the current date to preserve it"}
	}
	if _, err := parseMilestoneDueOn(req.DueOn); err != nil {
		return err
	}
	if !utf8.ValidString(req.Description) {
		return ErrValidationFailed{Field: "milestone.description", Message: "description must be valid UTF-8"}
	}
	units := 0
	for _, r := range req.Description {
		units++
		if r > 0xffff {
			units++
		}
		if units > MilestoneDescriptionMaxUTF16 {
			return ErrValidationFailed{Field: "milestone.description", Message: "description exceeds GitCode's maximum of 2000 UTF-16 code units; shorten it explicitly (supplementary characters count as two)"}
		}
	}
	if create && strings.TrimSpace(req.State) == "closed" {
		return ErrValidationFailed{Field: "milestone.state", Message: "GitCode creates milestones open; create with omitted/open state, then explicitly update to closed"}
	}
	return validateMilestoneWriteState(req.State)
}
