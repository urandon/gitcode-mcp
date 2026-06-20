package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/index"
	"gitcode-mcp/internal/service"
)

const version = "0.1.0"

var commands = []string{
	"ingest",
	"index",
	"search",
	"list",
	"get",
	"backlinks",
	"get-snippet", "snippet", "snippets",
	"list-chunks",
	"recent",
	"link-check",
	"stale-index",
	"sync",
	"cache-status",
	"export",
	"diff",
	"create-issue",
	"update-issue",
	"create-page",
	"update-page",
	"add-comment",
	"add-label",
	"config",
	"auth",
	"doctor",
	"repo",
}

type queryService interface {
	Ingest(context.Context, service.OperationRequest) (service.OperationResult, error)
	Index(context.Context, service.OperationRequest) (service.OperationResult, error)
	SearchSources(context.Context, service.SearchSourcesRequest) ([]service.SearchSourceResult, error)
	ListSources(context.Context, service.ListSourcesRequest) ([]service.SourceSummary, error)
	GetSource(context.Context, service.GetSourceRequest) (service.SourceRecord, error)
	GetBacklinks(context.Context, service.GetBacklinksRequest) ([]service.BacklinkResult, error)
	GetSnippet(context.Context, service.SnippetRequest) (service.SnippetResult, error)
	ListChunks(context.Context, service.ChunkQuery) (service.ChunkQueryResult, error)
	SearchChunks(context.Context, service.ChunkSearchQuery) (service.ChunkQueryResult, error)
	GetChunkSnippet(context.Context, service.SnippetQuery) (service.ChunkQueryResult, error)
	GetSyncStatus(context.Context, service.SyncStatusRequest) (service.SyncStatusResult, error)
	RecentChanges(context.Context, service.RecentChangesRequest) ([]service.RecentChangeResult, error)
	LinkCheck(context.Context, service.LinkCheckRequest) (service.LinkCheckResult, error)
	StaleIndex(context.Context, service.StaleIndexRequest) (service.StaleIndexResult, error)
	SyncToCache(context.Context, service.SyncRequest) (service.SyncResult, error)
	CacheStatus(context.Context, service.CacheStatusRequest) (service.CacheStatusResult, error)
	ExportSnapshot(context.Context, service.ExportSnapshotRequest) (service.ExportSnapshotResult, error)
	DiffSnapshot(context.Context, service.DiffSnapshotRequest) (service.DiffSnapshotResult, error)
	AddRepository(context.Context, service.AddRepositoryRequest) (service.RepositoryBinding, error)
	RepositoryStatus(context.Context, service.RepositoryStatusRequest) (service.RepositoryStatus, error)
	CreateIssue(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error)
	UpdateIssue(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error)
	CreatePage(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error)
	UpdatePage(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error)
	AddComment(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error)
	AddLabel(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error)
}

type serviceFactory func(context.Context, string) (queryService, func() error, error)

type localCommandDeps struct {
	Source             config.Source
	CredentialReporter config.CredentialStatusReporter
}

type options struct {
	format         string
	kind           string
	status         string
	limit          int
	offset         int
	lineStart      int
	lineEnd        int
	cachePath      string
	strict         bool
	base           string
	head           string
	full           bool
	incremental    bool
	issues         bool
	wiki           bool
	syncIndex      bool
	input          string
	output         string
	owner          string
	repo           string
	name           string
	id             string
	number         int
	slug           string
	title          string
	body           string
	state          string
	label          string
	labels         string
	idempotencyKey string
	overwrite      bool
	redacted       bool
	runtimeAudit   bool
	apiBaseURL     string
	scopes         string
	alias          multiFlag
	displayName    string
	policy         string
	chunkID        string
	sourceID       string
	recordID       string
	snapshotID     string
}

type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }
func (m *multiFlag) Set(value string) error {
	*m = append(*m, value)
	return nil
}

// Execute runs the gitcode-mcp CLI.
func Execute(args []string, stdout io.Writer, stderr io.Writer) int {
	return executeWithFactory(args, stdout, stderr, defaultServiceFactory)
}

func ExecuteWithSource(args []string, stdout io.Writer, stderr io.Writer, src config.Source) int {
	return executeWithFactoryAndDeps(args, stdout, stderr, defaultServiceFactory, localCommandDeps{Source: src})
}

func executeWithFactory(args []string, stdout io.Writer, stderr io.Writer, factory serviceFactory) int {
	return executeWithFactoryAndDeps(args, stdout, stderr, factory, localCommandDeps{Source: config.OSSource{}})
}

func executeWithFactoryAndDeps(args []string, stdout io.Writer, stderr io.Writer, factory serviceFactory, deps localCommandDeps) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		printHelp(stdout)
		return 0
	}
	if args[0] == "--version" || args[0] == "version" {
		fmt.Fprintf(stdout, "gitcode-mcp %s\n", version)
		return 0
	}
	if args[0] == "config" || args[0] == "auth" || args[0] == "doctor" {
		return executeLocalCommand(args, stdout, stderr, deps)
	}
	if !isKnownCommand(args[0]) {
		fmt.Fprintf(stderr, "unknown command %q\n\n", args[0])
		printHelp(stderr)
		return 2
	}

	command := args[0]
	opts, rest, err := parseOptions(command, args[1:])
	if err != nil {
		return writeError(stderr, opts.format, err)
	}
	svc, cleanup, err := factory(context.Background(), opts.cachePath)
	if err != nil {
		return writeError(stderr, opts.format, err)
	}
	if cleanup != nil {
		defer cleanup()
	}
	return dispatch(context.Background(), svc, command, rest, opts, stdout, stderr)
}

func defaultServiceFactory(ctx context.Context, cachePath string) (queryService, func() error, error) {
	path, err := resolvedCachePath(cachePath)
	if err != nil {
		return nil, nil, err
	}
	store, err := cache.NewSQLiteStore(ctx, path)
	if err != nil {
		return nil, nil, err
	}
	return service.New(store), store.Close, nil
}

func resolvedCachePath(path string) (string, error) {
	if path != "" {
		return path, nil
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	dir = filepath.Join(dir, "gitcode-mcp")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, "cache.db"), nil
}

func parseOptions(command string, args []string) (options, []string, error) {
	opts := options{format: "text"}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&opts.format, "format", "text", "text, markdown, or json")
	flags.StringVar(&opts.kind, "kind", "", "source kind")
	flags.StringVar(&opts.status, "status", "", "source status")
	flags.IntVar(&opts.limit, "limit", 0, "result limit")
	flags.IntVar(&opts.offset, "offset", 0, "result offset")
	flags.IntVar(&opts.lineStart, "line-start", 0, "snippet start line")
	flags.IntVar(&opts.lineEnd, "line-end", 0, "snippet end line")
	flags.StringVar(&opts.cachePath, "cache-path", "", "cache database path")
	flags.BoolVar(&opts.strict, "strict", false, "exit non-zero on findings")
	flags.StringVar(&opts.base, "base", "", "base snapshot")
	flags.StringVar(&opts.head, "head", "", "head snapshot")
	flags.BoolVar(&opts.full, "full", false, "run full index")
	flags.BoolVar(&opts.incremental, "incremental", false, "run incremental index")
	flags.BoolVar(&opts.issues, "issues", false, "sync issues")
	flags.BoolVar(&opts.wiki, "wiki", false, "sync wiki")
	flags.BoolVar(&opts.syncIndex, "index", false, "build index during sync")
	flags.StringVar(&opts.input, "input", "", "input path")
	flags.StringVar(&opts.output, "output", "", "output path")
	flags.StringVar(&opts.owner, "owner", "", "repository owner")
	flags.StringVar(&opts.repo, "repo", "", "configured repository id")
	flags.StringVar(&opts.name, "name", "", "repository name")
	flags.StringVar(&opts.id, "id", "", "record id")
	flags.IntVar(&opts.number, "number", 0, "issue number")
	flags.StringVar(&opts.slug, "slug", "", "page slug")
	flags.StringVar(&opts.title, "title", "", "title")
	flags.StringVar(&opts.body, "body", "", "body")
	flags.StringVar(&opts.state, "state", "", "state")
	flags.StringVar(&opts.label, "label", "", "label")
	flags.StringVar(&opts.labels, "labels", "", "comma-separated labels")
	flags.StringVar(&opts.idempotencyKey, "idempotency-key", "", "idempotency key")
	flags.BoolVar(&opts.overwrite, "overwrite", false, "overwrite existing file")
	flags.BoolVar(&opts.redacted, "redacted", false, "redact secret values")
	flags.BoolVar(&opts.runtimeAudit, "runtime-audit", false, "emit runtime audit report")
	flags.StringVar(&opts.apiBaseURL, "api-base-url", "", "repository API base URL")
	flags.StringVar(&opts.scopes, "scopes", "", "comma-separated repository scopes")
	flags.Var(&opts.alias, "alias", "repository alias")
	flags.StringVar(&opts.displayName, "display-name", "", "repository display name")
	flags.StringVar(&opts.policy, "policy", "", "chunk policy")
	flags.StringVar(&opts.chunkID, "chunk-id", "", "chunk id")
	flags.StringVar(&opts.sourceID, "source-id", "", "source id")
	flags.StringVar(&opts.recordID, "record-id", "", "record id")
	flags.StringVar(&opts.snapshotID, "snapshot-id", "", "snapshot id")
	if err := flags.Parse(reorderFlags(args)); err != nil {
		return opts, nil, service.ErrInvalidQuery{Field: "flags", Message: err.Error()}
	}
	opts.format = strings.ToLower(opts.format)
	if opts.format != "text" && opts.format != "markdown" && opts.format != "json" {
		return opts, nil, service.ErrInvalidQuery{Field: "format", Message: "format must be text, markdown, or json"}
	}
	return opts, flags.Args(), nil
}

func reorderFlags(args []string) []string {
	flags := make([]string, 0, len(args))
	positionals := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "--") {
			flags = append(flags, arg)
			if strings.Contains(arg, "=") || arg == "--strict" || arg == "--full" || arg == "--incremental" || arg == "--issues" || arg == "--wiki" || arg == "--index" || arg == "--overwrite" || arg == "--redacted" || arg == "--runtime-audit" {
				continue
			}
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
				flags = append(flags, args[i+1])
				i++
			}
			continue
		}
		positionals = append(positionals, arg)
	}
	return append(flags, positionals...)
}

func executeLocalCommand(args []string, stdout io.Writer, stderr io.Writer, deps localCommandDeps) int {
	if deps.Source == nil {
		deps.Source = config.OSSource{}
	}
	if deps.CredentialReporter == nil {
		provider := config.DefaultCredentialProvider(deps.Source)
		deps.CredentialReporter = provider
	}
	command := args[0]
	subArgs := []string{}
	if len(args) > 1 {
		subArgs = args[1:]
	}
	opts, rest, err := parseOptions(command, subArgs)
	if err != nil {
		return writeError(stderr, opts.format, err)
	}
	if command == "doctor" && opts.runtimeAudit {
		report := config.BuildRuntimeAuditConfigReport(deps.Source, config.Overrides{}, deps.CredentialReporter, version)
		payload := runtimeAuditPayload{RepoID: opts.repo, Config: report}
		if opts.format == "json" {
			return renderJSON(stdout, payload)
		}
		renderRuntimeAuditText(stdout, payload)
		return 0
	}
	sub, ok := firstArg(rest)
	if !ok {
		return writeError(stderr, opts.format, service.ErrInvalidQuery{Field: command, Message: "subcommand is required"})
	}
	switch command + " " + sub {
	case "config init":
		loc := config.Locate(deps.Source)
		if err := config.InitYAMLConfig(loc.Path, opts.overwrite); err != nil {
			fmt.Fprintln(stderr, config.RedactDiagnostic(err.Error(), deps.Source))
			return 1
		}
		fmt.Fprintf(stdout, "config_path: %s\nconfig_format: yaml\ncreated: true\n", loc.Path)
		return 0
	case "config locate":
		loc := config.Locate(deps.Source)
		return render(stdout, opts.format, loc, func(w io.Writer, v config.ConfigLocation) {
			fmt.Fprintf(w, "config_path: %s\nconfig_source: %s\nconfig_format: %s\nconfig_exists: %t\n", cliEmptyAsNone(v.Path), v.Source, v.Format, v.Exists)
		})
	case "config show":
		if !opts.redacted {
			return writeError(stderr, opts.format, service.ErrInvalidQuery{Field: "redacted", Message: "config show requires --redacted"})
		}
		eff, err := config.LoadEffective(deps.Source, config.Overrides{})
		if err != nil {
			fmt.Fprintln(stderr, config.RedactDiagnostic(err.Error(), deps.Source))
			return 1
		}
		status := deps.CredentialReporter.Status(context.Background(), eff)
		if opts.format == "json" {
			payload := struct {
				Effective  config.EffectiveConfig  `json:"effective"`
				Credential config.CredentialStatus `json:"credential"`
			}{Effective: eff, Credential: status}
			return render(stdout, opts.format, payload, nil)
		}
		fmt.Fprint(stdout, config.RenderRedactedEffectiveConfig(eff, status))
		return 0
	case "auth status":
		eff, err := config.LoadEffective(deps.Source, config.Overrides{})
		if err != nil {
			fmt.Fprintln(stderr, config.RedactDiagnostic(err.Error(), deps.Source))
			return 1
		}
		status := deps.CredentialReporter.Status(context.Background(), eff)
		if opts.format == "json" {
			return render(stdout, opts.format, status, nil)
		}
		fmt.Fprint(stdout, config.RenderCredentialStatus(status))
		return 0
	default:
		return writeError(stderr, opts.format, service.ErrInvalidQuery{Field: command, Message: "unknown subcommand"})
	}
}

func dispatch(ctx context.Context, svc queryService, command string, args []string, opts options, stdout io.Writer, stderr io.Writer) int {
	switch command {
	case "ingest":
		result, err := svc.Ingest(ctx, service.OperationRequest{InputPath: opts.input, OutputPath: opts.output, Strict: opts.strict})
		if err != nil {
			return writeError(stderr, opts.format, err)
		}
		return render(stdout, opts.format, result, renderOperationText)
	case "index":
		mode := ""
		if opts.full {
			mode = "full"
		}
		if opts.incremental {
			mode = "incremental"
		}
		result, err := svc.Index(ctx, service.OperationRequest{RepoID: opts.repo, Mode: mode, Strict: opts.strict})
		if err != nil {
			return writeError(stderr, opts.format, err)
		}
		return render(stdout, opts.format, result, renderOperationText)
	case "search":
		if len(args) == 0 {
			return writeError(stderr, opts.format, service.ErrInvalidQuery{Field: "query", Message: "query is required"})
		}
		results, err := svc.SearchSources(ctx, service.SearchSourcesRequest{RepoID: opts.repo, Query: strings.Join(args, " "), Kind: opts.kind, Limit: opts.limit, Offset: opts.offset})
		if err != nil {
			return writeError(stderr, opts.format, err)
		}
		return render(stdout, opts.format, results, renderSearchText)
	case "list":
		results, err := svc.ListSources(ctx, service.ListSourcesRequest{RepoID: opts.repo, Kind: opts.kind, Status: opts.status, Limit: opts.limit, Offset: opts.offset})
		if err != nil {
			return writeError(stderr, opts.format, err)
		}
		return render(stdout, opts.format, results, renderListText)
	case "get":
		id, ok := firstArg(args)
		if !ok {
			return writeError(stderr, opts.format, service.ErrInvalidQuery{Field: "id", Message: "id is required"})
		}
		result, err := svc.GetSource(ctx, service.GetSourceRequest{RepoID: opts.repo, ID: id})
		if err != nil {
			return writeError(stderr, opts.format, err)
		}
		return render(stdout, opts.format, result, renderGetText)
	case "backlinks":
		id, ok := firstArg(args)
		if !ok {
			return writeError(stderr, opts.format, service.ErrInvalidQuery{Field: "id", Message: "id is required"})
		}
		results, err := svc.GetBacklinks(ctx, service.GetBacklinksRequest{RepoID: opts.repo, ID: id})
		if err != nil {
			return writeError(stderr, opts.format, err)
		}
		return render(stdout, opts.format, results, renderBacklinksText)
	case "get-snippet", "snippet", "snippets":
		id, _ := firstArg(args)
		if opts.chunkID != "" {
			if opts.lineStart > 0 || opts.lineEnd > 0 {
				return writeError(stderr, opts.format, service.ErrInvalidQuery{Field: "address", Message: "chunk-id and line range are mutually exclusive"})
			}
			query := service.SnippetQuery{RepoID: opts.repo, SourceID: firstNonEmpty(opts.sourceID, id), RecordID: opts.recordID, SnapshotID: opts.snapshotID, Policy: indexPolicy(opts.policy), ChunkID: opts.chunkID}
			if query.SourceID == "" && query.RecordID == "" && query.SnapshotID == "" {
				return writeError(stderr, opts.format, service.ErrInvalidQuery{Field: "address", Message: "source id, record id, or snapshot id is required with chunk-id"})
			}
			result, err := svc.GetChunkSnippet(ctx, query)
			if err != nil {
				return writeError(stderr, opts.format, err)
			}
			return render(stdout, opts.format, result, renderChunkQueryText)
		}
		if id == "" {
			return writeError(stderr, opts.format, service.ErrInvalidQuery{Field: "id", Message: "id is required"})
		}
		result, err := svc.GetSnippet(ctx, service.SnippetRequest{RepoID: opts.repo, ID: id, LineStart: opts.lineStart, LineEnd: opts.lineEnd})
		if err != nil {
			return writeError(stderr, opts.format, err)
		}
		if opts.format == "text" {
			fmt.Fprintln(stdout, result.Text)
			for _, warning := range result.Warnings {
				fmt.Fprintln(stderr, warning)
			}
			return 0
		}
		return renderJSON(stdout, result)
	case "list-chunks":
		result, err := svc.ListChunks(ctx, service.ChunkQuery{RepoID: opts.repo, SourceID: opts.sourceID, RecordID: opts.recordID, SnapshotID: opts.snapshotID, Policy: indexPolicy(opts.policy), Limit: opts.limit, Offset: opts.offset})
		if err != nil {
			return writeError(stderr, opts.format, err)
		}
		return render(stdout, opts.format, result, renderChunkQueryText)
	case "tasks":
		results, err := svc.ListSources(ctx, service.ListSourcesRequest{RepoID: opts.repo, Kind: "task", Status: opts.status, Limit: opts.limit, Offset: opts.offset})
		if err != nil {
			return writeError(stderr, opts.format, err)
		}
		return render(stdout, opts.format, results, renderListText)
	case "tracks":
		results, err := svc.ListSources(ctx, service.ListSourcesRequest{RepoID: opts.repo, Kind: "track", Status: opts.status, Limit: opts.limit, Offset: opts.offset})
		if err != nil {
			return writeError(stderr, opts.format, err)
		}
		return render(stdout, opts.format, results, renderListText)
	case "recent":
		results, err := svc.RecentChanges(ctx, service.RecentChangesRequest{RepoID: opts.repo, Kind: opts.kind, Status: opts.status, Limit: opts.limit, Offset: opts.offset})
		if err != nil {
			return writeError(stderr, opts.format, err)
		}
		return render(stdout, opts.format, results, renderRecentText)
	case "link-check":
		result, err := svc.LinkCheck(ctx, service.LinkCheckRequest{RepoID: opts.repo, Strict: opts.strict})
		if opts.format == "json" {
			renderJSON(stdout, result)
		} else {
			renderLinkCheckText(stdout, result)
		}
		if err != nil {
			if isStrictFinding(err) {
				return 5
			}
			return writeError(stderr, opts.format, err)
		}
		return 0
	case "stale-index":
		result, err := svc.StaleIndex(ctx, service.StaleIndexRequest{RepoID: opts.repo, Strict: opts.strict})
		if opts.format == "json" {
			renderJSON(stdout, result)
		} else {
			renderStaleIndexText(stdout, result)
		}
		if err != nil {
			if isStrictFinding(err) {
				return 5
			}
			return writeError(stderr, opts.format, err)
		}
		return 0
	case "sync":
		if opts.issues || opts.wiki {
			results := []service.SyncResult{}
			if opts.issues {
				result, err := svc.SyncToCache(ctx, service.SyncRequest{RepoID: opts.repo, RemoteAlias: "issue:42", IdempotencyKey: syncScopedKey(opts.idempotencyKey, "issue")})
				if err != nil {
					return writeError(stderr, opts.format, err)
				}
				results = append(results, result)
			}
			if opts.wiki {
				result, err := svc.SyncToCache(ctx, service.SyncRequest{RepoID: opts.repo, RemoteAlias: "wiki:Home", IdempotencyKey: syncScopedKey(opts.idempotencyKey, "wiki")})
				if err != nil {
					return writeError(stderr, opts.format, err)
				}
				results = append(results, result)
			}
			if opts.format == "json" {
				return renderJSON(stdout, results)
			}
			for _, result := range results {
				renderSyncText(stdout, result)
			}
			return 0
		}
		result, err := svc.SyncToCache(ctx, service.SyncRequest{RepoID: opts.repo, StableID: opts.id, RemoteAlias: opts.input, IdempotencyKey: opts.idempotencyKey})
		if err != nil {
			return writeError(stderr, opts.format, err)
		}
		return render(stdout, opts.format, result, renderSyncText)
	case "cache-status":
		result, err := svc.CacheStatus(ctx, service.CacheStatusRequest{RepoID: opts.repo})
		if err != nil {
			return writeError(stderr, opts.format, err)
		}
		return render(stdout, opts.format, result, renderCacheStatusText)
	case "export":
		result, err := svc.ExportSnapshot(ctx, service.ExportSnapshotRequest{RepoID: opts.repo, Format: opts.format, OutputPath: opts.output, IncludeBody: true})
		if err != nil {
			return writeError(stderr, opts.format, err)
		}
		if opts.format == "json" {
			fmt.Fprint(stdout, result.InlineContent)
			return 0
		}
		return render(stdout, opts.format, result, renderExportText)
	case "diff":
		result, err := svc.DiffSnapshot(ctx, service.DiffSnapshotRequest{RepoID: opts.repo, BaseSnapshotID: opts.base, HeadSnapshotID: opts.head, Base: snapshotRefFromPath(opts.base, opts.format), Head: snapshotRefFromPathOrCurrent(opts.head, opts.format), Format: opts.format})
		if err != nil {
			return writeError(stderr, opts.format, err)
		}
		return render(stdout, opts.format, result, renderDiffText)
	case "repo":
		sub, ok := firstArg(args)
		if !ok {
			return writeError(stderr, opts.format, service.ErrInvalidQuery{Field: "repo", Message: "subcommand is required"})
		}
		switch sub {
		case "add":
			result, err := svc.AddRepository(ctx, service.AddRepositoryRequest{RepoID: opts.repo, Owner: opts.owner, Name: opts.name, APIBaseURL: opts.apiBaseURL, Scopes: []string{opts.scopes}, DisplayName: opts.displayName, Aliases: []string(opts.alias)})
			if err != nil {
				return writeError(stderr, opts.format, err)
			}
			return render(stdout, opts.format, result, renderRepositoryBindingText)
		case "status":
			result, err := svc.RepositoryStatus(ctx, service.RepositoryStatusRequest{RepoID: opts.repo})
			if err != nil {
				return writeError(stderr, opts.format, err)
			}
			return render(stdout, opts.format, result, renderRepositoryStatusText)
		default:
			return writeError(stderr, opts.format, service.ErrInvalidQuery{Field: "repo", Message: "unknown subcommand"})
		}
	case "create-issue":
		return dispatchWrite(ctx, svc.CreateIssue, command, opts, stdout, stderr)
	case "update-issue":
		return dispatchWrite(ctx, svc.UpdateIssue, command, opts, stdout, stderr)
	case "create-page":
		return dispatchWrite(ctx, svc.CreatePage, command, opts, stdout, stderr)
	case "update-page":
		return dispatchWrite(ctx, svc.UpdatePage, command, opts, stdout, stderr)
	case "add-comment":
		return dispatchWrite(ctx, svc.AddComment, command, opts, stdout, stderr)
	case "add-label":
		return dispatchWrite(ctx, svc.AddLabel, command, opts, stdout, stderr)
	default:
		return writeError(stderr, opts.format, service.ErrInvalidQuery{Field: "command", Message: command + " is not a query command"})
	}
}

func dispatchWrite(ctx context.Context, handler func(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error), command string, opts options, stdout io.Writer, stderr io.Writer) int {
	result, err := handler(ctx, writeRequest(opts))
	if err != nil {
		return writeError(stderr, opts.format, err)
	}
	return render(stdout, opts.format, result, renderWriteText)
}

func snapshotRefFromPath(path string, format string) service.SnapshotRef {
	if strings.TrimSpace(path) == "" {
		return service.SnapshotRef{Kind: "current", Format: format}
	}
	return service.SnapshotRef{Kind: "path", Path: path, Format: format}
}

func snapshotRefFromPathOrCurrent(path string, format string) service.SnapshotRef {
	return snapshotRefFromPath(path, format)
}

func syncScopedKey(base string, scope string) string {
	if strings.TrimSpace(base) == "" {
		return ""
	}
	return base + "-" + scope
}

func writeRequest(opts options) service.WriteCommandRequest {
	labels := []string{}
	if opts.labels != "" {
		for _, label := range strings.Split(opts.labels, ",") {
			if trimmed := strings.TrimSpace(label); trimmed != "" {
				labels = append(labels, trimmed)
			}
		}
	}
	return service.WriteCommandRequest{Owner: opts.owner, Repo: opts.repo, ID: opts.id, Number: opts.number, Slug: opts.slug, Title: opts.title, Body: opts.body, State: opts.state, Label: opts.label, Labels: labels, IdempotencyKey: opts.idempotencyKey}
}

func cliEmptyAsNone(value string) string {
	if value == "" {
		return "none"
	}
	return value
}

func render[T any](stdout io.Writer, format string, value T, text func(io.Writer, T)) int {
	if format == "json" {
		return renderJSON(stdout, value)
	}
	text(stdout, value)
	return 0
}

func renderJSON(w io.Writer, value any) int {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(value); err != nil {
		fmt.Fprintf(w, "encoding error: %v\n", err)
		return 1
	}
	return 0
}

type runtimeAuditPayload struct {
	RepoID string                          `json:"repo_id,omitempty"`
	Config config.RuntimeAuditConfigReport `json:"config"`
	Cache  string                          `json:"cache,omitempty"`
	Repo   string                          `json:"repo,omitempty"`
	MCP    string                          `json:"mcp,omitempty"`
	Index  string                          `json:"index,omitempty"`
}

func renderRuntimeAuditText(w io.Writer, result runtimeAuditPayload) {
	fmt.Fprintf(w, "repo_id: %s\n", cliEmptyAsNone(result.RepoID))
	fmt.Fprintln(w, "config:")
	fmt.Fprintf(w, "  version: %s\n", result.Config.Version)
	fmt.Fprintf(w, "  config_path: %s\n", cliEmptyAsNone(result.Config.ConfigPath))
	fmt.Fprintf(w, "  config_source: %s\n", result.Config.ConfigSource)
	fmt.Fprintf(w, "  config_format: %s\n", result.Config.ConfigFormat)
	fmt.Fprintf(w, "  config_exists: %t\n", result.Config.ConfigExists)
	fmt.Fprintf(w, "  cache_path: %s\n", result.Config.CachePath)
	fmt.Fprintf(w, "  cache_path_source: %s\n", result.Config.CachePathSource)
	fmt.Fprintf(w, "  credential_source: %s\n", cliEmptyAsNone(result.Config.CredentialSource))
	fmt.Fprintf(w, "  token_present: %t\n", result.Config.TokenPresent)
	fmt.Fprintf(w, "  credential_store_mode: %s\n", result.Config.CredentialStoreMode)
	fmt.Fprintf(w, "  failure_classes: %s\n", strings.Join(result.Config.FailureClasses, ","))
	for _, remediation := range result.Config.Remediation {
		fmt.Fprintf(w, "  remediation: %s\n", remediation)
	}
	fmt.Fprintln(w, "  handoff_fields:")
	fmt.Fprintf(w, "    resolved_config_path: %s\n", cliEmptyAsNone(result.Config.HandoffFields.ResolvedConfigPath))
	fmt.Fprintf(w, "    resolved_cache_path: %s\n", result.Config.HandoffFields.ResolvedCachePath)
	fmt.Fprintln(w, "cache: not_reported_by_owner")
	fmt.Fprintln(w, "repo: not_reported_by_owner")
	fmt.Fprintln(w, "mcp: not_reported_by_owner")
	fmt.Fprintln(w, "index: not_reported_by_owner")
}

func renderSearchText(w io.Writer, results []service.SearchSourceResult) {
	for _, result := range results {
		line := 0
		if result.LineStart != nil {
			line = *result.LineStart
		}
		fmt.Fprintf(w, "%s %s %s:%d:%s\n", result.RepoID, result.ID, result.Path, line, result.Snippet)
	}
}

func renderListText(w io.Writer, results []service.SourceSummary) {
	for _, result := range results {
		fmt.Fprintf(w, "%s %s %s %s %s %s\n", result.RepoID, result.ID, result.Kind, result.Status, result.Path, result.Title)
	}
}

func renderGetText(w io.Writer, result service.SourceRecord) {
	fmt.Fprintf(w, "repo_id: %s\nid: %s\nkind: %s\npath: %s\nremote_alias: %s\ntitle: %s\nstatus: %s\nbody:\n%s\n", result.RepoID, result.ID, result.Kind, result.Path, result.RemoteAlias, result.Title, result.Status, result.Body)
}

func renderBacklinksText(w io.Writer, results []service.BacklinkResult) {
	for _, result := range results {
		fmt.Fprintf(w, "%s %s %s %s\n", result.ID, result.Path, result.Title, result.TargetID)
	}
}

func renderChunkQueryText(w io.Writer, result service.ChunkQueryResult) {
	for _, chunk := range result.Chunks {
		text := chunk.SnippetText
		if text == "" {
			text = chunk.Text
		}
		fmt.Fprintf(w, "%s %s %s %s %d-%d %s\n", chunk.RepoID, chunk.SourceID, chunk.ID, chunk.Policy, chunk.ByteStart, chunk.ByteEnd, strings.TrimSpace(text))
	}
	for _, warning := range result.Warnings {
		fmt.Fprintf(w, "warning: %s %s\n", warning.Code, warning.Message)
	}
}

func renderSyncStatusText(w io.Writer, result service.SyncStatusResult) {
	fmt.Fprintf(w, "%s %s %s %s %s %s\n", result.RepoID, result.SourceID, result.Status, result.RemoteType, result.RemoteID, result.LastFetchedAt.Format(time.RFC3339))
}

func renderRecentText(w io.Writer, results []service.RecentChangeResult) {
	for _, result := range results {
		fmt.Fprintf(w, "%s %s %s %s %s\n", result.UpdatedAt.UTC().Format(time.RFC3339), result.RepoID, result.ID, result.Path, result.Title)
	}
}

func renderLinkCheckText(w io.Writer, result service.LinkCheckResult) {
	for _, broken := range result.BrokenLinks {
		fmt.Fprintf(w, "%s -> %s %s %s\n", broken.SourceID, broken.TargetID, broken.Kind, broken.Text)
	}
}

func renderStaleIndexText(w io.Writer, result service.StaleIndexResult) {
	fmt.Fprintf(w, "stale_count: %d\n", result.StaleCount)
	if len(result.AffectedSourceIDs) > 0 {
		fmt.Fprintf(w, "affected_source_ids: %s\n", strings.Join(result.AffectedSourceIDs, ","))
	}
	if len(result.MissingTargetIDs) > 0 {
		fmt.Fprintf(w, "missing_target_ids: %s\n", strings.Join(result.MissingTargetIDs, ","))
	}
}

func renderOperationText(w io.Writer, result service.OperationResult) {
	fmt.Fprintf(w, "%s: %s processed=%d evidence=%s\n", result.Command, result.Status, result.ProcessedCount, result.Evidence)
}

func renderSyncText(w io.Writer, result service.SyncResult) {
	fmt.Fprintf(w, "sync: %s fetched=%d updated=%d inserted=%d skipped=%d conflicts=%d idempotency_key=%s replayed=%t\n", result.Status, result.Counts.Fetched, result.Counts.Updated, result.Counts.Inserted, result.Counts.Skipped, result.Counts.Conflicts, result.IdempotencyKey, result.Replayed)
}

func renderCacheStatusText(w io.Writer, result service.CacheStatusResult) {
	fmt.Fprintf(w, "repo_id: %s\nwal_capable: %t\njournal_mode: %s\nrecords: %d\ncomments: %d\nidentity_aliases: %d\nsync_events: %d\naudit_rows: %d\nsnapshots: %d\nsnapshot_chunks: %d\nchunks: %d\nremote_revisions: %d\n", result.RepoID, result.WALCapable, result.JournalMode, result.Records, result.Comments, result.IdentityAliases, result.SyncEvents, result.AuditRows, result.Snapshots, result.SnapshotChunks, result.Chunks, result.RemoteRevisions)
}

func renderExportText(w io.Writer, result service.ExportSnapshotResult) {
	if result.InlineContent != "" {
		fmt.Fprint(w, result.InlineContent)
		return
	}
	fmt.Fprintf(w, "%s %s records=%d hash=%s\n", result.SnapshotID, result.Format, result.RecordCount, result.ContentHash)
}

func renderDiffText(w io.Writer, result service.DiffSnapshotResult) {
	if result.DiffText != "" {
		fmt.Fprint(w, result.DiffText)
		return
	}
	fmt.Fprintf(w, "changed_source_ids: %s\n", strings.Join(result.ChangedSourceIDs, ","))
}

func renderWriteText(w io.Writer, result service.WriteCommandResult) {
	fmt.Fprintf(w, "%s: %s id=%s idempotency_key=%s evidence=%s\n", result.Command, result.Status, result.ID, result.IdempotencyKey, result.Evidence)
}

func renderRepositoryBindingText(w io.Writer, result service.RepositoryBinding) {
	fmt.Fprintf(w, "repo_id: %s\nowner: %s\nname: %s\napi_base_url: %s\nscopes: %s\ndisplay_name: %s\naliases: %s\n", result.RepoID, result.Owner, result.Name, result.APIBaseURL, joinRepositoryScopes(result.Scopes), result.DisplayName, strings.Join(result.Aliases, ","))
}

func renderRepositoryStatusText(w io.Writer, result service.RepositoryStatus) {
	fmt.Fprintf(w, "repo_id: %s\nowner: %s\nname: %s\napi_base_url: %s\nscopes: %s\ndisplay_name: %s\naliases: %s\nbinding_state: %s\nalias_conflict_state: %s\ncache_state: %s\nindex_state: %s\n", result.RepoID, result.Owner, result.Name, result.APIBaseURL, joinRepositoryScopes(result.Scopes), result.DisplayName, strings.Join(result.Aliases, ","), result.BindingState, result.AliasConflictState, result.CacheState, result.IndexState)
	if result.FailureClass != "" {
		fmt.Fprintf(w, "failure_class: %s\n", result.FailureClass)
	}
}

func joinRepositoryScopes(scopes []service.RepositoryScope) string {
	parts := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		parts = append(parts, string(scope))
	}
	return strings.Join(parts, ",")
}

func writeError(stderr io.Writer, format string, err error) int {
	code := exitCode(err)
	failureClass := failureClass(err)
	if format == "json" {
		_ = json.NewEncoder(stderr).Encode(map[string]any{"error": err.Error(), "exit_code": code, "failure_class": failureClass})
		return code
	}
	fmt.Fprintln(stderr, err.Error())
	if failureClass != "" {
		fmt.Fprintf(stderr, "failure_class: %s\n", failureClass)
	}
	return code
}

func failureClass(err error) string {
	var cacheEmpty service.ErrCacheEmpty
	if errors.As(err, &cacheEmpty) {
		return "cache_empty"
	}
	var notFound service.ErrNotFound
	if errors.As(err, &notFound) {
		return "not_found"
	}
	var repoRequired service.ErrRepoRequired
	if errors.As(err, &repoRequired) {
		return "repo_required"
	}
	var ambiguous service.ErrAmbiguousAlias
	if errors.As(err, &ambiguous) {
		return "ambiguous_alias"
	}
	var invalid service.ErrInvalidQuery
	if errors.As(err, &invalid) {
		return "invalid_query"
	}
	var conflict service.ErrConflict
	if errors.As(err, &conflict) {
		return "conflict"
	}
	if isStrictFinding(err) {
		return "validation_failed"
	}
	return "internal_error"
}

func exitCode(err error) int {
	var cacheEmpty service.ErrCacheEmpty
	if errors.As(err, &cacheEmpty) {
		return 2
	}
	var notFound service.ErrNotFound
	if errors.As(err, &notFound) {
		return 3
	}
	var repoRequired service.ErrRepoRequired
	if errors.As(err, &repoRequired) {
		return 4
	}
	var ambiguous service.ErrAmbiguousAlias
	if errors.As(err, &ambiguous) {
		return 4
	}
	var invalid service.ErrInvalidQuery
	if errors.As(err, &invalid) {
		return 4
	}
	var conflict service.ErrConflict
	if errors.As(err, &conflict) {
		return 6
	}
	if isStrictFinding(err) {
		return 5
	}
	return 1
}

func isStrictFinding(err error) bool {
	var stale service.ErrStaleIndex
	if errors.As(err, &stale) {
		return true
	}
	var link service.ErrLinkCheckFailed
	return errors.As(err, &link)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func indexPolicy(policy string) index.ChunkPolicy {
	return index.ChunkPolicy(policy)
}

func firstArg(args []string) (string, bool) {
	if len(args) == 0 || strings.TrimSpace(args[0]) == "" {
		return "", false
	}
	return args[0], true
}

func printHelp(w io.Writer) {
	fmt.Fprintln(w, "gitcode-mcp - cache-first GitCode MCP and CLI tooling")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  gitcode-mcp [command] [flags]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	for _, command := range commands {
		fmt.Fprintf(w, "  %s\n", command)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Shell-equivalent query mapping:")
	fmt.Fprintln(w, "  find -> list")
	fmt.Fprintln(w, "  rg -n -> search")
	fmt.Fprintln(w, "  rg --files -> list")
	fmt.Fprintln(w, "  sed -n -> get-snippet")
	fmt.Fprintln(w, "  handoff/review inspection -> recent")
	fmt.Fprintln(w, "  broken pointer search -> link-check")
	fmt.Fprintln(w, "  stale derived data search -> stale-index")
	fmt.Fprintln(w, "  cache health inspection -> cache-status")
	fmt.Fprintln(w, "  minimum replacement sequence: sync -> search -> list -> get -> backlinks")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Global query flags:")
	fmt.Fprintln(w, "  --format text|json")
	fmt.Fprintln(w, "  --kind KIND")
	fmt.Fprintln(w, "  --status STATUS")
	fmt.Fprintln(w, "  --limit N")
	fmt.Fprintln(w, "  --offset N")
	fmt.Fprintln(w, "  --line-start N")
	fmt.Fprintln(w, "  --line-end N")
	fmt.Fprintln(w, "  --cache-path PATH")
	fmt.Fprintln(w, "  --strict")
	fmt.Fprintln(w, "  --full | --incremental")
	fmt.Fprintln(w, "  --input PATH --output PATH")
	fmt.Fprintln(w, "  --owner OWNER --repo REPO --name NAME --api-base-url URL --scopes issues,wiki --alias ALIAS")
	fmt.Fprintln(w, "  --number N --id ID --slug SLUG")
	fmt.Fprintln(w, "  --title TITLE --body BODY --label LABEL --labels A,B")
	fmt.Fprintln(w, "  --idempotency-key KEY")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Global options:")
	fmt.Fprintln(w, "  -h, --help     Show help")
	fmt.Fprintln(w, "  --version      Show version")
}

func isKnownCommand(candidate string) bool {
	for _, command := range commands {
		if strings.EqualFold(candidate, command) {
			return true
		}
	}
	return false
}
