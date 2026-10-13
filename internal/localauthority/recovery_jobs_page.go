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
	Schema         int               `json:"schema"`
	ProjectID      authority.UUID    `json:"project_id"`
	Generation     uint64            `json:"generation"`
	SnapshotDigest string            `json:"snapshot_digest"`
	Retained       int               `json:"retained"`
	Offset         int               `json:"offset"`
	Jobs           []RecoveryJobView `json:"jobs"`
	NextCursor     string            `json:"next_cursor,omitempty"`
}

func (p RecoveryJobsPage) Validate() error {
	if p.Schema != 1 || !p.ProjectID.Valid() || p.Generation < 1 || p.Generation > math.MaxInt64 || !validDigest(p.SnapshotDigest) || p.Retained < 0 || p.Retained > MaxRecoveryJobs || p.Offset < 0 || p.Offset > p.Retained || p.Jobs == nil || len(p.Jobs) > 100 || p.Offset+len(p.Jobs) > p.Retained || len(p.NextCursor) > 512 {
		return authority.Error{Code: authority.InvalidContract}
	}
	if uint64(p.Retained) >= p.Generation {
		return authority.Error{Code: authority.InvalidContract}
	}
	var last authority.UUID
	active := 0
	for _, v := range p.Jobs {
		if v.Validate() != nil || v.ProjectID != p.ProjectID || v.JobID <= last || v.Generation >= p.Generation {
			return authority.Error{Code: authority.InvalidContract}
		}
		last = v.JobID
		if v.State == "queued" || v.State == "running" || v.State == "cancelling" {
			active++
		}
	}
	if active > 1 {
		return authority.Error{Code: authority.InvalidContract}
	}
	if p.NextCursor != "" {
		cursor, err := decodeRecoveryJobsCursor(p.NextCursor)
		if err != nil || len(p.Jobs) == 0 || cursor.ProjectID != p.ProjectID || cursor.Generation != p.Generation || cursor.Digest != p.SnapshotDigest || cursor.Last != last || cursor.Total != p.Retained || cursor.Seen != p.Offset+len(p.Jobs) || cursor.Seen >= p.Retained {
			return authority.Error{Code: authority.InvalidContract}
		}
	} else if p.Offset+len(p.Jobs) != p.Retained {
		return authority.Error{Code: authority.InvalidContract}
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
	Seen       int            `json:"seen"`
	Total      int            `json:"total"`
}

func decodeRecoveryJobsCursor(encoded string) (recoveryJobsCursor, error) {
	var cursor recoveryJobsCursor
	body, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(encoded) > 512 || authority.DecodeJSON(body, &cursor) != nil || !cursor.ProjectID.Valid() || !cursor.Last.Valid() || !validDigest(cursor.Digest) || cursor.Generation < 1 || cursor.Generation > math.MaxInt64 || cursor.Total < 1 || cursor.Total > MaxRecoveryJobs || cursor.Seen < 1 || cursor.Seen > cursor.Total {
		return recoveryJobsCursor{}, authority.Error{Code: authority.InvalidContract}
	}
	return cursor, nil
}

// VerifyRecoveryJobsPage binds protected response continuation to the caller's
// exact query. It inspects no authority and never grants execution permission.
func VerifyRecoveryJobsPage(req RecoveryJobsRequest, page RecoveryJobsPage) error {
	invalid := authority.Error{Code: authority.InvalidContract}
	if req.Validate() != nil || page.Validate() != nil || page.ProjectID != req.ProjectID || len(page.Jobs) > req.Limit {
		return invalid
	}
	if req.Cursor != "" {
		prior, err := decodeRecoveryJobsCursor(req.Cursor)
		if err != nil || prior.ProjectID != req.ProjectID || prior.Generation != page.Generation || prior.Digest != page.SnapshotDigest || prior.Total != page.Retained || prior.Seen != page.Offset || len(page.Jobs) > 0 && page.Jobs[0].JobID <= prior.Last {
			return invalid
		}
	} else if page.Offset != 0 {
		return invalid
	}
	return nil
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
	cursor := recoveryJobsCursor{ProjectID: req.ProjectID, Generation: q.state.Generation, Digest: q.fileHash, Total: len(q.state.Jobs)}
	if req.Cursor != "" {
		var e error
		cursor, e = decodeRecoveryJobsCursor(req.Cursor)
		if e != nil || cursor.ProjectID != req.ProjectID {
			return zero, authority.Error{Code: authority.InvalidContract}
		}
		if cursor.Generation != q.state.Generation || cursor.Digest != q.fileHash {
			return zero, authority.Error{Code: authority.Conflict}
		}
		if i, found := recoveryRecordIndex(q.state.Jobs, cursor.Last); !found || i+1 != cursor.Seen || cursor.Total != len(q.state.Jobs) {
			return zero, authority.Error{Code: authority.InvalidContract}
		}
	}
	page := RecoveryJobsPage{Schema: 1, ProjectID: req.ProjectID, Generation: q.state.Generation, SnapshotDigest: q.fileHash, Retained: len(q.state.Jobs), Offset: cursor.Seen, Jobs: []RecoveryJobView{}}
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
		cursor.Seen++
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
