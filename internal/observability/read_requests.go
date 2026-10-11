package observability

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
)

const MaxReadRequestBytes = 8 << 10

type EventsRequest struct {
	Filter
	Cursor string `json:"cursor,omitempty"`
	Limit  int    `json:"limit"`
}
type MetricsRequest struct {
	Name           string `json:"name,omitempty"`
	HistoryMinutes int    `json:"history_minutes"`
}

// DecodeReadRequest rejects duplicate keys and non-flat objects, in addition to
// unknown fields/types/trailing values. Errors never echo request contents.
func DecodeReadRequest(raw []byte, dst any) error {
	invalid := QueryError{Code: "invalid_query", Reason: "unsupported_request"}
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	if len(raw) > MaxReadRequestBytes {
		return invalid
	}
	keys, ok := readRequestKeys(dst)
	if !ok {
		return invalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	start, err := d.Token()
	if err != nil || start != json.Delim('{') {
		return invalid
	}
	seen := map[string]bool{}
	for d.More() {
		token, err := d.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[key] || !keys[key] {
			return invalid
		}
		seen[key] = true
		var value json.RawMessage
		if d.Decode(&value) != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return invalid
		}
		v := bytes.TrimSpace(value)
		if len(v) == 0 || v[0] == '{' || v[0] == '[' {
			return invalid
		}
	}
	if end, err := d.Token(); err != nil || end != json.Delim('}') {
		return invalid
	}
	if _, err := d.Token(); !errors.Is(err, io.EOF) {
		return invalid
	}
	if decodeStrict(raw, dst) != nil {
		return invalid
	}
	return nil
}

// encoding/json also matches names case-insensitively. These read contracts
// permit exact JSON-tag names only, so aliases (including Unicode case folds)
// cannot bypass unknown-field or duplicate-key checks. Types are trusted local
// DTOs, never caller-provided schema or lookup authority.
func readRequestKeys(dst any) (map[string]bool, bool) {
	typ := reflect.TypeOf(dst)
	if typ == nil || typ.Kind() != reflect.Pointer || typ.Elem().Kind() != reflect.Struct || reflect.ValueOf(dst).IsNil() {
		return nil, false
	}
	keys := map[string]bool{}
	var add func(reflect.Type) bool
	add = func(typ reflect.Type) bool {
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			if f.PkgPath != "" {
				continue
			}
			name := strings.SplitN(f.Tag.Get("json"), ",", 2)[0]
			if name == "-" {
				continue
			}
			if f.Anonymous && name == "" && f.Type.Kind() == reflect.Struct {
				if !add(f.Type) {
					return false
				}
				continue
			}
			if name == "" || keys[name] {
				return false
			}
			keys[name] = true
		}
		return true
	}
	return keys, add(typ.Elem())
}

func DecodeEventsRequest(raw []byte) (EventsRequest, error) {
	req := EventsRequest{Limit: 50}
	if err := DecodeReadRequest(raw, &req); err != nil {
		return EventsRequest{}, err
	}
	return req.Validate()
}
func (r EventsRequest) Validate() (EventsRequest, error) {
	f, hash, err := normalizeFilter(r.Filter)
	if err != nil || r.Limit < 1 || r.Limit > MaxPageEvents {
		return r, QueryError{Code: "invalid_query", Reason: "unsupported_filter_or_limit"}
	}
	r.Filter = f
	if _, err := readCursor(r.Cursor, hash); err != nil {
		return r, err
	}
	return r, nil
}
func DecodeMetricsRequest(raw []byte) (MetricsRequest, error) {
	req := MetricsRequest{HistoryMinutes: MetricHistoryMinutes}
	if err := DecodeReadRequest(raw, &req); err != nil {
		return MetricsRequest{}, err
	}
	return req.Validate()
}
func (r MetricsRequest) Validate() (MetricsRequest, error) {
	if r.HistoryMinutes < 1 || r.HistoryMinutes > MetricHistoryMinutes {
		return r, QueryError{Code: "invalid_query", Reason: "unsupported_metric_or_window"}
	}
	if r.Name != "" {
		for _, d := range metricDefinitions() {
			if d.Name == r.Name {
				return r, nil
			}
		}
		return r, QueryError{Code: "invalid_query", Reason: "unsupported_metric_or_window"}
	}
	return r, nil
}
