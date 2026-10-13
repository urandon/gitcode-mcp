package localauthority

import (
	"errors"
	"strings"
	"testing"
)

func TestAccountMetadataIsBoundedAndHasNoEnvironmentFallback(t *testing.T) {
	t.Setenv("HOME", "/synthetic/incorrect-home")
	t.Setenv("USER", "incorrect-account")
	for _, tc := range []struct {
		name, input, want string
	}{
		{"exact uid", "root:x:0:0::/root:/bin/sh\nfixture:x:123:123::/synthetic/account:/bin/sh\n", "/synthetic/account"},
		{"missing", "root:x:0:0::/root:/bin/sh\n", ""},
		{"duplicate", "a:x:123:123::/synthetic/a:/bin/sh\nb:x:123:123::/synthetic/b:/bin/sh\n", ""},
		{"relative", "a:x:123:123::relative:/bin/sh\n", ""},
		{"unclean", "a:x:123:123::/synthetic/../account:/bin/sh\n", ""},
		{"control", "a:x:123:123::/synthetic/\raccount:/bin/sh\n", ""},
		{"malformed", "a:x:not-uid:123::/synthetic/a:/bin/sh\n", ""},
		{"invalid utf8", "\xff", ""},
		{"over budget", strings.Repeat("#", accountMetadataBytes+1), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, err := homeFromPasswd(strings.NewReader(tc.input), 123)
			if tc.want != "" {
				if err != nil || home != tc.want {
					t.Fatal("exact local account metadata did not select its home")
				}
				return
			}
			var typed Error
			if home != "" || !errors.As(err, &typed) || typed.Code != DurabilityUnavailable {
				t.Fatal("invalid account data selected an environment or alternate root")
			}
			if strings.Contains(err.Error(), "synthetic") {
				t.Fatal("account error leaked metadata")
			}
		})
	}
	if home, err := homeFromPasswd(strings.NewReader(""), -1); home != "" || err == nil {
		t.Fatal("negative account id accepted")
	}
}
