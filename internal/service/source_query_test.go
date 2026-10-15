package service

import (
	"errors"
	"testing"
)

func TestSourceQueryPage(t *testing.T) {
	for _, tc := range []struct {
		name                string
		limit, offset, want int
		field               string
	}{
		{"omitted", 0, 0, 20, ""},
		{"first", 1, 0, 1, ""},
		{"max", 100, 25, 100, ""},
		{"negative limit", -1, 0, 0, "limit"},
		{"over limit", 101, 0, 0, "limit"},
		{"negative offset", 20, -1, 0, "offset"},
		{"overflow", 20, int(^uint(0) >> 1), 0, "offset"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SourceQueryPage(tc.limit, tc.offset)
			if tc.field == "" {
				if err != nil || got != tc.want {
					t.Fatal("pagination default/bound differs")
				}
				return
			}
			var invalid ErrInvalidQuery
			if !errors.As(err, &invalid) || invalid.Field != tc.field {
				t.Fatal("missing typed pagination error")
			}
		})
	}
}
