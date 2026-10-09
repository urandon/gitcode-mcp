package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitcode-mcp/internal/service"
)

func TestPRMarkdownStdinPreservesBodyAndWriteIntent(t *testing.T) {
	body := "# Review — результат\n\n```text\npath\\name\n```\n\n"
	for _, command := range []string{"create-pr", "create-mr"} {
		for _, mode := range []string{"--dry-run", "--live"} {
			t.Run(command+mode, func(t *testing.T) {
				spy := &spyService{}
				factory := func(context.Context, string) (queryService, func() error, error) { return spy, nil, nil }
				var stdout, stderr bytes.Buffer
				args := []string{command, "--repo", "fixture-a", "--title", "Review", "--head", "topic", "--base", "main", "--body-file", "-", mode, "--idempotency-key", "pr-body-key", "--format", "json"}
				code := 0
				if mode == "--live" {
					// Exercise live request projection with a spy rather than the
					// real live startup factory (which intentionally ignores spies).
					opts, _, err := parseOptions(command, args[1:])
					if err != nil {
						t.Fatal(err)
					}
					opts, err = resolveMarkdownBodyInput(command, opts, strings.NewReader(body))
					if err != nil {
						t.Fatal(err)
					}
					code = dispatchWrite(context.Background(), spy.CreatePR, "create-pr", opts, &stdout, &stderr, startupPlan{})
				} else {
					code = executeWithFactoryAndDeps(args, &stdout, &stderr, factory, localCommandDeps{Source: newCLIConfigSource(t), Stdin: strings.NewReader(body)})
				}
				if code != 0 {
					t.Fatalf("exit=%d error=%q", code, stderr.String())
				}
				req := spy.lastWriteRequest["CreatePR"]
				wantMode := service.WriteModeDryRun
				if mode == "--live" {
					wantMode = service.WriteModeLive
				}
				if req.Body != body || req.IdempotencyKey != "pr-body-key" || req.Mode != wantMode || req.Head != "topic" || req.Base != "main" {
					t.Fatal("Markdown resolution changed the body or explicit write intent")
				}
				if strings.Contains(stdout.String(), "результат") {
					t.Fatal("output leaked the body")
				}
			})
		}
	}
}

func TestPRMarkdownValidationPrecedesServiceConstruction(t *testing.T) {
	for _, command := range []string{"create-pr", "create-mr"} {
		for _, tc := range []struct {
			name  string
			flags []string
			input string
			want  string
		}{
			{name: "ambiguous", flags: []string{"--body", "inline", "--body-file", "-"}, input: "file", want: "mutually exclusive"},
			{name: "empty", flags: []string{"--body-file", "-"}, want: "body input is empty"},
			{name: "invalid UTF-8", flags: []string{"--body-file", "-"}, input: string([]byte{0xff}), want: "valid UTF-8"},
			{name: "oversized", flags: []string{"--body-file", "-"}, input: strings.Repeat("x", int(maxMarkdownBodyBytes)+1), want: "exceeds"},
			{name: "literal escapes", flags: []string{"--body", `one\n\ntwo`}, want: "multiple literal"},
		} {
			t.Run(command+"/"+tc.name, func(t *testing.T) {
				calls := 0
				factory := func(context.Context, string) (queryService, func() error, error) {
					calls++
					return &spyService{}, nil, nil
				}
				args := append([]string{command, "--repo", "fixture-a", "--title", "Review", "--head", "topic", "--base", "main", "--dry-run", "--format", "json"}, tc.flags...)
				var stdout, stderr bytes.Buffer
				code := executeWithFactoryAndDeps(args, &stdout, &stderr, factory, localCommandDeps{Source: newCLIConfigSource(t), Stdin: strings.NewReader(tc.input)})
				if code == 0 || !strings.Contains(stderr.String(), tc.want) {
					t.Fatalf("exit=%d error=%q", code, stderr.String())
				}
				if calls != 0 {
					t.Fatal("invalid input constructed a service")
				}
			})
		}
	}
}

func TestPRMarkdownWriteModeConflictFailsBeforeWrite(t *testing.T) {
	for _, command := range []string{"create-pr", "create-mr"} {
		spy := &spyService{}
		factory := func(context.Context, string) (queryService, func() error, error) { return spy, nil, nil }
		var stdout, stderr bytes.Buffer
		args := []string{command, "--repo", "fixture-a", "--title", "Review", "--head", "topic", "--base", "main", "--body-file", "-", "--live", "--dry-run", "--format", "json"}
		code := executeWithFactoryAndDeps(args, &stdout, &stderr, factory, localCommandDeps{Source: newCLIConfigSource(t), Stdin: strings.NewReader("body\n")})
		if code == 0 || !strings.Contains(stderr.String(), "conflicts with --dry-run") || spy.calls["CreatePR"] != 0 {
			t.Fatal("conflicting write intent reached the write service")
		}
	}
}

func TestPRMarkdownInlineOverridePreservesLiteralSequences(t *testing.T) {
	for _, command := range []string{"create-pr", "create-mr"} {
		opts, err := resolveMarkdownBodyInput(command, options{body: `one\n\ntwo`, bodySet: true, allowLiteralBackslashN: true}, nil)
		if err != nil || opts.body != `one\n\ntwo` || opts.bodyInput == nil || opts.bodyInput.Source != "inline" {
			t.Fatal("explicit inline override failed")
		}
	}
}

func TestPRMarkdownLiveStartupStillRequiresCredential(t *testing.T) {
	for _, command := range []string{"create-pr", "create-mr"} {
		calls := 0
		factory := func(context.Context, string) (queryService, func() error, error) {
			calls++
			return &spyService{}, nil, nil
		}
		var stdout, stderr bytes.Buffer
		args := []string{command, "--repo", "fixture-a", "--title", "Review", "--head", "topic", "--base", "main", "--body-file", "-", "--live", "--idempotency-key", "pr-body-key", "--format", "json"}
		code := executeWithFactoryAndDeps(args, &stdout, &stderr, factory, localCommandDeps{Source: newCLIConfigSource(t), CredentialReporter: statusReporter{}, Stdin: strings.NewReader("# Body\n")})
		if code == 0 || calls != 0 || !strings.Contains(stderr.String(), "credential") {
			t.Fatal("body-file bypassed live credential preflight")
		}
	}
}

type failingBodyReader struct{}

func (failingBodyReader) Read([]byte) (int, error) {
	return 0, errors.New("/private-example/body-reader-failure")
}

func TestPRMarkdownReadFailuresArePublicSafe(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "unavailable.md")
	for _, command := range []string{"create-pr", "create-mr"} {
		for _, tc := range []struct {
			path  string
			stdin io.Reader
		}{
			{path: missing}, {path: "-", stdin: failingBodyReader{}},
		} {
			_, err := resolveMarkdownBodyInput(command, options{bodyFile: tc.path, bodyFileSet: true}, tc.stdin)
			var typed service.ErrInvalidQuery
			if !errors.As(err, &typed) || typed.Field != "body_file" || !strings.Contains(err.Error(), "cannot read body") {
				t.Fatal("read failure was not typed")
			}
			if strings.Contains(err.Error(), missing) || strings.Contains(err.Error(), "/private-example/") {
				t.Fatal("read failure leaked a local path or raw reader error")
			}
		}
		// A readable file still keeps its trailing newline exactly.
		bodyPath := filepath.Join(t.TempDir(), "body.md")
		if err := os.WriteFile(bodyPath, []byte("# Body\n\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		opts, err := resolveMarkdownBodyInput(command, options{bodyFile: bodyPath, bodyFileSet: true}, nil)
		if err != nil || opts.body != "# Body\n\n" {
			t.Fatal("file content changed")
		}
	}
}
