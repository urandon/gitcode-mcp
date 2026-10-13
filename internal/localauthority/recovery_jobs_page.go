package localauthority

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"math"

	"gitcode-mcp/internal/authority"
)

type RecoveryJobsRequest struct {
	ProjectID authority.UUID `json:"project_id"`
	Limit     int            `json:"limit"`
	Cursor    string         `json:"cursor,omitempty"`
}

func (r RecoveryJobsRequest) Validate() error {
	if !r.ProjectID.Valid() || r.Limit < 1 || r.Limit > 100 || len(r.Cursor) > 512 {
		return authority.Error{Code: authority.InvalidContract}
	}
	return nil
}

type RecoveryJobsPage struct {
	Schema     int               `json:"schema"`
	ProjectID  authority.UUID    `json:"project_id"`
	Generation uint64            `json:"generation"`
	Retained   int               `json:"retained"`
	Jobs       []RecoveryJobView `json:"jobs"`
	NextCursor string            `json:"next_cursor,omitempty"`
}

func (p RecoveryJobsPage) Validate() error {
	if p.Schema != 1 || !p.ProjectID.Valid() || p.Generation < 1 || p.Generation > math.MaxInt64 || p.Retained < 0 || p.Retained > MaxRecoveryJobs || p.Jobs == nil || len(p.Jobs) > 100 || len(p.NextCursor) > 512 {
		return authority.Error{Code: authority.InvalidContract}
	}
	var last authority.UUID
	for _, v := range p.Jobs {
		if v.Validate() != nil || v.ProjectID != p.ProjectID || v.JobID <= last {
			return authority.Error{Code: authority.InvalidContract}
		}
		last = v.JobID
	}
	body, err := json.Marshal(p)
	if err != nil || len(body) > MaxRecoveryJobPageBytes {
		return authority.Error{Code: authority.InvalidContract}
	}
	return nil
}

type recoveryJobsCursor struct {
	ProjectID  authority.UUID `json:"project_id"`
	Generation uint64         `json:"generation"`
	Digest     string         `json:"digest"`
	Last       authority.UUID `json:"last"`
}

func (c *Coordinator) RecoveryJobs(ctx context.Context, req RecoveryJobsRequest) (RecoveryJobsPage, error) {
	var zero RecoveryJobsPage
	if err := req.Validate(); err != nil {
		return zero, err
	}
	p, call, release, err := c.beginRecoveryControl(ctx, req.ProjectID)
	if err != nil {
		return zero, err
	}
	defer release()
	q := p.jobs
	if err = q.locked(call); err != nil {
		return zero, err
	}
	defer q.unlock()
	cursor := recoveryJobsCursor{ProjectID: req.ProjectID, Generation: q.state.Generation, Digest: q.fileHash}
	if req.Cursor != "" {
		body, e := base64.RawURLEncoding.DecodeString(req.Cursor)
		if e != nil || authority.DecodeJSON(body, &cursor) != nil || cursor.ProjectID != req.ProjectID || !cursor.Last.Valid() || !validDigest(cursor.Digest) || cursor.Generation < 1 || cursor.Generation > math.MaxInt64 {
			return zero, authority.Error{Code: authority.InvalidContract}
		}
		if cursor.Generation != q.state.Generation || cursor.Digest != q.fileHash {
			return zero, authority.Error{Code: authority.Conflict}
		}
	}
	page := RecoveryJobsPage{Schema: 1, ProjectID: req.ProjectID, Generation: q.state.Generation, Retained: len(q.state.Jobs), Jobs: []RecoveryJobView{}}
	more := false
	for _, r := range q.state.Jobs {
		if call.Err() != nil {
			return zero, call.Err()
		}
		if r.View.JobID <= cursor.Last {
			continue
		}
		if len(page.Jobs) >= req.Limit {
			more = true
			break
		}
		page.Jobs = append(page.Jobs, cloneRecoveryRecord(r).View)
		body, _ := json.Marshal(page)
		if len(body) > MaxRecoveryJobPageBytes-1024 {
			page.Jobs = page.Jobs[:len(page.Jobs)-1]
			if len(page.Jobs) == 0 {
				return zero, Error{Capacity}
			}
			more = true
			break
		}
		cursor.Last = r.View.JobID
	}
	if more {
		body, _ := json.Marshal(cursor)
		page.NextCursor = base64.RawURLEncoding.EncodeToString(body)
	}
	if page.Validate() != nil {
		return zero, Error{JobsCorrupt}
	}
	return page, nil
}
