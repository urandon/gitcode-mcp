package localauthority

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"math"

	"gitcode-mcp/internal/authority"
)

const MaxIndexJobPageBytes = 64 << 10
const MaxIndexJobPageSize = 32

type IndexJobLookup struct {
	ProjectID authority.UUID `json:"project_id"`
	JobID     authority.UUID `json:"job_id"`
}

func (r IndexJobLookup) Validate() error {
	if !r.ProjectID.Valid() || !r.JobID.Valid() {
		return authority.Error{Code: authority.InvalidContract}
	}
	return nil
}

// Admission binds the exact original caller intent without reflecting its key.
// This is a transport consistency digest, not an authentication signature or
// vector-readiness proof. Private peer authentication remains mandatory.
type IndexJobAdmission struct {
	Schema        int            `json:"schema"`
	ProjectID     authority.UUID `json:"project_id"`
	RequestDigest string         `json:"request_digest"`
	Job           IndexJobView   `json:"job"`
}

func (a IndexJobAdmission) Validate() error {
	if a.Schema != 1 || !a.ProjectID.Valid() || !validDigest(a.RequestDigest) || a.Job.Validate() != nil || a.Job.ProjectID != a.ProjectID {
		return authority.Error{Code: authority.InvalidContract}
	}
	return nil
}

func indexRequestDigest(r IndexJobRequest) string {
	body, _ := json.Marshal(r)
	return hash(append([]byte("authority.v1/content_catalog/admission\x00"), body...))
}

func VerifyIndexJobAdmission(r IndexJobRequest, a IndexJobAdmission) error {
	if r.Validate() != nil || a.Validate() != nil || a.ProjectID != r.ProjectID || a.Job.Sequence != r.Sequence || a.RequestDigest != indexRequestDigest(r) {
		return authority.Error{Code: authority.InvalidContract}
	}
	return nil
}

func (c *Coordinator) AdmitIndex(ctx context.Context, r IndexJobRequest, actor Actor) (IndexJobAdmission, error) {
	v, err := c.EnqueueIndex(ctx, r, actor)
	if err != nil {
		return IndexJobAdmission{}, err
	}
	a := IndexJobAdmission{Schema: 1, ProjectID: r.ProjectID, RequestDigest: indexRequestDigest(r), Job: v}
	if VerifyIndexJobAdmission(r, a) != nil {
		return IndexJobAdmission{}, Error{IndexJobsCorrupt}
	}
	return a, nil
}

type IndexJobsRequest struct {
	ProjectID authority.UUID `json:"project_id"`
	Limit     int            `json:"limit"`
	Cursor    string         `json:"cursor,omitempty"`
}

func (r IndexJobsRequest) Validate() error {
	if !r.ProjectID.Valid() || r.Limit < 1 || r.Limit > MaxIndexJobPageSize || len(r.Cursor) > 512 {
		return authority.Error{Code: authority.InvalidContract}
	}
	if r.Cursor != "" {
		cursor, err := decodeIndexJobsCursor(r.Cursor)
		if err != nil || cursor.ProjectID != r.ProjectID {
			return authority.Error{Code: authority.InvalidContract}
		}
	}
	return nil
}

type IndexJobsPage struct {
	Schema         int            `json:"schema"`
	ProjectID      authority.UUID `json:"project_id"`
	State          string         `json:"state"`
	Generation     uint64         `json:"generation"`
	SnapshotDigest string         `json:"snapshot_digest"`
	Retained       int            `json:"retained"`
	Offset         int            `json:"offset"`
	Jobs           []IndexJobView `json:"jobs"`
	NextCursor     string         `json:"next_cursor,omitempty"`
}

func (p IndexJobsPage) Validate() error {
	invalid := authority.Error{Code: authority.InvalidContract}
	if p.Schema != 1 || !p.ProjectID.Valid() || p.Jobs == nil || len(p.Jobs) > MaxIndexJobPageSize || p.Retained < 0 || p.Retained > maxIndexJobs || p.Offset < 0 || p.Offset > p.Retained || p.Offset+len(p.Jobs) > p.Retained || len(p.NextCursor) > 512 {
		return invalid
	}
	if p.State == "missing" {
		if p.Generation != 0 || p.SnapshotDigest != "" || p.Retained != 0 || p.Offset != 0 || len(p.Jobs) != 0 || p.NextCursor != "" {
			return invalid
		}
		return nil
	}
	if p.State != "ready" || p.Generation < 1 || p.Generation > math.MaxInt64 || !validDigest(p.SnapshotDigest) || uint64(p.Retained) >= p.Generation {
		return invalid
	}
	var last authority.UUID
	active := 0
	for _, v := range p.Jobs {
		if v.Validate() != nil || v.ProjectID != p.ProjectID || v.JobID <= last || v.Generation >= p.Generation {
			return invalid
		}
		last = v.JobID
		if v.State == "queued" || v.State == "running" || v.State == "cancelling" {
			active++
		}
	}
	if active > 1 {
		return invalid
	}
	if p.NextCursor != "" {
		cursor, err := decodeIndexJobsCursor(p.NextCursor)
		if err != nil || len(p.Jobs) == 0 || cursor.ProjectID != p.ProjectID || cursor.Generation != p.Generation || cursor.Digest != p.SnapshotDigest || cursor.Last != last || cursor.Total != p.Retained || cursor.Seen != p.Offset+len(p.Jobs) || cursor.Seen >= p.Retained {
			return invalid
		}
	} else if p.Offset+len(p.Jobs) != p.Retained {
		return invalid
	}
	body, err := json.Marshal(p)
	if err != nil || len(body) > MaxIndexJobPageBytes {
		return invalid
	}
	return nil
}

type indexJobsCursor struct {
	ProjectID  authority.UUID `json:"project_id"`
	Generation uint64         `json:"generation"`
	Digest     string         `json:"digest"`
	Last       authority.UUID `json:"last"`
	Seen       int            `json:"seen"`
	Total      int            `json:"total"`
}

func decodeIndexJobsCursor(encoded string) (indexJobsCursor, error) {
	var cursor indexJobsCursor
	if len(encoded) > 512 {
		return cursor, authority.Error{Code: authority.InvalidContract}
	}
	body, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || authority.DecodeJSON(body, &cursor) != nil || !cursor.ProjectID.Valid() || !cursor.Last.Valid() || !validDigest(cursor.Digest) || cursor.Generation < 1 || cursor.Generation > math.MaxInt64 || cursor.Total < 1 || cursor.Total > maxIndexJobs || cursor.Seen < 1 || cursor.Seen >= cursor.Total {
		return indexJobsCursor{}, authority.Error{Code: authority.InvalidContract}
	}
	return cursor, nil
}

func VerifyIndexJobsPage(req IndexJobsRequest, page IndexJobsPage) error {
	invalid := authority.Error{Code: authority.InvalidContract}
	if req.Validate() != nil || page.Validate() != nil || page.ProjectID != req.ProjectID || len(page.Jobs) > req.Limit {
		return invalid
	}
	if req.Cursor == "" {
		if page.Offset != 0 {
			return invalid
		}
		return nil
	}
	prior, err := decodeIndexJobsCursor(req.Cursor)
	if err != nil || prior.ProjectID != req.ProjectID || prior.Generation != page.Generation || prior.Digest != page.SnapshotDigest || prior.Total != page.Retained || prior.Seen != page.Offset || len(page.Jobs) == 0 || page.Jobs[0].JobID <= prior.Last {
		return invalid
	}
	return nil
}

func (c *Coordinator) IndexJobs(ctx context.Context, req IndexJobsRequest) (IndexJobsPage, error) {
	var zero IndexJobsPage
	if req.Validate() != nil {
		return zero, authority.Error{Code: authority.InvalidContract}
	}
	p, call, release, err := c.beginIndexControl(ctx, req.ProjectID)
	if err != nil {
		return zero, err
	}
	defer release()
	q := p.indexJobs
	if err = q.lock(call); err != nil {
		return zero, err
	}
	defer q.mu.Unlock()
	page := IndexJobsPage{Schema: 1, ProjectID: req.ProjectID, State: "missing", Jobs: []IndexJobView{}}
	if q.state.Generation == 0 {
		if req.Cursor != "" {
			if cursor, e := decodeIndexJobsCursor(req.Cursor); e != nil || cursor.ProjectID != req.ProjectID {
				return zero, authority.Error{Code: authority.InvalidContract}
			}
			return zero, authority.Error{Code: authority.Conflict}
		}
		return page, nil
	}
	cursor := indexJobsCursor{ProjectID: req.ProjectID, Generation: q.state.Generation, Digest: q.fileHash, Total: len(q.state.Jobs)}
	if req.Cursor != "" {
		var e error
		cursor, e = decodeIndexJobsCursor(req.Cursor)
		if e != nil || cursor.ProjectID != req.ProjectID {
			return zero, authority.Error{Code: authority.InvalidContract}
		}
		if cursor.Generation != q.state.Generation || cursor.Digest != q.fileHash {
			return zero, authority.Error{Code: authority.Conflict}
		}
		found := false
		for i, r := range q.state.Jobs {
			if r.Selection.BuildID == cursor.Last {
				found = i+1 == cursor.Seen && cursor.Total == len(q.state.Jobs)
				break
			}
		}
		if !found {
			return zero, authority.Error{Code: authority.InvalidContract}
		}
	}
	page.State, page.Generation, page.SnapshotDigest = "ready", q.state.Generation, q.fileHash
	page.Retained, page.Offset = len(q.state.Jobs), cursor.Seen
	for _, r := range q.state.Jobs[cursor.Seen:] {
		if call.Err() != nil {
			return zero, call.Err()
		}
		if len(page.Jobs) >= req.Limit {
			break
		}
		page.Jobs = append(page.Jobs, indexJobView(r))
		cursor.Last, cursor.Seen = r.Selection.BuildID, cursor.Seen+1
	}
	if cursor.Seen < cursor.Total {
		body, _ := json.Marshal(cursor)
		page.NextCursor = base64.RawURLEncoding.EncodeToString(body)
	}
	if VerifyIndexJobsPage(req, page) != nil {
		return zero, Error{IndexJobsCorrupt}
	}
	return page, nil
}
