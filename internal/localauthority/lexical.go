package localauthority

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"gitcode-mcp/internal/authority"
)

const lexicalEntityBudget = 128

// LexicalRequest selects a bounded slice of canonical content. Higher layers
// own requested hybrid mode and its policy/coverage fallback; this layer never
// constructs an embedding provider or claims semantic retrieval.
type LexicalRequest struct {
	ProjectID authority.UUID
	Query     string
	Kind      authority.EntityKind
	Limit     int
	Cursor    string
}

type LexicalHit struct {
	Citation      authority.Citation `json:"citation"`
	ContentDigest string             `json:"content_digest"`
	Score         int                `json:"score"`
	Snippet       string             `json:"snippet"`
}

type LexicalResult struct {
	ProjectID  authority.UUID `json:"project_id"`
	Sequence   uint64         `json:"sequence"`
	Scanned    int            `json:"scanned"`
	Partial    bool           `json:"partial"`
	NextCursor string         `json:"next_cursor,omitempty"`
	Hits       []LexicalHit   `json:"hits"`
}

type lexicalCursor struct {
	QueryHash string               `json:"query_hash"`
	Kind      authority.EntityKind `json:"kind"`
	Snapshot  string               `json:"snapshot"`
}

// LexicalSearch does real case-normalized whole-token AND matching against
// typed content, not JSON field names. Results are deterministic top-K within
// the explicitly bounded slice, never falsely claimed global corpus ranking.
// Canonical hydration verifies immutable citations independently of an index.
func (s *Store) LexicalSearch(ctx context.Context, req LexicalRequest) (LexicalResult, error) {
	zero := LexicalResult{}
	if !req.ProjectID.Valid() || len(req.Query) == 0 || len(req.Query) > 4096 || !utf8.ValidString(req.Query) || req.Kind != "" && !req.Kind.Valid() || req.Limit < 1 || req.Limit > 100 || len(req.Cursor) > 1024 {
		return zero, authority.Error{Code: authority.InvalidContract}
	}
	terms := map[string]bool{}
	if err := lexicalTokens(ctx, req.Query, func(token string, _ int) { terms[token] = true }); err != nil {
		return zero, err
	}
	if len(terms) == 0 || len(terms) > 64 {
		return zero, authority.Error{Code: authority.InvalidContract}
	}
	queryHash := hash([]byte(req.Query))
	c := lexicalCursor{QueryHash: queryHash, Kind: req.Kind}
	if req.Cursor != "" {
		data, err := base64.RawURLEncoding.DecodeString(req.Cursor)
		if err != nil || authority.DecodeJSON(data, &c) != nil || c.QueryHash != queryHash || c.Kind != req.Kind || c.Snapshot == "" {
			return zero, authority.Error{Code: authority.InvalidContract}
		}
	}
	result := LexicalResult{ProjectID: req.ProjectID, Hits: []LexicalHit{}}
	for result.Scanned < lexicalEntityBudget {
		limit := lexicalEntityBudget - result.Scanned
		if limit > 25 {
			limit = 25
		}
		page, err := s.Snapshot(ctx, SnapshotRequest{ProjectID: req.ProjectID, Limit: limit, Cursor: c.Snapshot})
		if err != nil {
			return zero, err
		}
		result.Sequence = page.Sequence
		for _, entry := range page.Entities {
			result.Scanned++
			v := entry.Entity
			if v.Tombstone || req.Kind != "" && v.Citation.Kind != req.Kind {
				continue
			}
			text, err := searchableText(v)
			if err != nil {
				return zero, err
			}
			matched := map[string]bool{}
			score, first := 0, -1
			if err = lexicalTokens(ctx, text, func(token string, start int) {
				if terms[token] {
					matched[token] = true
					if score < 1000 {
						score++
					}
					if first < 0 {
						first = start
					}
				}
			}); err != nil {
				return zero, err
			}
			if len(matched) == len(terms) {
				result.Hits = append(result.Hits, LexicalHit{Citation: v.Citation, ContentDigest: entry.ContentDigest, Score: score, Snippet: lexicalSnippet(text, first)})
			}
		}
		c.Snapshot = page.NextCursor
		if c.Snapshot == "" {
			break
		}
	}
	sort.Slice(result.Hits, func(i, j int) bool {
		a, b := result.Hits[i], result.Hits[j]
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		return a.Citation.EntityID < b.Citation.EntityID
	})
	if len(result.Hits) > req.Limit {
		result.Hits = result.Hits[:req.Limit]
	}
	if c.Snapshot != "" {
		data, _ := json.Marshal(c)
		result.Partial = true
		result.NextCursor = base64.RawURLEncoding.EncodeToString(data)
	}
	return result, nil
}

func searchableText(v authority.EntityView) (string, error) {
	invalid := Error{Corrupt}
	switch v.Citation.Kind {
	case authority.Issue:
		var p authority.IssuePayload
		if authority.DecodeJSON(v.Payload, &p) != nil {
			return "", invalid
		}
		return p.Title + "\n" + p.Body, nil
	case authority.Comment:
		var p authority.CommentPayload
		if authority.DecodeJSON(v.Payload, &p) != nil {
			return "", invalid
		}
		return p.Body, nil
	case authority.Label:
		var p authority.LabelPayload
		if authority.DecodeJSON(v.Payload, &p) != nil {
			return "", invalid
		}
		if p.Archived {
			return "", nil
		}
		return p.Name, nil
	case authority.Milestone:
		var p authority.MilestonePayload
		if authority.DecodeJSON(v.Payload, &p) != nil {
			return "", invalid
		}
		if p.Archived {
			return "", nil
		}
		return p.Title + "\n" + p.Description, nil
	case authority.Document:
		var p authority.DocumentPayload
		if authority.DecodeJSON(v.Payload, &p) != nil {
			return "", invalid
		}
		return p.Title + "\n" + p.Markdown, nil
	case authority.Link:
		var p authority.LinkPayload
		if authority.DecodeJSON(v.Payload, &p) != nil {
			return "", invalid
		}
		return p.Relation, nil
	default:
		return "", invalid
	}
}

func lexicalTokens(ctx context.Context, text string, visit func(string, int)) error {
	start, count := -1, 0
	for i, r := range text {
		count++
		if count%4096 == 0 && ctx.Err() != nil {
			return ctx.Err()
		}
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			if start < 0 {
				start = i
			}
		} else if start >= 0 {
			visit(strings.ToLower(text[start:i]), start)
			start = -1
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if start >= 0 {
		visit(strings.ToLower(text[start:]), start)
	}
	return nil
}

func lexicalSnippet(text string, first int) string {
	start := first - 128
	if start < 0 {
		start = 0
	}
	for start < len(text) && !utf8.RuneStart(text[start]) {
		start++
	}
	end := start + 512
	if end > len(text) {
		end = len(text)
	}
	for end < len(text) && !utf8.RuneStart(text[end]) {
		end--
	}
	return text[start:end]
}
