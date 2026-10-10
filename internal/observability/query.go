package observability

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
)

type Filter struct {
	Stream    string `json:"stream,omitempty"`
	Severity  string `json:"severity,omitempty"`
	Component string `json:"component,omitempty"`
	Code      Code   `json:"code,omitempty"`
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

func (e QueryError) Error() string { return e.Code + ": " + e.Reason }

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
	if f.Stream != "" && f.Stream != "stdout" && f.Stream != "stderr" {
		return f, "", errors.New("invalid filter")
	}
	if f.Severity != "" && f.Severity != "info" && f.Severity != "warning" && f.Severity != "error" {
		return f, "", errors.New("invalid filter")
	}
	if f.Component != "" && f.Component != "daemon" && f.Component != "recovery" && f.Component != "legacy" {
		return f, "", errors.New("invalid filter")
	}
	if f.Code != "" {
		if _, ok := catalog[f.Code]; !ok {
			return f, "", errors.New("invalid filter")
		}
	}
	b, _ := json.Marshal(f)
	sum := sha256.Sum256(b)
	return f, hex.EncodeToString(sum[:16]), nil
}
func (f Filter) match(e Event) bool {
	return (f.Stream == "" || e.Stream == f.Stream) && (f.Severity == "" || e.Severity == f.Severity) && (f.Component == "" || e.Component == f.Component) && (f.Code == "" || e.Code == f.Code)
}
func (c *Collector) Query(f Filter, token string, limit int) (Page, error) {
	f, fingerprint, err := normalizeFilter(f)
	if err != nil || limit < 1 || limit > MaxPageEvents {
		return Page{}, QueryError{Code: "invalid_query", Reason: "unsupported_filter_or_limit"}
	}
	c.mu.Lock()
	events := c.events
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
		if len(token) > 512 {
			return Page{}, QueryError{Code: "invalid_cursor", Reason: "malformed"}
		}
		b, e := base64.RawURLEncoding.DecodeString(token)
		if e != nil || decodeStrict(b, &cur) != nil || cur.Schema != 1 || !hexID.MatchString(cur.Generation) || !hexID.MatchString(cur.Filter) {
			return Page{}, QueryError{Code: "invalid_cursor", Reason: "malformed"}
		}
		if cur.Filter != fingerprint {
			return Page{}, QueryError{Code: "invalid_cursor", Reason: "filter_changed"}
		}
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
	return p, nil
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
