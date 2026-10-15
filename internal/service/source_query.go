package service

// SourceQueryPage is the shared public source-list/search pagination contract.
// Zero is the service-level omission sentinel; transports reject explicit zero.
const (
	DefaultSourceQueryLimit = 20
	MaxSourceQueryLimit     = 100
)

func SourceQueryPage(limit, offset int) (int, error) {
	if limit == 0 {
		limit = DefaultSourceQueryLimit
	}
	if limit < 1 || limit > MaxSourceQueryLimit {
		return 0, ErrInvalidQuery{Field: "limit", Message: "limit must be between 1 and 100"}
	}
	// Prevent limit+offset integer overflow before storage/query allocation.
	if offset < 0 || offset > int(^uint(0)>>1)-limit {
		return 0, ErrInvalidQuery{Field: "offset", Message: "offset must be non-negative and leave room for limit"}
	}
	return limit, nil
}
