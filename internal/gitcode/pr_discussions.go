package gitcode

import (
	"encoding/json"
	"strings"
)

type prDiscussionEnvelope struct {
	Content struct {
		Data []prDiscussion `json:"data"`
	} `json:"content"`
	Data  []prDiscussion     `json:"data"`
	Notes []prDiscussionNote `json:"notes"`
	prDiscussion
}

type flatPRDiscussionEnvelope struct {
	Content struct {
		Data []prDiscussionNote `json:"data"`
	} `json:"content"`
	Data []prDiscussionNote `json:"data"`
}

type prDiscussion struct {
	ID           any                `json:"id"`
	NoteableType string             `json:"noteable_type"`
	Notes        []prDiscussionNote `json:"notes"`
	ProjectID    any                `json:"project_id"`
	Resolved     *bool              `json:"resolved"`
}

type prDiscussionNote struct {
	ID               any             `json:"id"`
	Body             string          `json:"body"`
	Author           json.RawMessage `json:"author"`
	DiscussionID     any             `json:"discussion_id"`
	Type             string          `json:"type"`
	NoteableType     string          `json:"noteable_type"`
	DiffFile         string          `json:"diff_file"`
	FilePath         string          `json:"file_path"`
	Line             any             `json:"line"`
	NewLine          any             `json:"new_line"`
	Position         *prDiffPosition `json:"position"`
	OriginalPosition *prDiffPosition `json:"original_position"`
	Resolved         *bool           `json:"resolved"`
	Resolvable       *bool           `json:"resolvable"`
	IsOutdated       *bool           `json:"is_outdated"`
	CreatedAt        string          `json:"created_at"`
	UpdatedAt        string          `json:"updated_at"`
	InReplyToID      any             `json:"in_reply_to_id"`
	ReplyID          any             `json:"reply_id"`
}

type prDiffPosition struct {
	BaseSHA       string `json:"base_sha"`
	StartSHA      string `json:"start_sha"`
	HeadSHA       string `json:"head_sha"`
	OldPath       string `json:"old_path"`
	NewPath       string `json:"new_path"`
	PositionType  string `json:"position_type"`
	OldLine       any    `json:"old_line"`
	NewLine       any    `json:"new_line"`
	StartOldLine  any    `json:"start_old_line"`
	StartNewLine  any    `json:"start_new_line"`
	LineCode      string `json:"line_code"`
	StartLineCode string `json:"start_line_code"`
	PatchsetIID   any    `json:"patchset_iid"`
	DiffID        any    `json:"diff_id"`
	VersionSHA    string `json:"version_sha"`
}

func decodePRDiscussionComments(endpoint string, body []byte, prNumber int) ([]PRComment, error) {
	var list []prDiscussion
	if err := json.Unmarshal(body, &list); err == nil {
		return prDiscussionsToComments(list, prNumber)
	}
	var envelope prDiscussionEnvelope
	if err := decodeSchemaJSON(endpoint, body, &envelope); err != nil {
		return nil, err
	}
	discussions := envelope.Content.Data
	if len(discussions) == 0 {
		discussions = envelope.Data
	}
	if len(discussions) == 0 && len(envelope.Notes) > 0 {
		discussions = []prDiscussion{envelope.prDiscussion}
		discussions[0].Notes = envelope.Notes
	}
	if !hasDiscussionNotes(discussions) {
		var flat flatPRDiscussionEnvelope
		if err := json.Unmarshal(body, &flat); err == nil {
			notes := flat.Content.Data
			if len(notes) == 0 {
				notes = flat.Data
			}
			if hasFlatDiscussionNotes(notes) {
				return flatPRDiscussionNotesToComments(notes, prNumber)
			}
		}
	}
	return prDiscussionsToComments(discussions, prNumber)
}

func hasDiscussionNotes(discussions []prDiscussion) bool {
	for _, discussion := range discussions {
		if len(discussion.Notes) > 0 {
			return true
		}
	}
	return false
}

func hasFlatDiscussionNotes(notes []prDiscussionNote) bool {
	for _, note := range notes {
		if flatDiscussionNoteIsInline(note) {
			return true
		}
	}
	return false
}

func flatPRDiscussionNotesToComments(notes []prDiscussionNote, prNumber int) ([]PRComment, error) {
	grouped := make([]prDiscussion, 0)
	byID := map[string]int{}
	for _, note := range notes {
		discussionID, err := decodeOptionalID(note.DiscussionID)
		if err != nil {
			return nil, err
		}
		if discussionID == "" {
			discussionID, err = decodeOptionalID(note.ID)
			if err != nil {
				return nil, err
			}
		}
		index, ok := byID[discussionID]
		if !ok {
			index = len(grouped)
			byID[discussionID] = index
			grouped = append(grouped, prDiscussion{ID: discussionID})
		}
		grouped[index].Notes = append(grouped[index].Notes, note)
	}
	inlineDiscussions := grouped[:0]
	for _, discussion := range grouped {
		for _, note := range discussion.Notes {
			if flatDiscussionNoteIsInline(note) {
				inlineDiscussions = append(inlineDiscussions, discussion)
				break
			}
		}
	}
	return prDiscussionsToComments(inlineDiscussions, prNumber)
}

func flatDiscussionNoteIsInline(note prDiscussionNote) bool {
	kind := strings.ToLower(note.Type)
	return note.Position != nil || note.OriginalPosition != nil || strings.TrimSpace(note.DiffFile) != "" || strings.TrimSpace(note.FilePath) != "" || note.Line != nil || note.NewLine != nil || strings.Contains(kind, "diff") || strings.Contains(kind, "inline")
}

func prDiscussionsToComments(discussions []prDiscussion, prNumber int) ([]PRComment, error) {
	out := make([]PRComment, 0)
	for _, discussion := range discussions {
		discussionID, err := decodeOptionalID(discussion.ID)
		if err != nil {
			return nil, err
		}
		rootID := ""
		if len(discussion.Notes) > 0 {
			rootID, err = decodeOptionalID(discussion.Notes[0].ID)
			if err != nil {
				return nil, err
			}
		}
		for i, note := range discussion.Notes {
			comment, err := prDiscussionNoteToComment(discussionID, rootID, i, note, prNumber)
			if err != nil {
				return nil, err
			}
			if comment.ID != "" {
				out = append(out, comment)
			}
		}
	}
	return out, nil
}

func prDiscussionNoteToComment(discussionID, rootID string, index int, note prDiscussionNote, prNumber int) (PRComment, error) {
	id, err := decodeOptionalID(note.ID)
	if err != nil {
		return PRComment{}, err
	}
	noteDiscussionID, err := decodeOptionalID(note.DiscussionID)
	if err != nil {
		return PRComment{}, err
	}
	if noteDiscussionID != "" {
		discussionID = noteDiscussionID
	}
	lineValue := firstNonNil(note.NewLine, note.Line)
	if note.Position != nil {
		lineValue = firstNonNil(note.Position.NewLine, note.Position.OldLine, lineValue)
	}
	line, err := decodeOptionalInt(lineValue)
	if err != nil {
		return PRComment{}, err
	}
	created, err := decodeOptionalTime("pr_comment.created_at", note.CreatedAt)
	if err != nil {
		return PRComment{}, err
	}
	updated, err := decodeOptionalTime("pr_comment.updated_at", note.UpdatedAt)
	if err != nil {
		return PRComment{}, err
	}
	parentID, err := decodeOptionalID(firstNonNil(note.InReplyToID, note.ReplyID))
	if err != nil {
		return PRComment{}, err
	}
	if parentID == "" && index > 0 {
		parentID = rootID
	}
	path := firstNonEmpty(note.FilePath, note.DiffFile)
	if note.Position != nil {
		path = firstNonEmpty(note.Position.NewPath, note.Position.OldPath, path)
	}
	positions, err := prCommentPositions(note)
	if err != nil {
		return PRComment{}, err
	}
	reviewKind := "general"
	rawKind := strings.ToLower(firstNonEmpty(note.Type, note.NoteableType))
	if path != "" || line > 0 || strings.Contains(rawKind, "diff") || strings.Contains(rawKind, "inline") {
		reviewKind = "inline"
	}
	if updated.IsZero() {
		updated = created
	}
	if created.IsZero() {
		created = updated
	}
	return PRComment{
		Kind:         "pr_comment",
		ID:           id,
		Body:         note.Body,
		Author:       firstNonEmpty(decodeActor(note.Author)),
		DiscussionID: discussionID,
		ReviewKind:   reviewKind,
		Path:         path,
		Line:         line,
		Resolved:     note.Resolved,
		Resolvable:   note.Resolvable,
		ParentID:     parentID,
		Positions:    positions,
		PRNumber:     prNumber,
		CreatedAt:    created,
		UpdatedAt:    updated,
	}, nil
}

func prCommentPositions(note prDiscussionNote) ([]PRCommentPosition, error) {
	out := []PRCommentPosition{}
	if note.Position != nil {
		position, err := prDiffPositionToCommentPosition("current", note.Position, note.IsOutdated)
		if err != nil {
			return nil, err
		}
		out = append(out, position)
	}
	if note.OriginalPosition != nil {
		position, err := prDiffPositionToCommentPosition("original", note.OriginalPosition, note.IsOutdated)
		if err != nil {
			return nil, err
		}
		out = append(out, position)
	}
	return out, nil
}

func prDiffPositionToCommentPosition(kind string, raw *prDiffPosition, isOutdated *bool) (PRCommentPosition, error) {
	oldLine, err := decodeOptionalInt(raw.OldLine)
	if err != nil {
		return PRCommentPosition{}, err
	}
	newLine, err := decodeOptionalInt(raw.NewLine)
	if err != nil {
		return PRCommentPosition{}, err
	}
	startOldLine, err := decodeOptionalInt(raw.StartOldLine)
	if err != nil {
		return PRCommentPosition{}, err
	}
	startNewLine, err := decodeOptionalInt(raw.StartNewLine)
	if err != nil {
		return PRCommentPosition{}, err
	}
	patchsetIID, err := decodeOptionalInt(raw.PatchsetIID)
	if err != nil {
		return PRCommentPosition{}, err
	}
	diffID, err := decodeOptionalInt(raw.DiffID)
	if err != nil {
		return PRCommentPosition{}, err
	}
	side := "new"
	if newLine == 0 && oldLine > 0 {
		side = "old"
	}
	return PRCommentPosition{
		PositionKind:  kind,
		PositionType:  raw.PositionType,
		BaseSHA:       raw.BaseSHA,
		StartSHA:      raw.StartSHA,
		HeadSHA:       raw.HeadSHA,
		OldPath:       raw.OldPath,
		NewPath:       raw.NewPath,
		OldLine:       oldLine,
		NewLine:       newLine,
		StartOldLine:  startOldLine,
		StartNewLine:  startNewLine,
		LineCode:      raw.LineCode,
		StartLineCode: raw.StartLineCode,
		PatchsetIID:   patchsetIID,
		DiffID:        diffID,
		VersionSHA:    raw.VersionSHA,
		Side:          side,
		IsOutdated:    isOutdated,
	}, nil
}

func prReviewCommentPayload(req CreatePRReviewCommentRequest, _ PullRequest) any {
	newLine := prReviewCommentNewLine(req)
	type reviewPayload struct {
		Body      string `json:"body"`
		Path      string `json:"path"`
		Line      int    `json:"line"`
		NewLine   int    `json:"new_line"`
		Position  int    `json:"position"`
		StartLine int    `json:"start_line,omitempty"`
		EndLine   int    `json:"end_line,omitempty"`
	}
	payload := reviewPayload{
		Body:     req.Body,
		Path:     req.Path,
		Line:     newLine,
		NewLine:  newLine,
		Position: newLine,
	}
	if req.StartLine > 0 {
		payload.StartLine = req.StartLine
		payload.EndLine = firstPositive(req.EndLine, newLine)
	}
	return payload
}

func requestConfirmedPRReviewComment(created PRComment, req CreatePRReviewCommentRequest) PRComment {
	newLine := prReviewCommentNewLine(req)
	created.Body = firstNonEmpty(created.Body, req.Body)
	created.ReviewKind = "inline"
	created.Path = req.Path
	created.Line = newLine
	created.Position = newLine
	created.StartLine = req.StartLine
	if req.StartLine > 0 {
		created.EndLine = firstPositive(req.EndLine, newLine)
	} else {
		created.EndLine = req.EndLine
	}
	created.PRNumber = req.Number
	return created
}

func prReviewCommentNewLine(req CreatePRReviewCommentRequest) int {
	newLine := req.Line
	if req.Position > 0 && newLine == 0 {
		newLine = req.Position
	}
	if req.StartLine > 0 {
		newLine = firstPositive(req.EndLine, req.StartLine, newLine)
	}
	return newLine
}

func ensurePRReviewCommentPosition(comment PRComment, req CreatePRReviewCommentRequest, pr PullRequest) PRComment {
	if len(comment.Positions) > 0 {
		return comment
	}
	newLine := prReviewCommentNewLine(req)
	comment.Positions = []PRCommentPosition{{
		PositionKind: "current",
		PositionType: "text",
		BaseSHA:      pr.BaseSHA,
		StartSHA:     firstNonEmpty(pr.BaseSHA, pr.HeadSHA),
		HeadSHA:      pr.HeadSHA,
		OldPath:      req.Path,
		NewPath:      req.Path,
		NewLine:      newLine,
		StartNewLine: req.StartLine,
		Side:         "new",
	}}
	return comment
}
