package gitcode

import (
	"encoding/json"
	"strconv"
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
	return prDiscussionsToComments(discussions, prNumber)
}

func prDiscussionsToComments(discussions []prDiscussion, prNumber int) ([]PRComment, error) {
	out := make([]PRComment, 0)
	for _, discussion := range discussions {
		discussionID, err := decodeOptionalID(discussion.ID)
		if err != nil {
			return nil, err
		}
		for i, note := range discussion.Notes {
			comment, err := prDiscussionNoteToComment(discussionID, i, note, prNumber)
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

func prDiscussionNoteToComment(discussionID string, index int, note prDiscussionNote, prNumber int) (PRComment, error) {
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
		parentID = discussionID
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

func mergePRDiscussionMetadata(items []PRComment, discussions []PRComment) []PRComment {
	byID := make(map[string]PRComment, len(discussions))
	for _, comment := range discussions {
		byID[comment.ID] = comment
	}
	seen := make(map[string]bool, len(items))
	for i := range items {
		seen[items[i].ID] = true
		if enriched, ok := byID[items[i].ID]; ok {
			items[i] = mergePRComment(items[i], enriched)
		}
	}
	for _, comment := range discussions {
		if comment.ReviewKind == "inline" && !seen[comment.ID] {
			items = append(items, comment)
		}
	}
	return items
}

func mergePRComment(base, enriched PRComment) PRComment {
	if enriched.Body != "" {
		base.Body = enriched.Body
	}
	if enriched.Author != "" {
		base.Author = enriched.Author
	}
	if enriched.DiscussionID != "" {
		base.DiscussionID = enriched.DiscussionID
	}
	if enriched.ReviewKind != "" {
		base.ReviewKind = enriched.ReviewKind
	}
	if enriched.Path != "" {
		base.Path = enriched.Path
	}
	if enriched.Line != 0 {
		base.Line = enriched.Line
	}
	if enriched.Resolved != nil {
		base.Resolved = enriched.Resolved
	}
	if enriched.Resolvable != nil {
		base.Resolvable = enriched.Resolvable
	}
	if len(enriched.Positions) > 0 {
		base.Positions = enriched.Positions
	}
	if enriched.ParentID != "" {
		base.ParentID = enriched.ParentID
	}
	if !enriched.CreatedAt.IsZero() {
		base.CreatedAt = enriched.CreatedAt
	}
	if !enriched.UpdatedAt.IsZero() {
		base.UpdatedAt = enriched.UpdatedAt
	}
	return base
}

func prReviewCommentPayload(req CreatePRReviewCommentRequest, pr PullRequest) any {
	newLine := req.Line
	if req.Position > 0 && newLine == 0 {
		newLine = req.Position
	}
	if req.StartLine > 0 {
		newLine = firstPositive(req.EndLine, req.StartLine, newLine)
	}
	type reviewPayload struct {
		Body     string `json:"body"`
		Position any    `json:"position"`
	}
	payload := reviewPayload{
		Body: req.Body,
		Position: struct {
			BaseSHA      string `json:"base_sha"`
			HeadSHA      string `json:"head_sha"`
			StartSHA     string `json:"start_sha"`
			PositionType string `json:"position_type"`
			NewPath      string `json:"new_path"`
			OldPath      string `json:"old_path"`
			NewLine      int    `json:"new_line,omitempty"`
		}{
			BaseSHA:      pr.BaseSHA,
			HeadSHA:      pr.HeadSHA,
			StartSHA:     firstNonEmpty(pr.BaseSHA, pr.HeadSHA),
			PositionType: "text",
			NewPath:      req.Path,
			OldPath:      req.Path,
			NewLine:      newLine,
		},
	}
	return payload
}

func ensurePRReviewCommentPosition(comment PRComment, req CreatePRReviewCommentRequest, pr PullRequest) PRComment {
	if len(comment.Positions) > 0 {
		return comment
	}
	newLine := req.Line
	if req.Position > 0 && newLine == 0 {
		newLine = req.Position
	}
	if req.StartLine > 0 {
		newLine = firstPositive(req.EndLine, req.StartLine, newLine)
	}
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

func prReviewCommentEndpoint(owner, repo string, number int) string {
	return "/api/v4/projects/" + pathEscapedRepo(owner, repo) + "/merge_requests/" + strconv.Itoa(number) + "/discussions"
}

func pathEscapedRepo(owner, repo string) string {
	return strings.ReplaceAll(owner+"/"+repo, "/", "%2F")
}

func isConfirmedInlineComment(comment PRComment, req CreatePRReviewCommentRequest) bool {
	return comment.ID != "" && comment.ReviewKind == "inline" && comment.Path == req.Path && comment.Line == req.Line
}
