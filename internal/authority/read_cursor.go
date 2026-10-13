package authority

import (
	"encoding/base64"
	"strconv"
	"strings"
)

// ReadPageCursor is the protected pagination contract shared by storage and
// transports. It preserves the existing encoding; it contains no source text
// and validating it never opens an authority or advances a snapshot.
type ReadPageCursor struct {
	Project   UUID       `json:"project"`
	Kind      EntityKind `json:"kind"`
	Operation string     `json:"operation"`
	Entity    UUID       `json:"entity"`
	Snapshot  int64      `json:"snapshot"`
	Last      int64      `json:"last"`
}

func (c ReadPageCursor) Validate() error {
	if !c.Project.Valid() || !c.Kind.Valid() || c.Snapshot < 1 || c.Last < 1 || c.Last > c.Snapshot || c.Operation != "list" && c.Operation != "history" || c.Operation == "list" && c.Entity != "" || c.Operation == "history" && !c.Entity.Valid() {
		return Error{InvalidContract}
	}
	return nil
}

// DecodeReadPageCursor binds a nonempty, exact-schema continuation to the
// original request scope. Request.Validate may call this, so do not recurse.
func DecodeReadPageCursor(encoded string, request ReadRequest) (ReadPageCursor, error) {
	var cursor ReadPageCursor
	if encoded == "" || len(encoded) > 512 {
		return cursor, Error{InvalidContract}
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil || base64.RawURLEncoding.EncodeToString(raw) != encoded || DecodeJSON(raw, &cursor) != nil || cursor.Project != request.ProjectID || cursor.Kind != request.Kind || cursor.Operation != request.Operation || cursor.Entity != request.EntityID {
		return ReadPageCursor{}, Error{InvalidContract}
	}
	return cursor, nil
}

// Match validates rows and continuation against a specific protected read,
// including terminal pages. It is pure and applies even without a destination.
func (result ReadResult) Match(request ReadRequest) error {
	bad := Error{InvalidContract}
	if request.Validate() != nil || result.Validate() != nil || result.ProjectID != request.ProjectID || request.Operation == "status" {
		return bad
	}
	limit := request.Limit
	if limit == 0 {
		limit = 50
	}
	if request.Operation == "get" {
		if len(result.Entities) != 1 || result.NextCursor != "" {
			return bad
		}
	} else if len(result.Entities) > limit {
		return bad
	}
	var input, output ReadPageCursor
	var err error
	if request.Cursor != "" {
		input, err = DecodeReadPageCursor(request.Cursor, request)
		if err != nil {
			return bad
		}
	}
	if result.NextCursor != "" {
		output, err = DecodeReadPageCursor(result.NextCursor, request)
		if err != nil || request.Cursor != "" && output.Snapshot != input.Snapshot {
			return bad
		}
	}
	last := uint64(input.Last)
	seen := map[UUID]bool{}
	for _, entity := range result.Entities {
		if entity.Citation.Kind != request.Kind {
			return bad
		}
		alias, err := strconv.ParseUint(entity.DisplayAlias[strings.LastIndexByte(entity.DisplayAlias, '-')+1:], 10, 64)
		// Both the entity alias and this revision consume project sequences,
		// regardless of which ordinal the requested operation paginates on.
		if err != nil || alias > entity.ProjectSequence || entity.Citation.Revision > entity.ProjectSequence {
			return bad
		}
		if request.Cursor != "" && entity.ProjectSequence > uint64(input.Snapshot) || result.NextCursor != "" && entity.ProjectSequence > uint64(output.Snapshot) {
			return bad
		}
		if request.Operation == "get" || request.Operation == "history" {
			if entity.Citation.EntityID != request.EntityID || request.Revision != 0 && entity.Citation.Revision != request.Revision {
				return bad
			}
			if request.Operation == "history" && entity.Citation.Revision <= last {
				return bad
			}
			last = entity.Citation.Revision
		} else {
			if alias <= last || seen[entity.Citation.EntityID] {
				return bad
			}
			last, seen[entity.Citation.EntityID] = alias, true
		}
	}
	if result.NextCursor != "" && (len(result.Entities) == 0 || uint64(output.Last) != last || output.Last <= input.Last) {
		return bad
	}
	return nil
}
