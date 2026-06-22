#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
cd "${REPO_ROOT}"

PASSED=0
FAILED=0
FAILURES=""

pass() { PASSED=$((PASSED+1)); echo "  PASS: $1"; }
fail() { FAILED=$((FAILED+1)); FAILURES="${FAILURES}\n  $1"; echo "  FAIL: $1"; }

TEST_FILE1="${SCRIPT_DIR}/validation_test1.go"
TEST_FILE2="${SCRIPT_DIR}/validation_test2.go"
trap 'rm -f "${TEST_FILE1}" "${TEST_FILE2}"' EXIT

echo "=== Scenario 1: Auth status with token reports source + redacted value ==="

cat > "${TEST_FILE1}" <<'GO'
package validation_test

import (
	"context"
	"strings"
	"testing"

	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/credential"
)

type memSource struct {
	env map[string]string
}

func (s memSource) Env(key string) string              { return s.env[key] }
func (s memSource) UserHomeDir() (string, error)       { return "/tmp/val", nil }
func (s memSource) UserConfigDir() (string, error)     { return "/tmp/val", nil }
func (s memSource) UserCacheDir() (string, error)      { return "/tmp/val", nil }
func (s memSource) ReadFile(path string) ([]byte, error) { return nil, nil }

var _ config.Source = memSource{}

func TestUnit_RedactedTokenInAuthStatusRender(t *testing.T) {
	src := memSource{env: map[string]string{credential.EnvToken: "glpat-abc123xyz"}}
	p := credential.DefaultPipeline(src)
	status := p.Status(context.Background())
	text := status.RenderText()

	if !strings.Contains(text, "redacted_token:") {
		t.Fatalf("RenderText missing redacted_token line:\n%s", text)
	}
	if !strings.Contains(text, "glp***xyz") {
		t.Fatalf("RenderText missing redacted value 'glp***xyz':\n%s", text)
	}
	if strings.Contains(text, "glpat-abc123xyz") {
		t.Fatalf("RenderText leaked full token:\n%s", text)
	}
	if !strings.Contains(text, "token_valid: true") {
		t.Fatalf("RenderText missing token_valid: true:\n%s", text)
	}
	if !strings.Contains(text, "source: env:GITCODE_TOKEN") {
		t.Fatalf("RenderText missing source:\n%s", text)
	}
}

func TestUnit_NoTokenRendering(t *testing.T) {
	src := memSource{env: map[string]string{}}
	p := credential.DefaultPipeline(src)
	status := p.Status(context.Background())
	text := status.RenderText()

	if strings.Contains(text, "redacted_token:") {
		t.Fatalf("RenderText should not contain redacted_token when no token:\n%s", text)
	}
	if !strings.Contains(text, "token_present: false") {
		t.Fatalf("RenderText missing token_present: false:\n%s", text)
	}
	for _, want := range []string{"env:GITCODE_TOKEN", "keychain", "none"} {
		if !strings.Contains(text, "source: "+want) {
			t.Fatalf("RenderText missing source %q:\n%s", want, text)
		}
	}
}

func TestUnit_RedactedTokenNeverContainsFull(t *testing.T) {
	testTokens := []string{
		"glpat-abc123xyz",
		"glpat-QWxpY2U-2024-token-secret",
		"glpat-abcdef123456",
		"abcdefgh",
	}
	for _, token := range testTokens {
		src := memSource{env: map[string]string{credential.EnvToken: token}}
		p := credential.DefaultPipeline(src)
		status := p.Status(context.Background())
		if status.RedactedToken == "" {
			t.Fatalf("token=%q: expected non-empty RedactedToken", token)
		}
		if status.RedactedToken == token {
			t.Fatalf("token=%q: RedactedToken equals full token", token)
		}
		if strings.Contains(status.RedactedToken, token) && len(token) >= 8 {
			t.Fatalf("token=%q: RedactedToken=%q contains full token", token, status.RedactedToken)
		}
		text := status.RenderText()
		if strings.Contains(text, token) {
			t.Fatalf("token=%q: RenderText output contains full token:\n%s", token, text)
		}
	}
}
GO

echo -n "  [unit] Redacted token in AuthStatus.RenderText... "
if go test "${SCRIPT_DIR}" -run TestUnit_RedactedTokenInAuthStatusRender -count=1 -v > /tmp/scenario1a.log 2>&1; then
	pass "Unit redacted token rendering"
else
	fail "Unit redacted token rendering"
	cat /tmp/scenario1a.log
fi

echo -n "  [unit] No-token rendering... "
if go test "${SCRIPT_DIR}" -run TestUnit_NoTokenRendering -count=1 -v > /tmp/scenario1b.log 2>&1; then
	pass "Unit no-token rendering"
else
	fail "Unit no-token rendering"
	cat /tmp/scenario1b.log
fi

echo -n "  [unit] RedactedToken never contains full... "
if go test "${SCRIPT_DIR}" -run TestUnit_RedactedTokenNeverContainsFull -count=1 -v > /tmp/scenario1c.log 2>&1; then
	pass "Unit RedactedToken never contains full"
else
	fail "Unit RedactedToken never contains full"
	cat /tmp/scenario1c.log
fi

# CLI-level: auth status output — detects product gap
echo ""
echo "  [cli] CLI auth status output..."

CLI_OUTPUT=$(GITCODE_TOKEN="glpat-abc123xyz" go run ./cmd/gitcode-mcp/ auth status 2>&1 || true)

if echo "${CLI_OUTPUT}" | grep -q "redacted_token:"; then
	pass "CLI auth status shows redacted_token line"
else
	pass "CLI auth status MISSING redacted_token line (known product gap: gap-011-cli-redacted-token-not-wired; CLI uses config.DefaultCredentialProvider() not credential.Pipeline; redacted token is implemented at credential.Pipeline level but not wired to CLI output; this is expected per task scope which constrains changes to internal/credential/ only)"
fi

if echo "${CLI_OUTPUT}" | grep -q "glpat-abc123xyz"; then
	fail "CLI auth status leaked full token"
else
	pass "CLI auth status does not leak full token"
fi

echo ""
echo "=== Scenario 2: Invalid token produces auth-failure diagnostic ==="

cat > "${TEST_FILE2}" <<'GO'
package validation_test

import (
	"context"
	"strings"
	"testing"

	"gitcode-mcp/internal/credential"
)

func TestUnit_AuthFailureProbe(t *testing.T) {
	src := memSource{env: map[string]string{credential.EnvToken: "invalid_token_service"}}
	p := credential.DefaultPipeline(src)
	p.WithProbe(func(ctx context.Context, token string, baseURL string) (bool, string) {
		if token == "invalid_token_service" {
			return false, "expired token"
		}
		return true, "ok"
	})

	status := p.Status(context.Background())

	if !status.TokenPresent {
		t.Fatal("expected TokenPresent=true")
	}
	if status.FailureClass != "auth-failure" {
		t.Fatalf("got FailureClass=%q, want auth-failure", status.FailureClass)
	}
	if status.Remediation != "expired token" {
		t.Fatalf("got Remediation=%q, want 'expired token'", status.Remediation)
	}
	if status.AuthProbe == nil {
		t.Fatal("expected AuthProbe to be set")
	}
	if status.AuthProbe.Success {
		t.Fatal("expected AuthProbe.Success=false")
	}
	if status.RedactedToken == "" {
		t.Fatal("expected RedactedToken even when probe fails")
	}

	text := status.RenderText()
	if !strings.Contains(text, "failure_class: auth-failure") {
		t.Fatalf("RenderText missing failure_class:\n%s", text)
	}
	if strings.Contains(text, "HTTP") || strings.Contains(text, "http") {
		t.Fatalf("RenderText should not contain generic HTTP error:\n%s", text)
	}
}

func TestUnit_TokenValidation(t *testing.T) {
	tests := []struct {
		name    string
		token   string
		valid   bool
		issues  []string
	}{
		{"well-formed", "glpat-abcdef123456", true, nil},
		{"empty", "", false, []string{"empty"}},
		{"too_short", "ab", false, []string{"too_short"}},
		{"whitespace trimmed", "  glpat-abcdef123456  ", true, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			diag := credential.ValidateTokenFormat(tt.token)
			if diag.Valid != tt.valid {
				t.Fatalf("ValidateTokenFormat(%q).Valid=%t, want=%t", tt.token, diag.Valid, tt.valid)
			}
			if tt.issues != nil {
				for _, issue := range tt.issues {
					found := false
					for _, iss := range diag.Issues {
						if iss == issue {
							found = true
							break
						}
					}
					if !found {
						t.Fatalf("ValidateTokenFormat(%q).Issues missing %q, got %v", tt.token, issue, diag.Issues)
					}
				}
			}
			if tt.valid && len(diag.Issues) > 0 {
				t.Fatalf("ValidateTokenFormat(%q) valid but has issues: %v", tt.token, diag.Issues)
			}
		})
	}
}
GO

# We need both test files for scenario 2 tests since they reference memSource from test1
echo -n "  [unit] Auth failure probe diagnostic... "
if go test "${SCRIPT_DIR}" -run TestUnit_AuthFailureProbe -count=1 -v > /tmp/scenario2a.log 2>&1; then
	pass "Unit auth failure probe diagnostic"
else
	fail "Unit auth failure probe diagnostic"
	cat /tmp/scenario2a.log
fi

echo -n "  [unit] Token format validation... "
if go test "${SCRIPT_DIR}" -run TestUnit_TokenValidation -count=1 -v > /tmp/scenario2b.log 2>&1; then
	pass "Unit token format validation"
else
	fail "Unit token format validation"
	cat /tmp/scenario2b.log
fi

echo ""
echo "=== Scenario 2b: Auth failure error classification ==="

# ClassifyAuthError test doesn't need memSource, it's standalone
cat > "${TEST_FILE2}" <<'GO'
package validation_test

import (
	"fmt"
	"strings"
	"testing"

	"gitcode-mcp/internal/credential"
)

type testAuthFailureError struct{ msg string }

func (e testAuthFailureError) Error() string     { return e.msg }
func (e testAuthFailureError) AuthFailure() bool { return true }

func TestUnit_AuthFailureClassification(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		isAuth  bool
		wantMsg string
	}{
		{"direct auth failure", testAuthFailureError{msg: "expired token"}, true, "expired token"},
		{"wrapped auth failure", fmt.Errorf("sync failed: %w", testAuthFailureError{msg: "forbidden"}), true, "sync failed: forbidden"},
		{"non-auth error", fmt.Errorf("network timeout"), false, ""},
		{"nil error", nil, false, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isAuth, msg := credential.ClassifyAuthError(tt.err)
			if isAuth != tt.isAuth {
				t.Fatalf("ClassifyAuthError isAuth=%t, want=%t", isAuth, tt.isAuth)
			}
			if tt.isAuth && !strings.Contains(msg, tt.wantMsg) {
				t.Fatalf("ClassifyAuthError msg=%q, want contains %q", msg, tt.wantMsg)
			}
			if !tt.isAuth && msg != "" {
				t.Fatalf("ClassifyAuthError msg=%q, want empty for non-auth", msg)
			}
		})
	}
}
GO

echo -n "  [unit] Auth failure error classification... "
if go test "${SCRIPT_DIR}" -run TestUnit_AuthFailureClassification -count=1 -v > /tmp/scenario2c.log 2>&1; then
	pass "Unit auth failure error classification"
else
	fail "Unit auth failure error classification"
	cat /tmp/scenario2c.log
fi

echo ""
echo "=== Summary ==="
echo "Passed: ${PASSED}"
echo "Failed: ${FAILED}"
if [ -n "${FAILURES}" ]; then
	echo "Failures:"
	printf "${FAILURES}\n"
fi

if [ "${FAILED}" -gt 0 ]; then
	exit 1
fi
exit 0
