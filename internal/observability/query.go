package observability

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
)

type Filter struct {
	Stream                  string `json:"stream,omitempty"`
	Severity                string `json:"severity,omitempty"`
	Component               string `json:"component,omitempty"`
	Code                    Code   `json:"code,omitempty"`
	Phase                   string `json:"phase,omitempty"`
	Collection              string `json:"collection,omitempty"`
	Outcome                 string `json:"outcome,omitempty"`
	FailureClass            string `json:"failure_class,omitempty"`
	JobRef                  string `json:"job_ref,omitempty"`
	RegistrationRef         string `json:"registration_ref,omitempty"`
	CacheRef                string `json:"cache_ref,omitempty"`
	RepoRef                 string `json:"repo_ref,omitempty"`
	CorrelationRef          string `json:"correlation_ref,omitempty"`
	StageRef                string `json:"stage_ref,omitempty"`
	SourceRef               string `json:"source_ref,omitempty"`
	PreviousRegistrationRef string `json:"previous_registration_ref,omitempty"`
	Since                   string `json:"since,omitempty"`
	Until                   string `json:"until,omitempty"`
}
type cursor struct {
	Signature  string `json:"h"`
	Schema     int    `json:"s"`
	Generation string `json:"g"`
	Position   uint64 `json:"p"`
	Filter     string `json:"f"`
	Loss       uint64 `json:"l"`
}
type QueryError struct {
	Code     string `json:"code"`
	Reason   string `json:"reason"`
	Earliest uint64 `json:"earliest,omitempty"`
	Latest   uint64 `json:"latest,omitempty"`
}

func (e QueryError) Error() string          { return e.Code + ": " + e.Reason }
func (e QueryError) DiagnosticCode() string { return e.Code }

type Page struct {
	Events   []Event  `json:"events"`
	Cursor   string   `json:"cursor"`
	HasMore  bool     `json:"has_more"`
	Coverage Coverage `json:"coverage"`
}

func normalizeFilter(f Filter) (Filter, string, error) {
	f.Stream = strings.ToLower(strings.TrimSpace(f.Stream))
	f.Severity = strings.ToLower(strings.TrimSpace(f.Severity))
	f.Component = strings.ToLower(strings.TrimSpace(f.Component))
	var err error
	f, err = validateFilter(f)
	if err != nil {
		return f, "", err
	}
	b, _ := json.Marshal(f)
	sum := sha256.Sum256(b)
	return f, hex.EncodeToString(sum[:16]), nil
}
func (f Filter) match(e Event) bool {
	return f.matchSelectors(e) && (f.Since == "" || !e.OccurredAt.Before(parseFilterTime(f.Since))) && (f.Until == "" || !e.OccurredAt.After(parseFilterTime(f.Until)))
}
func (c *Collector) Query(f Filter, token string, limit int) (Page, error) {
	return c.query(context.Background(), f, token, limit, false)
}

// QueryContext is the transport read boundary. A busy collector is unavailable,
// not a reason to block a poll or leave a timeout goroutine behind. Validation
// precedes observation access; formatting uses an independent retained copy.
func (c *Collector) QueryContext(ctx context.Context, f Filter, token string, limit int) (Page, error) {
	return c.query(ctx, f, token, limit, true)
}

func (c *Collector) query(ctx context.Context, f Filter, token string, limit int, nonblocking bool) (Page, error) {
	f, fingerprint, err := normalizeFilter(f)
	if err != nil || limit < 1 || limit > MaxPageEvents {
		return Page{}, QueryError{Code: "invalid_query", Reason: "unsupported_filter_or_limit"}
	}
	supplied, err := readCursor(token, fingerprint)
	if err != nil {
		return Page{}, err
	}
	if err := ctx.Err(); err != nil {
		return Page{}, err
	}
	if nonblocking {
		if !c.mu.TryLock() {
			return Page{}, QueryError{Code: "observation_busy", Reason: "ledger_busy"}
		}
	} else {
		c.mu.Lock()
	}
	if err := ctx.Err(); err != nil {
		c.mu.Unlock()
		return Page{}, err
	}
	c.absorbLossLocked()
	events := append([]Event(nil), c.events...)
	generation := c.generation
	coverage := c.coverage
	key := c.cursorKey
	latest := uint64(0)
	if len(events) > 0 {
		latest = events[len(events)-1].Sequence
	}
	c.mu.Unlock()
	if generation == "" {
		return Page{}, QueryError{Code: "snapshot_required", Reason: "initializing"}
	}
	if len(events) == 0 && coverage.State == "ready" {
		return Page{}, QueryError{Code: "snapshot_required", Reason: "initializing"}
	}
	earliest := latest + 1
	if len(events) > 0 {
		earliest = events[0].Sequence
	}
	cur := cursor{Schema: 1, Generation: generation, Filter: fingerprint, Loss: coverage.LossEpoch, Position: earliest - 1}
	if token != "" {
		cur = supplied
		if !validSignature(cur, key) {
			return Page{}, QueryError{Code: "invalid_cursor", Reason: "foreign_or_modified"}
		}
		if cur.Generation != generation || cur.Loss != coverage.LossEpoch {
			return Page{}, QueryError{Code: "snapshot_required", Reason: "generation_or_loss_changed", Earliest: earliest, Latest: latest}
		}
		if cur.Position > latest {
			return Page{}, QueryError{Code: "invalid_cursor", Reason: "future_position"}
		}
		if cur.Position+1 < earliest {
			return Page{}, QueryError{Code: "snapshot_required", Reason: "retention_expired", Earliest: earliest, Latest: latest}
		}
	}
	p := Page{Events: []Event{}, Coverage: coverage}
	scanned, bytes := 0, 0
	for _, e := range events {
		if err := ctx.Err(); err != nil {
			return Page{}, err
		}
		if e.Sequence <= cur.Position {
			continue
		}
		encoded, _ := json.Marshal(e)
		if scanned+len(encoded)+1 > MaxPageBytes-4096 {
			break
		}
		if f.match(e) && (len(p.Events) == limit || bytes+len(encoded)+1 > MaxPageBytes-4096) {
			break
		}
		scanned += len(encoded) + 1
		cur.Position = e.Sequence
		if f.match(e) {
			p.Events = append(p.Events, e)
			bytes += len(encoded) + 1
		}
	}
	p.HasMore = cur.Position < latest
	cur.Signature = signCursor(cur, key)
	b, _ := json.Marshal(cur)
	p.Cursor = base64.RawURLEncoding.EncodeToString(b)
	if err := ctx.Err(); err != nil {
		return Page{}, err
	}
	return p, nil
}

func readCursor(token, fingerprint string) (cursor, error) {
	var supplied cursor
	if token == "" {
		return supplied, nil
	}
	if len(token) > 512 {
		return supplied, QueryError{Code: "invalid_cursor", Reason: "malformed"}
	}
	b, e := base64.RawURLEncoding.DecodeString(token)
	if e != nil || DecodeReadRequest(b, &supplied) != nil || supplied.Schema != 1 || !hexID.MatchString(supplied.Generation) || !hexID.MatchString(supplied.Filter) || len(supplied.Signature) != 64 {
		return supplied, QueryError{Code: "invalid_cursor", Reason: "malformed"}
	}
	if _, err := hex.DecodeString(supplied.Signature); err != nil {
		return supplied, QueryError{Code: "invalid_cursor", Reason: "malformed"}
	}
	if supplied.Filter != fingerprint {
		return supplied, QueryError{Code: "invalid_cursor", Reason: "filter_changed"}
	}
	return supplied, nil
}
func signCursor(cur cursor, key string) string {
	cur.Signature = ""
	b, _ := json.Marshal(cur)
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write(b)
	return hex.EncodeToString(mac.Sum(nil))
}
func validSignature(cur cursor, key string) bool {
	return hmac.Equal([]byte(cur.Signature), []byte(signCursor(cur, key)))
}
