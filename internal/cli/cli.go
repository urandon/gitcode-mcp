package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gitcode-mcp/internal/buildinfo"
	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/credential"
	"gitcode-mcp/internal/diagnostics"
	"gitcode-mcp/internal/doctor"
	"gitcode-mcp/internal/gitcode"
	"gitcode-mcp/internal/index"
	"gitcode-mcp/internal/service"
)

var commands = []string{
	"ingest",
	"index",
	"search", "search_sources",
	"list",
	"get",
	"backlinks",
	"get-snippet", "snippet", "snippets",
	"list-chunks",
	"recent",
	"link-check",
	"stale-index",
	"pr-discussions", "pr-review-discussions",
	"sync",
	"cache",
	"cache-status",
	"sync-status", "sync_status",
	"export", "export-snapshot",
	"diff", "diff-snapshot",
	"create-issue",
	"update-issue",
	"create-pr", "create-mr",
	"create-page",
	"update-page",
	"delete-page",
	"add-comment",
	"add-pr-review-comment",
	"update-comment",
	"add-label",
	"publish-release",
	"config",
	"auth",
	"doctor",
	"migrate-cache",
	"repo",
	"bind",
}

type queryService interface {
	Ingest(context.Context, service.OperationRequest) (service.OperationResult, error)
	Index(context.Context, service.OperationRequest) (service.OperationResult, error)
	SearchSources(context.Context, service.SearchSourcesRequest) (service.SearchSourcesResult, error)
	ListSources(context.Context, service.ListSourcesRequest) (service.ListSourcesResult, error)
	GetSource(context.Context, service.GetSourceRequest) (service.SourceRecord, error)
	GetBacklinks(context.Context, service.GetBacklinksRequest) (service.BacklinksResult, error)
	GetSnippet(context.Context, service.SnippetRequest) (service.SnippetResult, error)
	ListChunks(context.Context, service.ChunkQuery) (service.ChunkQueryResult, error)
	SearchChunks(context.Context, service.ChunkSearchQuery) (service.ChunkQueryResult, error)
	GetChunkSnippet(context.Context, service.SnippetQuery) (service.ChunkQueryResult, error)
	GetSyncStatus(context.Context, service.SyncStatusRequest) (service.SyncStatusResult, error)
	SyncStatus(context.Context, service.ListSourcesRequest) (service.SyncStatusSummaryResult, error)
	RecentChanges(context.Context, service.RecentChangesRequest) (service.RecentChangesResult, error)
	LinkCheck(context.Context, service.LinkCheckRequest) (service.LinkCheckResult, error)
	StaleIndex(context.Context, service.StaleIndexRequest) (service.StaleIndexResult, error)
	SyncToCache(context.Context, service.SyncRequest) (service.SyncResult, error)
	SyncResources(context.Context, []service.SyncRequest) (*service.SyncResourcesResult, error)
	BulkSyncIssues(context.Context, service.BulkSyncRequest) (*service.SyncResourcesResult, error)
	BulkSyncWiki(context.Context, service.BulkSyncRequest) (*service.SyncResourcesResult, error)
	BulkSyncPullRequests(context.Context, service.BulkSyncRequest) (*service.SyncResourcesResult, error)
	BulkSyncPRComments(context.Context, service.BulkSyncRequest) (*service.SyncResourcesResult, error)
	BulkSyncAll(context.Context, service.BulkSyncRequest) (*service.SyncResourcesResult, error)
	ListPRDiscussions(context.Context, service.PRDiscussionRequest) (service.PRDiscussionsResult, error)
	ResetLiveCache(context.Context, service.ResetLiveCacheRequest) (service.ResetLiveCacheResult, error)
	CacheStatus(context.Context, service.CacheStatusRequest) (service.CacheStatusResult, error)
	ExportSnapshot(context.Context, service.ExportSnapshotRequest) (service.ExportSnapshotResult, error)
	DiffSnapshot(context.Context, service.DiffSnapshotRequest) (service.DiffSnapshotResult, error)
	AddRepository(context.Context, service.AddRepositoryRequest) (service.RepositoryBinding, error)
	RepositoryStatus(context.Context, service.RepositoryStatusRequest) (service.RepositoryStatus, error)
	CreateIssue(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error)
	UpdateIssue(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error)
	CreatePR(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error)
	CreatePage(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error)
	UpdatePage(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error)
	DeletePage(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error)
	AddComment(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error)
	AddPRReviewComment(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error)
	UpdateComment(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error)
	AddLabel(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error)
	PublishRelease(context.Context, service.PublishReleaseRequest) (service.PublishReleaseResult, error)
}

type serviceFactory func(context.Context, string) (queryService, func() error, error)

type localCommandDeps struct {
	Source             config.Source
	CredentialReporter config.CredentialStatusReporter
}

type startupPlan struct {
	Command               string
	ProviderMode          string
	CachePath             string
	RepoID                string
	APIBaseURL            string
	MCPToolAccess         string
	LiveRepositoryBinding service.LiveRepositoryBinding
	CredentialStatus      config.CredentialStatus
	CredentialResolution  config.CredentialResolutionResult
	Token                 config.SecretString
	ServiceConfig         service.ServiceConfig
}

type options struct {
	format         string
	kind           string
	status         string
	provenance     string
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
	pulls          bool
	comments       bool
	syncIndex      bool
	maxPages       int
	maxRecords     int
	perPage        int
	progress       string
	quiet          bool
	details        bool
	input          string
	output         string
	owner          string
	repo           string
	name           string
	id             string
	number         int
	commentID      string
	slug           string
	path           string
	line           int
	startLine      int
	endLine        int
	position       int
	sha            string
	title          string
	body           string
	state          string
	label          string
	labels         string
	tag            string
	ref            string
	asset          multiFlag
	idempotencyKey string
	dryRun         bool
	live           bool
	offline        bool
	fixture        bool
	overwrite      bool
	redacted       bool
	runtimeAudit   bool
	unresolvedOnly bool
	apiBaseURL     string
	scopes         string
	alias          multiFlag
	displayName    string
	policy         string
	chunkID        string
	sourceID       string
	recordID       string
	snapshotID     string
	confirm        bool
	helpRequested  bool
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
	return ExecuteWithSourceContext(context.Background(), args, stdout, stderr, src)
}

func ExecuteWithSourceContext(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer, src config.Source) int {
	return executeWithFactoryAndDepsContext(ctx, args, stdout, stderr, defaultServiceFactory, localCommandDeps{Source: src})
}

func ExecuteWithClient(args []string, stdout io.Writer, stderr io.Writer, client gitcode.Client) int {
	return executeWithFactory(args, stdout, stderr, func(ctx context.Context, cachePath string) (queryService, func() error, error) {
		path, err := resolvedCachePath(cachePath)
		if err != nil {
			return nil, nil, err
		}
		if err := ensureCacheParentDir(path); err != nil {
			return nil, nil, err
		}
		store, err := cache.NewSQLiteStore(ctx, path)
		if err != nil {
			return nil, nil, err
		}
		return service.NewWithClient(store, client), store.Close, nil
	})
}

func executeWithFactory(args []string, stdout io.Writer, stderr io.Writer, factory serviceFactory) int {
	return executeWithFactoryAndDeps(args, stdout, stderr, factory, localCommandDeps{Source: config.OSSource{}})
}

func executeWithFactoryAndDeps(args []string, stdout io.Writer, stderr io.Writer, factory serviceFactory, deps localCommandDeps) int {
	return executeWithFactoryAndDepsContext(context.Background(), args, stdout, stderr, factory, deps)
}

func executeWithFactoryAndDepsContext(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer, factory serviceFactory, deps localCommandDeps) int {
	if deps.Source == nil {
		deps.Source = config.OSSource{}
	}
	if deps.CredentialReporter == nil {
		provider := config.DefaultCredentialProvider(deps.Source)
		deps.CredentialReporter = provider
	}
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		printHelp(stdout)
		return 0
	}
	if args[0] == "--version" || args[0] == "version" {
		fmt.Fprintf(stdout, "gitcode-mcp %s\n", buildinfo.Version)
		return 0
	}
	if args[0] == "config" || args[0] == "auth" || args[0] == "doctor" || args[0] == "migrate-cache" || args[0] == "bind" {
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
	if opts.helpRequested {
		if command == "repo" {
			if sub, ok := firstArg(rest); ok {
				switch sub {
				case "add", "status", "init-local":
					printLocalSubcommandHelp(command, sub, stdout)
					return 0
				}
			}
		}
		printCommandHelp(command, stdout)
		return 0
	}
	if command == "repo" {
		if sub, ok := firstArg(rest); ok && sub == "init-local" {
			return executeRepoInitLocalCommand(ctx, opts, stdout, stderr, deps)
		}
	}
	plan, planErr := buildStartupPlan(context.Background(), command, opts, deps)
	if planErr != nil {
		return writeCommandError(stderr, opts.format, plan, planErr)
	}
	svc, cleanup, err := serviceFromStartupPlan(context.Background(), plan, factory)
	if err != nil {
		return writeCommandError(stderr, opts.format, plan, err)
	}
	if cleanup != nil {
		defer cleanup()
	}
	return dispatch(ctx, svc, command, rest, opts, stdout, stderr, plan)
}

func buildStartupPlan(ctx context.Context, command string, opts options, deps localCommandDeps) (startupPlan, error) {
	plan := startupPlan{Command: command, ProviderMode: "offline-fixture", CachePath: opts.cachePath, RepoID: opts.repo}
	if opts.live && (opts.offline || opts.fixture) {
		return plan, service.ErrInvalidQuery{Field: "provider_mode", Message: "--live conflicts with --offline/--fixture"}
	}
	explicitOffline := opts.offline || opts.fixture
	if isLiveStartupCommand(command) && !explicitOffline && !opts.dryRun {
		plan.ProviderMode = "live-http"
	}
	eff, err := config.LoadEffective(deps.Source, config.Overrides{CachePath: opts.cachePath})
	if err != nil {
		return plan, err
	}
	plan.CachePath = firstNonEmpty(opts.cachePath, eff.Config.CachePath)
	plan.MCPToolAccess = eff.Config.MCPToolAccess
	if plan.ProviderMode != "live-http" {
		return plan, nil
	}
	resolution, err := resolveLiveCredential(ctx, eff, deps)
	plan.CredentialResolution = resolution
	plan.CredentialStatus = resolution.Status()
	if err != nil {
		return plan, err
	}
	if !resolution.Present || strings.TrimSpace(resolution.Token.Value()) == "" {
		return plan, config.MissingCredentialError{Status: resolution.Status()}
	}
	plan.Token = resolution.Token
	binding, err := resolveStartupLiveRepositoryBinding(ctx, plan.CachePath, opts.repo, liveRequestedScope(command, opts), eff.Config.GitCodeBaseURL)
	if err != nil {
		return plan, err
	}
	plan.LiveRepositoryBinding = binding
	plan.APIBaseURL = binding.APIBaseURL
	plan.ServiceConfig = service.ServiceConfig{BaseURL: binding.APIBaseURL, LockPath: eff.Config.LockPath, Timeout: eff.Config.DefaultTimeout, MaxResponseSize: eff.Config.MaxResponseSize, MaxRetries: eff.Config.MaxRetries}
	return plan, nil
}

func resolveLiveCredential(ctx context.Context, eff config.EffectiveConfig, deps localCommandDeps) (config.CredentialResolutionResult, error) {
	if resolver, ok := deps.CredentialReporter.(interface {
		ResolveLiveCredential(context.Context, config.EffectiveConfig) (config.CredentialResolutionResult, error)
	}); ok {
		return resolver.ResolveLiveCredential(ctx, eff)
	}
	if provider, ok := deps.CredentialReporter.(config.CredentialProvider); ok {
		secret, status, err := provider.Resolve(ctx, eff)
		result := config.CredentialResolutionResult{Present: status.Present && strings.TrimSpace(secret.Value()) != "", Token: secret, Source: status.Source, StoreMode: status.StoreMode, AttemptedSources: append([]string(nil), status.AttemptedSources...), AvailableSources: append([]string(nil), status.AvailableSources...), UnavailableSources: append([]string(nil), status.UnavailableSources...), ErrorClass: status.ErrorClass, Remediation: status.Remediation}
		if err != nil {
			return result, err
		}
		if !result.Present {
			return result, config.MissingCredentialError{Status: result.Status()}
		}
		return result, nil
	}
	provider := config.DefaultCredentialProvider(deps.Source)
	return provider.ResolveLiveCredential(ctx, eff)
}

func isLiveStartupCommand(command string) bool {
	switch command {
	case "sync", "create-issue", "update-issue", "create-pr", "create-mr", "create-page", "update-page", "delete-page", "add-comment", "add-pr-review-comment", "update-comment", "add-label", "publish-release", "doctor":
		return true
	default:
		return false
	}
}

func resolveStartupLiveRepositoryBinding(ctx context.Context, cachePath, repoID string, requestedScope service.RepositoryScope, fallback string) (service.LiveRepositoryBinding, error) {
	store, err := cache.NewSQLiteReadOnlyStore(ctx, cachePath)
	if err != nil {
		return service.LiveRepositoryBinding{}, err
	}
	defer store.Close()
	svc := service.New(store)
	return svc.ResolveLiveRepositoryBinding(ctx, service.LiveRepositoryBindingRequest{RepoID: repoID, RequestedScope: requestedScope, CachePath: cachePath, AuditPath: cachePath, FallbackAPIBaseURL: fallback})
}

func liveRequestedScope(command string, opts options) service.RepositoryScope {
	switch command {
	case "create-page", "update-page", "delete-page":
		return service.RepositoryScopeWiki
	case "sync":
		if opts.wiki && !opts.issues && !opts.pulls && !opts.comments {
			return service.RepositoryScopeWiki
		}
	}
	return service.RepositoryScopeIssues
}

func serviceFromStartupPlan(ctx context.Context, plan startupPlan, factory serviceFactory) (queryService, func() error, error) {
	if plan.ProviderMode != "live-http" {
		if factory == nil {
			factory = defaultServiceFactory
		}
		return factory(ctx, plan.CachePath)
	}
	path, err := resolvedCachePath(plan.CachePath)
	if err != nil {
		return nil, nil, err
	}
	if err := ensureCacheParentDir(path); err != nil {
		return nil, nil, err
	}
	store, err := cache.NewSQLiteStore(ctx, path)
	if err != nil {
		return nil, nil, err
	}
	svc, err := service.NewWithMode(store, gitcode.ProviderModeLive, plan.Token.Value(), plan.ServiceConfig)
	if err != nil {
		_ = store.Close()
		return nil, nil, err
	}
	return svc, store.Close, nil
}

func defaultServiceFactory(ctx context.Context, cachePath string) (queryService, func() error, error) {
	path, err := resolvedCachePath(cachePath)
	if err != nil {
		return nil, nil, err
	}
	if err := ensureCacheParentDir(path); err != nil {
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

func ensureCacheParentDir(path string) error {
	if path == "" || path == ":memory:" {
		return nil
	}
	dir := filepath.Dir(path)
	if dir == "." || dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("cache: cannot create cache directory %s: %w", dir, err)
	}
	return nil
}

func parseOptions(command string, args []string) (options, []string, error) {
	opts := options{format: "text"}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&opts.format, "format", "text", "text, markdown, or json")
	flags.StringVar(&opts.kind, "kind", "", "source kind")
	flags.StringVar(&opts.status, "status", "", "source status")
	flags.StringVar(&opts.provenance, "provenance", "", "source provenance")
	flags.IntVar(&opts.limit, "limit", 0, "result limit")
	flags.IntVar(&opts.offset, "offset", 0, "result offset")
	flags.IntVar(&opts.lineStart, "line-start", 0, "snippet start line")
	flags.IntVar(&opts.lineEnd, "line-end", 0, "snippet end line")
	flags.StringVar(&opts.cachePath, "cache-path", "", "cache database path")
	flags.BoolVar(&opts.strict, "strict", false, "exit non-zero on findings")
	flags.StringVar(&opts.base, "base", "", "base snapshot")
	flags.StringVar(&opts.base, "base-id", "", "base snapshot id")
	flags.StringVar(&opts.head, "head", "", "head snapshot")
	flags.StringVar(&opts.head, "head-id", "", "head snapshot id")
	flags.BoolVar(&opts.full, "full", false, "run full index")
	flags.BoolVar(&opts.incremental, "incremental", false, "run incremental index")
	flags.BoolVar(&opts.issues, "issues", false, "sync issues")
	flags.BoolVar(&opts.wiki, "wiki", false, "sync wiki")
	flags.BoolVar(&opts.pulls, "pulls", false, "sync pull requests")
	flags.BoolVar(&opts.comments, "comments", false, "sync supported comments")
	flags.BoolVar(&opts.syncIndex, "index", false, "build index during sync")
	flags.IntVar(&opts.maxPages, "max-pages", 0, "maximum pages to sync")
	flags.IntVar(&opts.maxRecords, "max-records", 0, "maximum records to sync")
	flags.IntVar(&opts.perPage, "per-page", 0, "records per page")
	flags.StringVar(&opts.progress, "progress", "auto", "sync progress mode: auto, spinner, lines, jsonl, or off")
	flags.BoolVar(&opts.quiet, "quiet", false, "suppress non-result progress output")
	flags.BoolVar(&opts.details, "details", false, "include per-record details in large command output")
	flags.BoolVar(&opts.details, "records", false, "alias for --details")
	flags.StringVar(&opts.input, "input", "", "input path")
	flags.StringVar(&opts.output, "output", "", "output path")
	flags.StringVar(&opts.owner, "owner", "", "repository owner")
	flags.StringVar(&opts.repo, "repo", "", "configured repository id")
	flags.StringVar(&opts.name, "name", "", "repository name")
	flags.StringVar(&opts.id, "id", "", "record id")
	flags.IntVar(&opts.number, "number", 0, "issue number")
	flags.StringVar(&opts.commentID, "comment-id", "", "comment id")
	flags.StringVar(&opts.slug, "slug", "", "page slug")
	flags.StringVar(&opts.path, "path", "", "page path")
	flags.IntVar(&opts.line, "line", 0, "line number")
	flags.IntVar(&opts.startLine, "start-line", 0, "start line")
	flags.IntVar(&opts.endLine, "end-line", 0, "end line")
	flags.IntVar(&opts.position, "position", 0, "diff position")
	flags.StringVar(&opts.sha, "sha", "", "page sha")
	flags.StringVar(&opts.title, "title", "", "title")
	flags.StringVar(&opts.body, "body", "", "body")
	flags.StringVar(&opts.state, "state", "", "state")
	flags.StringVar(&opts.label, "label", "", "label")
	flags.StringVar(&opts.labels, "labels", "", "comma-separated labels")
	flags.StringVar(&opts.tag, "tag", "", "release tag")
	flags.StringVar(&opts.ref, "ref", "", "release ref")
	flags.Var(&opts.asset, "asset", "release asset link as name=url")
	flags.Var(&opts.asset, "asset-url", "release asset link as name=url")
	flags.StringVar(&opts.idempotencyKey, "idempotency-key", "", "idempotency key")
	flags.BoolVar(&opts.dryRun, "dry-run", false, "validate write without mutation")
	flags.BoolVar(&opts.live, "live", false, "execute live write")
	flags.BoolVar(&opts.offline, "offline", false, "use explicit offline/fixture provider")
	flags.BoolVar(&opts.fixture, "fixture", false, "use explicit fixture provider")
	flags.BoolVar(&opts.overwrite, "overwrite", false, "overwrite existing file")
	flags.BoolVar(&opts.redacted, "redacted", false, "redact secret values")
	flags.BoolVar(&opts.runtimeAudit, "runtime-audit", false, "emit runtime audit report")
	flags.BoolVar(&opts.unresolvedOnly, "unresolved-only", false, "only include unresolved review discussions")
	flags.StringVar(&opts.apiBaseURL, "api-base-url", "", "repository API base URL")
	flags.StringVar(&opts.scopes, "scopes", "", "comma-separated repository scopes")
	flags.Var(&opts.alias, "alias", "repository alias")
	flags.StringVar(&opts.displayName, "display-name", "", "repository display name")
	flags.StringVar(&opts.policy, "policy", "", "chunk policy")
	flags.StringVar(&opts.chunkID, "chunk-id", "", "chunk id")
	flags.StringVar(&opts.sourceID, "source-id", "", "source id")
	flags.StringVar(&opts.recordID, "record-id", "", "record id")
	flags.StringVar(&opts.snapshotID, "snapshot-id", "", "snapshot id")
	flags.BoolVar(&opts.helpRequested, "help", false, "show help for command")
	flags.BoolVar(&opts.helpRequested, "h", false, "show help for command")
	flags.BoolVar(&opts.confirm, "confirm", false, "confirm migration without interactive prompt")
	if err := flags.Parse(reorderFlags(args)); err != nil {
		return opts, nil, service.ErrInvalidQuery{Field: "flags", Message: err.Error()}
	}
	opts.format = strings.ToLower(opts.format)
	if opts.format != "text" && opts.format != "markdown" && opts.format != "json" {
		return opts, nil, service.ErrInvalidQuery{Field: "format", Message: "format must be text, markdown, or json"}
	}
	opts.progress = strings.ToLower(strings.TrimSpace(opts.progress))
	if opts.progress == "" {
		opts.progress = "auto"
	}
	switch opts.progress {
	case "auto", "spinner", "lines", "jsonl", "off":
	default:
		return opts, nil, service.ErrInvalidQuery{Field: "progress", Message: "progress must be auto, spinner, lines, jsonl, or off"}
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
			if strings.Contains(arg, "=") || arg == "--strict" || arg == "--full" || arg == "--incremental" || arg == "--issues" || arg == "--wiki" || arg == "--pulls" || arg == "--comments" || arg == "--index" || arg == "--quiet" || arg == "--dry-run" || arg == "--live" || arg == "--offline" || arg == "--fixture" || arg == "--overwrite" || arg == "--redacted" || arg == "--runtime-audit" || arg == "--confirm" {
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
	if opts.helpRequested {
		sub, _ := firstArg(rest)
		if sub != "" && (command == "config" || command == "auth" || command == "repo") {
			switch command + " " + sub {
			case "config init", "config locate", "config show":
				printLocalSubcommandHelp(command, sub, stdout)
			case "auth status":
				printLocalSubcommandHelp(command, sub, stdout)
			case "repo add", "repo status":
				printLocalSubcommandHelp(command, sub, stdout)
			case "repo init-local":
				printLocalSubcommandHelp(command, sub, stdout)
			default:
				printCommandHelp(command, stdout)
			}
			return 0
		}
		printCommandHelp(command, stdout)
		return 0
	}
	if command == "doctor" && opts.runtimeAudit {
		report := config.BuildRuntimeAuditConfigReport(deps.Source, config.Overrides{}, deps.CredentialReporter, buildinfo.Version)
		payload := runtimeAuditPayload{RepoID: opts.repo, Config: report}
		if opts.format == "json" {
			return renderJSON(stdout, payload)
		}
		renderRuntimeAuditText(stdout, payload)
		return 0
	}
	if command == "doctor" {
		plan, planErr := buildStartupPlan(context.Background(), command, opts, deps)
		var invalid service.ErrInvalidQuery
		if errors.As(planErr, &invalid) {
			return writeError(stderr, opts.format, planErr)
		}
		return executeDoctorCommand(context.Background(), opts, plan, stdout, stderr, deps)
	}
	if command == "migrate-cache" {
		return executeMigrateCacheCommand(context.Background(), opts, stdout, stderr, deps)
	}
	if command == "bind" {
		return writeError(stderr, opts.format, service.ErrInvalidQuery{Field: "bind", Message: "use repo add to create repository bindings"})
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
		if opts.live {
			resolution, _ := resolveLiveCredential(context.Background(), eff, deps)
			status = resolution.Status()
			status = probeAuthStatus(context.Background(), deps.Source, eff, opts, status, resolution.Token)
		}
		sanitizedStatus := sanitizeCredentialStatus(status, deps.Source)
		if opts.format == "json" {
			code := render(stdout, opts.format, sanitizedStatus, nil)
			if status.AuthProbe != nil && status.AuthProbe.FailureClass == "auth-failure" {
				return 1
			}
			return code
		}
		fmt.Fprint(stdout, config.RedactDiagnostic(config.RenderCredentialStatus(sanitizedStatus), deps.Source))
		if status.AuthProbe != nil && status.AuthProbe.FailureClass == "auth-failure" {
			return 1
		}
		return 0
	default:
		return writeError(stderr, opts.format, service.ErrInvalidQuery{Field: command, Message: "unknown subcommand"})
	}
}

func probeAuthStatus(ctx context.Context, src config.Source, eff config.EffectiveConfig, opts options, status config.CredentialStatus, secret config.SecretString) config.CredentialStatus {
	if !status.Present || strings.TrimSpace(secret.Value()) == "" {
		status.AuthProbe = &config.CredentialAuthProbe{Status: "skipped", FailureClass: "token-missing", Message: "auth probe requires a token"}
		return status
	}
	token := strings.TrimSpace(secret.Value())
	owner := strings.TrimSpace(opts.owner)
	repo := strings.TrimSpace(opts.repo)
	if owner == "" || repo == "" {
		status.AuthProbe = &config.CredentialAuthProbe{Status: "skipped", Message: "auth probe requires --owner and --repo"}
		return status
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	provider, err := gitcode.NewLiveProvider(gitcode.ProviderConfig{Mode: gitcode.ProviderModeLive, LiveAllowed: true, BaseURL: eff.Config.GitCodeBaseURL, Token: token, Timeout: eff.Config.DefaultTimeout, MaxResponseSize: eff.Config.MaxResponseSize, MaxRetries: eff.Config.MaxRetries})
	if err != nil {
		status.AuthProbe = &config.CredentialAuthProbe{Status: "failed", FailureClass: "auth-failure", Message: "auth-failure: unable to initialize live auth probe"}
		return status
	}
	_, err = provider.ProbeAuth(probeCtx, gitcode.AuthProbeRequest{Owner: owner, Repo: repo})
	if err != nil {
		failureClass := "auth-probe-failed"
		var authErr gitcode.ErrAuthExpired
		var forbiddenErr gitcode.ErrForbidden
		if errors.As(err, &authErr) || errors.As(err, &forbiddenErr) {
			failureClass = "auth-failure"
		}
		status.AuthProbe = &config.CredentialAuthProbe{Status: "failed", FailureClass: failureClass, Message: config.RedactDiagnostic(err.Error(), src)}
		return status
	}
	status.AuthProbe = &config.CredentialAuthProbe{Status: "ok"}
	return status
}

func sanitizeCredentialStatus(status config.CredentialStatus, src config.Source) config.CredentialStatus {
	status.Source = config.RedactDiagnostic(status.Source, src)
	status.ErrorClass = config.RedactDiagnostic(status.ErrorClass, src)
	status.Remediation = config.RedactDiagnostic(status.Remediation, src)
	status.RedactedToken = config.RedactDiagnostic(status.RedactedToken, src)
	if status.Present && status.RedactedToken == "" {
		status.RedactedToken = credential.ResolvedToken{Value: config.Token(src)}.RedactToken()
	}
	for i := range status.AvailableSources {
		status.AvailableSources[i] = config.RedactDiagnostic(status.AvailableSources[i], src)
	}
	if status.AuthProbe != nil {
		probe := *status.AuthProbe
		probe.Status = config.RedactDiagnostic(probe.Status, src)
		probe.FailureClass = config.RedactDiagnostic(probe.FailureClass, src)
		probe.Message = config.RedactDiagnostic(probe.Message, src)
		status.AuthProbe = &probe
	}
	return status
}

func dispatch(ctx context.Context, svc queryService, command string, args []string, opts options, stdout io.Writer, stderr io.Writer, plan startupPlan) int {
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
	case "search", "search_sources":
		if len(args) == 0 {
			return writeError(stderr, opts.format, service.ErrInvalidQuery{Field: "query", Message: "query is required"})
		}
		results, err := svc.SearchSources(ctx, service.SearchSourcesRequest{RepoID: opts.repo, Query: strings.Join(args, " "), Kind: opts.kind, Provenance: opts.provenance, Limit: opts.limit, Offset: opts.offset})
		if err != nil {
			return writeError(stderr, opts.format, err)
		}
		return render(stdout, opts.format, results, renderSearchText)
	case "list":
		results, err := svc.ListSources(ctx, service.ListSourcesRequest{RepoID: opts.repo, Kind: opts.kind, Status: opts.status, Provenance: opts.provenance, Limit: opts.limit, Offset: opts.offset})
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
		results, err := svc.GetBacklinks(ctx, service.GetBacklinksRequest{RepoID: opts.repo, ID: id, Limit: opts.limit, Offset: opts.offset})
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
	case "pr-discussions", "pr-review-discussions":
		result, err := svc.ListPRDiscussions(ctx, service.PRDiscussionRequest{RepoID: opts.repo, Number: opts.number, UnresolvedOnly: opts.unresolvedOnly})
		if err != nil {
			return writeError(stderr, opts.format, err)
		}
		return render(stdout, opts.format, result, renderPRDiscussionsText)
	case "sync":
		if opts.issues || opts.wiki || opts.pulls || opts.comments || (opts.id == "" && opts.input == "") {
			req := bulkSyncRequest(opts)
			started := time.Now().UTC()
			progressCh, stopProgress := startSyncProgress(stderr, started, syncProgressMode(opts, stderr))
			req.ProgressChan = progressCh
			if req.Bounds != nil {
				req.Bounds.ProgressChan = progressCh
			}
			defer stopProgress()
			var result *service.SyncResourcesResult
			var syncErr error
			if !opts.issues && !opts.wiki && !opts.pulls && !opts.comments {
				result, syncErr = svc.BulkSyncAll(ctx, req)
				stopProgress()
				return renderSyncResources(stdout, stderr, opts.format, opts.details, result, syncErr, plan, started)
			}
			aggregate := &service.SyncResourcesResult{Results: []service.SyncResult{}, Failures: []service.ResourceError{}}
			runBulk := func(fn func(context.Context, service.BulkSyncRequest) (*service.SyncResourcesResult, error)) {
				part, err := fn(ctx, req)
				mergeSyncResources(aggregate, part)
				if err != nil {
					syncErr = mergeSyncError(syncErr, aggregate, err)
				}
			}
			if opts.issues {
				runBulk(svc.BulkSyncIssues)
			}
			if opts.wiki {
				runBulk(svc.BulkSyncWiki)
			}
			if opts.pulls {
				runBulk(svc.BulkSyncPullRequests)
			}
			if opts.comments {
				runBulk(svc.BulkSyncPRComments)
			}
			result = aggregate
			if result.SuccessCount == 0 && result.FailureCount == 0 {
				result.SuccessCount = len(result.Results)
				result.FailureCount = len(result.Failures)
			}
			stopProgress()
			return renderSyncResources(stdout, stderr, opts.format, opts.details, result, syncErr, plan, started)
		}
		result, err := svc.SyncToCache(ctx, service.SyncRequest{RepoID: opts.repo, StableID: opts.id, RemoteAlias: opts.input, IdempotencyKey: opts.idempotencyKey})
		if err != nil {
			return writeError(stderr, opts.format, err)
		}
		return render(stdout, opts.format, result, renderSyncText)
	case "cache":
		sub, ok := firstArg(args)
		if !ok {
			return writeError(stderr, opts.format, service.ErrInvalidQuery{Field: "cache", Message: "subcommand is required"})
		}
		switch sub {
		case "reset":
			if !opts.live {
				return writeError(stderr, opts.format, service.ErrInvalidQuery{Field: "live", Message: "cache reset requires --live"})
			}
			result, err := svc.ResetLiveCache(ctx, service.ResetLiveCacheRequest{RepoID: opts.repo})
			if err != nil {
				return writeError(stderr, opts.format, err)
			}
			return render(stdout, opts.format, result, renderResetLiveCacheText)
		default:
			return writeError(stderr, opts.format, service.ErrInvalidQuery{Field: "cache", Message: "unknown subcommand"})
		}
	case "cache-status":
		result, err := svc.CacheStatus(ctx, service.CacheStatusRequest{RepoID: opts.repo})
		if err != nil {
			return writeError(stderr, opts.format, err)
		}
		return render(stdout, opts.format, result, renderCacheStatusText)
	case "sync-status", "sync_status":
		id, _ := firstArg(args)
		if id != "" {
			result, err := svc.GetSyncStatus(ctx, service.SyncStatusRequest{RepoID: opts.repo, ID: id})
			if err != nil {
				return writeError(stderr, opts.format, err)
			}
			return render(stdout, opts.format, result, renderSyncStatusText)
		}
		result, err := svc.SyncStatus(ctx, service.ListSourcesRequest{RepoID: opts.repo, Kind: opts.kind, Status: opts.status, Limit: opts.limit, Offset: opts.offset})
		if err != nil {
			return writeError(stderr, opts.format, err)
		}
		if !opts.details && opts.format == "json" {
			return renderJSON(stdout, syncStatusCompactSummaryFromResult(result))
		}
		return render(stdout, opts.format, result, renderSyncStatusSummaryText)
	case "export", "export-snapshot":
		result, err := svc.ExportSnapshot(ctx, service.ExportSnapshotRequest{RepoID: opts.repo, SnapshotID: firstNonEmpty(opts.id, opts.snapshotID), Format: opts.format, OutputPath: opts.output, IncludeBody: true})
		if err != nil {
			return writeError(stderr, opts.format, err)
		}
		if opts.format == "json" {
			fmt.Fprint(stdout, result.InlineContent)
			return 0
		}
		return render(stdout, opts.format, result, renderExportText)
	case "diff", "diff-snapshot":
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
		return dispatchWrite(ctx, svc.CreateIssue, command, opts, stdout, stderr, plan)
	case "update-issue":
		return dispatchWrite(ctx, svc.UpdateIssue, command, opts, stdout, stderr, plan)
	case "create-pr", "create-mr":
		return dispatchWrite(ctx, svc.CreatePR, "create-pr", opts, stdout, stderr, plan)
	case "create-page":
		return dispatchWrite(ctx, svc.CreatePage, command, opts, stdout, stderr, plan)
	case "update-page":
		return dispatchWrite(ctx, svc.UpdatePage, command, opts, stdout, stderr, plan)
	case "delete-page":
		return dispatchWrite(ctx, svc.DeletePage, command, opts, stdout, stderr, plan)
	case "add-comment":
		return dispatchWrite(ctx, svc.AddComment, command, opts, stdout, stderr, plan)
	case "add-pr-review-comment":
		return dispatchWrite(ctx, svc.AddPRReviewComment, command, opts, stdout, stderr, plan)
	case "update-comment":
		return dispatchWrite(ctx, svc.UpdateComment, command, opts, stdout, stderr, plan)
	case "add-label":
		return dispatchWrite(ctx, svc.AddLabel, command, opts, stdout, stderr, plan)
	case "publish-release":
		return dispatchPublishRelease(ctx, svc, opts, stdout, stderr, plan)
	default:
		return writeError(stderr, opts.format, service.ErrInvalidQuery{Field: "command", Message: command + " is not a query command"})
	}
}

func dispatchPublishRelease(ctx context.Context, svc queryService, opts options, stdout io.Writer, stderr io.Writer, plan startupPlan) int {
	if err := validateWriteOptions("publish-release", opts); err != nil {
		return writeCommandError(stderr, opts.format, plan, err)
	}
	req, err := publishReleaseRequest(opts)
	if err != nil {
		return writeCommandError(stderr, opts.format, plan, err)
	}
	result, err := svc.PublishRelease(ctx, req)
	if err != nil {
		return writeCommandError(stderr, opts.format, plan, err)
	}
	return render(stdout, opts.format, result, renderPublishReleaseText)
}

func dispatchWrite(ctx context.Context, handler func(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error), command string, opts options, stdout io.Writer, stderr io.Writer, plan startupPlan) int {
	if err := validateWriteOptions(command, opts); err != nil {
		return writeCommandError(stderr, opts.format, plan, err)
	}
	result, err := handler(ctx, writeRequest(opts))
	if err != nil {
		return writeCommandError(stderr, opts.format, plan, err)
	}
	return render(stdout, opts.format, result, renderWriteText)
}

func validateWriteOptions(command string, opts options) error {
	if strings.TrimSpace(opts.repo) == "" {
		return service.ErrRepoRequired{Operation: command}
	}
	if strings.TrimSpace(opts.owner) != "" || strings.TrimSpace(opts.name) != "" || strings.TrimSpace(opts.apiBaseURL) != "" {
		return service.ErrInvalidQuery{Field: "write_scope", Message: "write commands accept only --repo configured repo id"}
	}
	if opts.live && opts.dryRun {
		return service.ErrInvalidQuery{Field: "write_mode", Message: "--live conflicts with --dry-run"}
	}
	if (opts.offline || opts.fixture) && !opts.dryRun {
		return service.ErrInvalidQuery{Field: "write_mode", Message: "offline write commands require --dry-run"}
	}
	return nil
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

func bulkSyncRequest(opts options) service.BulkSyncRequest {
	perPage := opts.perPage
	if perPage <= 0 {
		perPage = 100
	}
	return service.BulkSyncRequest{RepoID: opts.repo, IdempotencyKey: opts.idempotencyKey, PerPage: perPage, Bounds: &service.SyncBounds{MaxPages: opts.maxPages, MaxRecords: opts.maxRecords}}
}

func syncProgressMode(opts options, w io.Writer) string {
	if opts.quiet {
		return "off"
	}
	if opts.progress == "" || opts.progress == "auto" {
		if isTerminalWriter(w) {
			return "spinner"
		}
		return "lines"
	}
	return opts.progress
}

func startSyncProgress(w io.Writer, started time.Time, mode string) (chan service.ProgressEvent, func()) {
	if w == nil || mode == "off" {
		return nil, func() {}
	}
	if mode == "spinner" {
		return startSyncProgressSpinner(w, started)
	}
	ch := make(chan service.ProgressEvent, 32)
	done := make(chan struct{})
	go func() {
		defer close(done)
		encoder := json.NewEncoder(w)
		for ev := range ch {
			switch mode {
			case "jsonl":
				_ = encoder.Encode(syncProgressJSONEvent(ev, started))
			default:
				renderSyncProgressLine(w, ev, started)
			}
		}
	}()
	stopped := false
	return ch, func() {
		if stopped {
			return
		}
		stopped = true
		close(ch)
		<-done
	}
}

func isTerminalWriter(w io.Writer) bool {
	file, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func startSyncProgressSpinner(w io.Writer, started time.Time) (chan service.ProgressEvent, func()) {
	ch := make(chan service.ProgressEvent, 32)
	done := make(chan struct{})
	go func() {
		defer close(done)
		state := syncProgressSpinnerState{Started: started}
		ticker := time.NewTicker(120 * time.Millisecond)
		defer ticker.Stop()
		rendered := false
		for {
			select {
			case ev, ok := <-ch:
				if !ok {
					if rendered {
						fmt.Fprint(w, "\r\033[K")
					}
					return
				}
				state.Apply(ev)
				renderSyncProgressSpinnerFrame(w, &state)
				rendered = true
			case <-ticker.C:
				renderSyncProgressSpinnerFrame(w, &state)
				rendered = true
			}
		}
	}()
	stopped := false
	return ch, func() {
		if stopped {
			return
		}
		stopped = true
		close(ch)
		<-done
	}
}

type syncProgressSpinnerState struct {
	Started        time.Time
	Frame          int
	Type           string
	Collection     string
	Phase          string
	Page           int
	RecordsFetched int
	RecordsListed  int
	RateLimitState string
	RetryAfter     string
	Endpoint       string
	Message        string
}

func (s *syncProgressSpinnerState) Apply(ev service.ProgressEvent) {
	s.Type = syncProgressType(ev)
	if ev.Collection != "" {
		s.Collection = ev.Collection
	}
	if ev.Phase != "" {
		s.Phase = ev.Phase
	}
	if ev.Page > 0 {
		s.Page = ev.Page
	}
	s.RecordsFetched += ev.RecordsFetched
	s.RecordsListed += ev.RecordsListed
	if ev.RateLimitState != "" {
		s.RateLimitState = ev.RateLimitState
	}
	if ev.RetryAfter != "" {
		s.RetryAfter = ev.RetryAfter
	}
	if ev.Endpoint != "" {
		s.Endpoint = ev.Endpoint
	}
	if ev.Message != "" {
		s.Message = ev.Message
	}
}

func renderSyncProgressSpinnerFrame(w io.Writer, state *syncProgressSpinnerState) {
	frames := []string{"-", "\\", "|", "/"}
	frame := frames[state.Frame%len(frames)]
	state.Frame++
	fmt.Fprintf(w, "\r\033[K%s sync", frame)
	if state.Collection != "" {
		fmt.Fprintf(w, " %s", state.Collection)
	}
	if state.Phase != "" {
		fmt.Fprintf(w, " %s", state.Phase)
	}
	if state.Page > 0 {
		fmt.Fprintf(w, " p%d", state.Page)
	}
	if state.RecordsFetched > 0 {
		fmt.Fprintf(w, " %d rec", state.RecordsFetched)
	} else if state.RecordsListed > 0 {
		fmt.Fprintf(w, " %d listed", state.RecordsListed)
	}
	if state.RateLimitState != "" {
		fmt.Fprint(w, " wait")
	}
	if state.RetryAfter != "" {
		fmt.Fprintf(w, " %s", state.RetryAfter)
	}
	fmt.Fprintf(w, " elapsed=%s", time.Since(state.Started).Round(time.Millisecond))
}

type syncProgressEventJSON struct {
	service.ProgressEvent
	Type      string `json:"type"`
	ElapsedMS int64  `json:"elapsed_ms"`
}

func syncProgressJSONEvent(ev service.ProgressEvent, started time.Time) syncProgressEventJSON {
	return syncProgressEventJSON{
		ProgressEvent: ev,
		Type:          syncProgressType(ev),
		ElapsedMS:     time.Since(started).Milliseconds(),
	}
}

func renderSyncProgressLine(w io.Writer, ev service.ProgressEvent, started time.Time) {
	fmt.Fprintf(w, "sync progress: type=%s", syncProgressType(ev))
	if ev.Collection != "" {
		fmt.Fprintf(w, " collection=%s", ev.Collection)
	}
	if ev.Phase != "" {
		fmt.Fprintf(w, " phase=%s", ev.Phase)
	}
	if ev.Page > 0 {
		fmt.Fprintf(w, " page=%d", ev.Page)
	}
	if ev.RecordsListed > 0 {
		fmt.Fprintf(w, " listed=%d", ev.RecordsListed)
	}
	if ev.RecordsFetched > 0 {
		fmt.Fprintf(w, " records=%d", ev.RecordsFetched)
	}
	if ev.RecordsInserted > 0 {
		fmt.Fprintf(w, " inserted=%d", ev.RecordsInserted)
	}
	if ev.RecordsUpdated > 0 {
		fmt.Fprintf(w, " updated=%d", ev.RecordsUpdated)
	}
	if ev.RecordsSkipped > 0 {
		fmt.Fprintf(w, " skipped=%d", ev.RecordsSkipped)
	}
	if ev.RecordsFailed > 0 {
		fmt.Fprintf(w, " failed=%d", ev.RecordsFailed)
	}
	if ev.RateLimitState != "" {
		fmt.Fprintf(w, " rate_limit=%s", ev.RateLimitState)
	}
	if ev.RetryAfter != "" {
		fmt.Fprintf(w, " retry_after=%s", ev.RetryAfter)
	}
	if ev.ResumeAt != "" {
		fmt.Fprintf(w, " resume_at=%s", ev.ResumeAt)
	}
	if ev.Attempt > 0 {
		fmt.Fprintf(w, " attempt=%d", ev.Attempt)
	}
	if ev.Endpoint != "" {
		fmt.Fprintf(w, " endpoint=%s", ev.Endpoint)
	}
	if ev.Message != "" {
		fmt.Fprintf(w, " message=%q", ev.Message)
	}
	fmt.Fprintf(w, " elapsed=%s\n", time.Since(started).Round(time.Millisecond))
}

func syncProgressType(ev service.ProgressEvent) string {
	if ev.Type != "" {
		return ev.Type
	}
	if ev.RateLimitState != "" || ev.RetryAfter != "" || ev.ResumeAt != "" {
		return "rate_limit"
	}
	if ev.Phase != "" {
		return "phase"
	}
	return "records"
}

func mergeSyncResources(dst *service.SyncResourcesResult, src *service.SyncResourcesResult) {
	if dst == nil || src == nil {
		return
	}
	dst.Results = append(dst.Results, src.Results...)
	dst.Failures = append(dst.Failures, src.Failures...)
	dst.SuccessCount = len(dst.Results)
	dst.FailureCount = len(dst.Failures)
	dst.PagesListed += src.PagesListed
	dst.RecordsListed += src.RecordsListed
	dst.SkippedByWatermark += src.SkippedByWatermark
	if src.StopReason != "" {
		if dst.StopReason == "" {
			dst.StopReason = src.StopReason
		} else if dst.StopReason != src.StopReason {
			dst.StopReason = "mixed"
		}
	}
	if src.Ordering != "" {
		if dst.Ordering == "" {
			dst.Ordering = src.Ordering
		} else if dst.Ordering != src.Ordering {
			dst.Ordering = "mixed"
		}
	}
	if src.TraversalStatus != "" {
		if dst.TraversalStatus == "" {
			dst.TraversalStatus = src.TraversalStatus
		} else if dst.TraversalStatus != src.TraversalStatus {
			dst.TraversalStatus = "mixed"
		}
	}
	if src.WatermarkStatus != "" {
		if dst.WatermarkStatus == "" {
			dst.WatermarkStatus = src.WatermarkStatus
		} else if dst.WatermarkStatus != src.WatermarkStatus {
			dst.WatermarkStatus = "mixed"
		}
	}
	if src.WatermarkReason != "" {
		if dst.WatermarkReason == "" {
			dst.WatermarkReason = src.WatermarkReason
		} else if dst.WatermarkReason != src.WatermarkReason {
			dst.WatermarkReason = "mixed"
		}
	}
}

func mergeSyncError(existing error, result *service.SyncResourcesResult, err error) error {
	if err == nil {
		return existing
	}
	if existing == nil {
		return err
	}
	if result == nil {
		return existing
	}
	return &service.PartialSyncError{Errors: result.Failures, SuccessCount: result.SuccessCount, FailureCount: result.FailureCount}
}

type syncResourcesCompactSummary struct {
	Status             string                    `json:"status"`
	SuccessCount       int                       `json:"success_count"`
	FailureCount       int                       `json:"failure_count"`
	Counts             service.SyncCounts        `json:"counts"`
	PagesListed        int                       `json:"pages_listed,omitempty"`
	RecordsListed      int                       `json:"records_listed,omitempty"`
	SkippedByWatermark int                       `json:"skipped_by_watermark,omitempty"`
	StopReason         string                    `json:"stop_reason,omitempty"`
	Ordering           string                    `json:"ordering,omitempty"`
	TraversalStatus    string                    `json:"traversal_status,omitempty"`
	WatermarkStatus    string                    `json:"watermark_status,omitempty"`
	WatermarkReason    string                    `json:"watermark_reason,omitempty"`
	ZeroDeltaCount     int                       `json:"zero_delta_count,omitempty"`
	Diagnostic         service.SyncDiagnostic    `json:"diagnostic,omitempty"`
	TotalRequested     int                       `json:"total_requested,omitempty"`
	FailureGroups      []syncFailureGroupSummary `json:"failure_groups,omitempty"`
	Elapsed            string                    `json:"elapsed"`
	StartedAt          time.Time                 `json:"started_at"`
	CompletedAt        time.Time                 `json:"completed_at"`
}

type syncFailureGroupSummary struct {
	RemoteType   string `json:"remote_type"`
	FailureClass string `json:"failure_class,omitempty"`
	Endpoint     string `json:"endpoint,omitempty"`
	StatusCode   int    `json:"status_code,omitempty"`
	Count        int    `json:"count"`
}

type syncStatusCompactSummary struct {
	RepoID              string    `json:"repo_id"`
	FreshCount          int       `json:"fresh_count"`
	StaleCount          int       `json:"stale_count"`
	UnknownCount        int       `json:"unknown_count"`
	MissingRemoteCount  int       `json:"missing_remote_count"`
	ResultCount         int       `json:"result_count"`
	LastSyncAt          time.Time `json:"last_sync_at"`
	LastSyncStartedAt   time.Time `json:"last_sync_started_at"`
	LastSyncCompletedAt time.Time `json:"last_sync_completed_at"`
	ZeroDelta           bool      `json:"zero_delta"`
	CacheEmpty          bool      `json:"cache_empty"`
	Limit               int       `json:"limit"`
	Offset              int       `json:"offset"`
	Warnings            []string  `json:"warnings,omitempty"`
}

func syncStatusCompactSummaryFromResult(result service.SyncStatusSummaryResult) syncStatusCompactSummary {
	summary := syncStatusCompactSummary{
		RepoID:              result.RepoID,
		FreshCount:          result.FreshCount,
		StaleCount:          result.StaleCount,
		ResultCount:         len(result.Results),
		LastSyncAt:          result.LastSyncAt,
		LastSyncStartedAt:   result.LastSyncStartedAt,
		LastSyncCompletedAt: result.LastSyncCompletedAt,
		ZeroDelta:           result.ZeroDelta,
		CacheEmpty:          result.CacheEmpty,
		Limit:               result.Limit,
		Offset:              result.Offset,
		Warnings:            result.Warnings,
	}
	for _, item := range result.Results {
		switch item.Freshness {
		case service.FreshnessMissingRemote:
			summary.MissingRemoteCount++
		case service.FreshnessUnknown:
			summary.UnknownCount++
		}
	}
	return summary
}

func renderSyncResources(stdout, stderr io.Writer, format string, details bool, result *service.SyncResourcesResult, syncErr error, plan startupPlan, started time.Time) int {
	if syncErr != nil {
		if partial, ok := syncErr.(*service.PartialSyncError); ok {
			if result != nil && len(result.Results) > 0 {
				if format == "json" {
					if details {
						_ = renderJSON(stdout, result)
					} else {
						_ = renderJSON(stdout, syncResourcesSummary(result, partial, started))
					}
				} else {
					if details {
						for _, r := range result.Results {
							renderSyncText(stdout, r)
						}
					} else {
						renderSyncResourcesSummaryText(stdout, syncResourcesSummary(result, partial, started))
					}
				}
			}
			if result != nil && len(result.Failures) > 0 {
				for _, f := range result.Failures {
					fmt.Fprintf(stderr, "sync: %s failed: %s\n", f.SourceID, f.Message)
				}
			}
			if result == nil || result.SuccessCount == 0 {
				return writeCommandError(stderr, format, plan, partial)
			}
			return 1
		}
		return writeCommandError(stderr, format, plan, syncErr)
	}
	if result != nil {
		if format == "json" {
			if details {
				return renderJSON(stdout, result)
			}
			return renderJSON(stdout, syncResourcesSummary(result, nil, started))
		}
		if details {
			for _, r := range result.Results {
				renderSyncText(stdout, r)
			}
			return 0
		}
		renderSyncResourcesSummaryText(stdout, syncResourcesSummary(result, nil, started))
	}
	return 0
}

func syncResourcesSummary(result *service.SyncResourcesResult, partial *service.PartialSyncError, started time.Time) syncResourcesCompactSummary {
	if started.IsZero() {
		started = time.Now().UTC()
	}
	completed := time.Now().UTC()
	summary := syncResourcesCompactSummary{
		Status:             "succeeded",
		SuccessCount:       0,
		FailureCount:       0,
		PagesListed:        0,
		RecordsListed:      0,
		SkippedByWatermark: 0,
		Elapsed:            completed.Sub(started).Round(time.Millisecond).String(),
		StartedAt:          started,
		CompletedAt:        completed,
	}
	if result != nil {
		summary.SuccessCount = result.SuccessCount
		summary.FailureCount = result.FailureCount
		summary.PagesListed = result.PagesListed
		summary.RecordsListed = result.RecordsListed
		summary.SkippedByWatermark = result.SkippedByWatermark
		summary.StopReason = result.StopReason
		summary.Ordering = result.Ordering
		summary.TraversalStatus = result.TraversalStatus
		summary.WatermarkStatus = result.WatermarkStatus
		summary.WatermarkReason = result.WatermarkReason
		for _, item := range result.Results {
			summary.Counts.Fetched += item.Counts.Fetched
			summary.Counts.Skipped += item.Counts.Skipped
			summary.Counts.Updated += item.Counts.Updated
			summary.Counts.Conflicts += item.Counts.Conflicts
			summary.Counts.Inserted += item.Counts.Inserted
			summary.Counts.Listed += item.Counts.Listed
			summary.Counts.FetchedDetail += item.Counts.FetchedDetail
			summary.Counts.SkippedByRevision += item.Counts.SkippedByRevision
			summary.Counts.Failed += item.Counts.Failed
			if item.ZeroDelta {
				summary.ZeroDeltaCount++
			}
		}
		summary.FailureGroups = syncFailureGroups(result.Failures)
	}
	if partial != nil {
		summary.Status = "partial"
		summary.Diagnostic = partial.Diagnostic
		summary.TotalRequested = partial.TotalRequested
		if summary.FailureCount == 0 {
			summary.FailureCount = partial.FailureCount
		}
		if len(summary.FailureGroups) == 0 {
			summary.FailureGroups = syncFailureGroups(partial.Errors)
		}
	} else if summary.FailureCount > 0 {
		summary.Status = "partial"
	}
	return summary
}

func syncFailureGroups(failures []service.ResourceError) []syncFailureGroupSummary {
	if len(failures) == 0 {
		return nil
	}
	type failureKey struct {
		remoteType   string
		failureClass string
		endpoint     string
		statusCode   int
	}
	counts := map[failureKey]int{}
	for _, failure := range failures {
		remoteType := failure.RemoteType
		if remoteType == "" {
			remoteType = "unknown"
		}
		key := failureKey{remoteType: remoteType, failureClass: failure.FailureClass, endpoint: failure.Endpoint, statusCode: failure.StatusCode}
		counts[key]++
	}
	keys := make([]failureKey, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].remoteType != keys[j].remoteType {
			return keys[i].remoteType < keys[j].remoteType
		}
		if keys[i].failureClass != keys[j].failureClass {
			return keys[i].failureClass < keys[j].failureClass
		}
		if keys[i].statusCode != keys[j].statusCode {
			return keys[i].statusCode < keys[j].statusCode
		}
		return keys[i].endpoint < keys[j].endpoint
	})
	out := make([]syncFailureGroupSummary, 0, len(keys))
	for _, key := range keys {
		out = append(out, syncFailureGroupSummary{RemoteType: key.remoteType, FailureClass: key.failureClass, Endpoint: key.endpoint, StatusCode: key.statusCode, Count: counts[key]})
	}
	return out
}

func renderSyncResourcesSummaryText(w io.Writer, summary syncResourcesCompactSummary) {
	fmt.Fprintf(w, "sync: %s success_count=%d failure_count=%d fetched=%d updated=%d inserted=%d skipped=%d conflicts=%d listed=%d fetched_detail=%d skipped_by_revision=%d zero_delta=%d elapsed=%s",
		summary.Status,
		summary.SuccessCount,
		summary.FailureCount,
		summary.Counts.Fetched,
		summary.Counts.Updated,
		summary.Counts.Inserted,
		summary.Counts.Skipped,
		summary.Counts.Conflicts,
		summary.Counts.Listed,
		summary.Counts.FetchedDetail,
		summary.Counts.SkippedByRevision,
		summary.ZeroDeltaCount,
		summary.Elapsed,
	)
	if summary.PagesListed > 0 || summary.RecordsListed > 0 || summary.SkippedByWatermark > 0 {
		fmt.Fprintf(w, " pages_listed=%d records_listed=%d skipped_by_watermark=%d", summary.PagesListed, summary.RecordsListed, summary.SkippedByWatermark)
	}
	if summary.StopReason != "" {
		fmt.Fprintf(w, " stop_reason=%s", summary.StopReason)
	}
	if summary.TraversalStatus != "" {
		fmt.Fprintf(w, " traversal_status=%s", summary.TraversalStatus)
	}
	if summary.WatermarkStatus != "" {
		fmt.Fprintf(w, " watermark_status=%s", summary.WatermarkStatus)
	}
	if summary.WatermarkReason != "" {
		fmt.Fprintf(w, " watermark_reason=%s", summary.WatermarkReason)
	}
	if summary.Diagnostic != "" {
		fmt.Fprintf(w, " diagnostic=%s", summary.Diagnostic)
	}
	if len(summary.FailureGroups) > 0 {
		parts := make([]string, 0, len(summary.FailureGroups))
		for _, group := range summary.FailureGroups {
			label := group.RemoteType
			if group.FailureClass != "" {
				label += "/" + group.FailureClass
			}
			if group.StatusCode != 0 {
				label += fmt.Sprintf("/%d", group.StatusCode)
			}
			if group.Endpoint != "" {
				label += "@" + group.Endpoint
			}
			parts = append(parts, fmt.Sprintf("%s:%d", label, group.Count))
		}
		fmt.Fprintf(w, " failure_groups=%s", strings.Join(parts, ","))
	}
	fmt.Fprintln(w)
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
	mode := service.WriteModeDryRun
	if !opts.dryRun {
		mode = service.WriteModeLive
	}
	return service.WriteCommandRequest{RepoID: opts.repo, Repo: opts.repo, Mode: mode, ID: opts.id, Number: opts.number, CommentID: opts.commentID, Slug: opts.slug, Path: opts.path, Line: opts.line, StartLine: opts.startLine, EndLine: opts.endLine, Position: opts.position, Sha: opts.sha, Title: opts.title, Body: opts.body, Head: opts.head, Base: opts.base, State: opts.state, Label: opts.label, Labels: labels, IdempotencyKey: opts.idempotencyKey}
}

func publishReleaseRequest(opts options) (service.PublishReleaseRequest, error) {
	body := opts.body
	if strings.TrimSpace(opts.input) != "" {
		if strings.TrimSpace(body) != "" {
			return service.PublishReleaseRequest{}, service.ErrInvalidQuery{Field: "body", Message: "--body conflicts with --input body file"}
		}
		data, err := os.ReadFile(opts.input)
		if err != nil {
			return service.PublishReleaseRequest{}, err
		}
		body = string(data)
	}
	assets := make([]service.PublishAssetLink, 0, len(opts.asset))
	for _, raw := range opts.asset {
		name, url, ok := strings.Cut(raw, "=")
		if !ok || strings.TrimSpace(name) == "" || strings.TrimSpace(url) == "" {
			return service.PublishReleaseRequest{}, service.ErrInvalidQuery{Field: "asset", Message: "asset must be name=url"}
		}
		assets = append(assets, service.PublishAssetLink{Name: strings.TrimSpace(name), URL: strings.TrimSpace(url)})
	}
	mode := service.WriteModeDryRun
	if !opts.dryRun {
		mode = service.WriteModeLive
	}
	return service.PublishReleaseRequest{RepoID: opts.repo, Repo: opts.repo, Mode: mode, Tag: opts.tag, Ref: firstNonEmpty(opts.ref, opts.base), Title: opts.title, Body: body, Status: opts.status, Assets: assets, IdempotencyKey: opts.idempotencyKey}, nil
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

func renderSearchText(w io.Writer, result service.SearchSourcesResult) {
	fmt.Fprintf(w, "search_mode: %s\n", cliSearchMode(result.SearchMode))
	for _, item := range result.Results {
		line := 0
		if item.LineStart != nil {
			line = *item.LineStart
		}
		fmt.Fprintf(w, "%s %s %s:%d:%s\n", item.RepoID, item.ID, item.Path, line, item.Snippet)
	}
}

func renderListText(w io.Writer, result service.ListSourcesResult) {
	for _, item := range result.Results {
		fmt.Fprintf(w, "%s %s %s %s %s %s\n", item.RepoID, item.ID, item.Kind, item.Status, item.Path, item.Title)
	}
}

func renderGetText(w io.Writer, result service.SourceRecord) {
	fmt.Fprintf(w, "repo_id: %s\nid: %s\nkind: %s\npath: %s\nremote_alias: %s\ntitle: %s\nstatus: %s\nbody:\n%s\n", result.RepoID, result.ID, result.Kind, result.Path, result.RemoteAlias, result.Title, result.Status, result.Body)
}

func renderBacklinksText(w io.Writer, result service.BacklinksResult) {
	for _, item := range result.Backlinks {
		fmt.Fprintf(w, "%s %s %s %s\n", item.ID, item.Path, item.Title, item.TargetID)
	}
}

func renderChunkQueryText(w io.Writer, result service.ChunkQueryResult) {
	if result.SearchMode != "" {
		fmt.Fprintf(w, "search_mode: %s\n", result.SearchMode)
	}
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

func cliSearchMode(mode string) string {
	if strings.TrimSpace(mode) == "" {
		return service.SearchModeFullText
	}
	return mode
}

func renderResetLiveCacheText(w io.Writer, result service.ResetLiveCacheResult) {
	fmt.Fprintf(w, "repo_id: %s\nreset: %s\n", result.RepoID, result.Reset)
}

func renderSyncStatusText(w io.Writer, result service.SyncStatusResult) {
	fmt.Fprintf(w, "%s %s %s %s %s %s\n", result.RepoID, result.SourceID, result.Status, result.RemoteType, result.RemoteID, result.LastFetchedAt.Format(time.RFC3339))
}

func renderSyncStatusSummaryText(w io.Writer, result service.SyncStatusSummaryResult) {
	fmt.Fprintf(w, "repo_id: %s\nfresh_count: %d\nstale_count: %d\ncache_empty: %t\nzero_delta: %t\n", result.RepoID, result.FreshCount, result.StaleCount, result.CacheEmpty, result.ZeroDelta)
}

func renderRecentText(w io.Writer, result service.RecentChangesResult) {
	for _, item := range result.Results {
		fmt.Fprintf(w, "%s %s %s %s %s\n", item.UpdatedAt.UTC().Format(time.RFC3339), item.RepoID, item.ID, item.Path, item.Title)
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

func renderPRDiscussionsText(w io.Writer, result service.PRDiscussionsResult) {
	fmt.Fprintf(w, "repo_id: %s\npull_request: %d\ndiscussions: %d\n", result.RepoID, result.Number, len(result.Discussions))
	for _, discussion := range result.Discussions {
		resolved := "unknown"
		if discussion.Resolved != nil {
			resolved = fmt.Sprintf("%t", *discussion.Resolved)
		}
		location := discussion.Path
		if discussion.Line > 0 {
			location = fmt.Sprintf("%s:%d", location, discussion.Line)
		}
		fmt.Fprintf(w, "%s %s resolved=%s comments=%d", discussion.ID, discussion.Kind, resolved, len(discussion.Comments))
		if location != "" {
			fmt.Fprintf(w, " %s", location)
		}
		fmt.Fprintln(w)
	}
}

func renderOperationText(w io.Writer, result service.OperationResult) {
	fmt.Fprintf(w, "%s: %s processed=%d evidence=%s\n", result.Command, result.Status, result.ProcessedCount, result.Evidence)
}

func renderSyncText(w io.Writer, result service.SyncResult) {
	extra := ""
	if result.Counts.Listed > 0 || result.Counts.FetchedDetail > 0 || result.Counts.SkippedByRevision > 0 || result.Counts.Failed > 0 {
		extra = fmt.Sprintf(" listed=%d fetched_detail=%d skipped_by_revision=%d failed=%d", result.Counts.Listed, result.Counts.FetchedDetail, result.Counts.SkippedByRevision, result.Counts.Failed)
	}
	fmt.Fprintf(w, "sync: %s fetched=%d updated=%d inserted=%d skipped=%d conflicts=%d%s idempotency_key=%s replayed=%t zero_delta=%t\n", result.Status, result.Counts.Fetched, result.Counts.Updated, result.Counts.Inserted, result.Counts.Skipped, result.Counts.Conflicts, extra, result.IdempotencyKey, result.Replayed, result.ZeroDelta)
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
	if result.RemoteSlug != "" {
		fmt.Fprintf(w, "remote_slug: %s\n", result.RemoteSlug)
	}
	if result.APIPath != "" {
		fmt.Fprintf(w, "api_path: %s\n", result.APIPath)
	}
	if result.CachePath != "" {
		fmt.Fprintf(w, "cache_path: %s\n", result.CachePath)
	}
	if result.BrowserURL != "" {
		fmt.Fprintf(w, "browser_url: %s\n", result.BrowserURL)
	}
}

func renderPublishReleaseText(w io.Writer, result service.PublishReleaseResult) {
	fmt.Fprintf(w, "%s: %s repo_id=%s tag=%s release_status=%d assets=%d idempotency_key=%s evidence=%s\n", result.Command, result.Status, result.RepoID, result.Tag, result.ReleaseStatus, len(result.AssetLinks), result.IdempotencyKey, result.Evidence)
}

func renderRepositoryBindingText(w io.Writer, result service.RepositoryBinding) {
	fmt.Fprintf(w, "repo_id: %s\nowner: %s\nname: %s\napi_base_url: %s\nscopes: %s\ndisplay_name: %s\naliases: %s\n", result.RepoID, result.Owner, result.Name, result.APIBaseURL, joinRepositoryScopes(result.Scopes), result.DisplayName, strings.Join(result.Aliases, ","))
}

func renderRepoLocalInitText(w io.Writer, result repoLocalInitResult) {
	fmt.Fprintf(w, "repo_root: %s\nconfig_path: %s\nconfig_status: %s\ngitignore_path: %s\ngitignore_updated: %t\ncache_path: %s\nbinding_status: %s\n", result.RepoRoot, result.ConfigPath, result.ConfigStatus, result.GitignorePath, result.GitignoreUpdated, result.CachePath, result.BindingStatus)
	renderRepositoryBindingText(w, result.Binding)
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
	return writeCommandError(stderr, format, startupPlan{}, err)
}

func writeCommandError(stderr io.Writer, format string, plan startupPlan, err error) int {
	code := exitCode(err)
	failureClass := failureClass(err)
	message := config.RedactDiagnostic(err.Error(), config.OSSource{})
	var diagnostic diagnostics.Diagnostic
	if plan.ProviderMode == "live-http" {
		diagnostic = diagnostics.Classify(err, diagnosticContext(plan, err))
		failureClass = string(diagnostic.Code)
		message = diagnostic.Message
	}
	if format == "json" {
		payload := map[string]any{"error": message, "exit_code": code, "failure_class": failureClass}
		if diagnostic.Code != "" {
			payload["http_attempted"] = diagnostic.HTTPAttempted
			payload["retryable"] = diagnostic.Retryable
			if len(diagnostic.Context) > 0 {
				payload["context"] = diagnostic.Context
			}
		}
		_ = json.NewEncoder(stderr).Encode(payload)
		return code
	}
	fmt.Fprintln(stderr, message)
	if failureClass != "" {
		fmt.Fprintf(stderr, "failure_class: %s\n", failureClass)
	}
	if diagnostic.Code != "" {
		fmt.Fprintf(stderr, "http_attempted: %t\n", diagnostic.HTTPAttempted)
		if readiness := diagnostic.Context["cache_readiness"]; readiness != "" {
			fmt.Fprintf(stderr, "cache_readiness: %s\n", readiness)
		}
	}
	return code
}

func diagnosticContext(plan startupPlan, err error) diagnostics.CommandContext {
	ctx := diagnostics.CommandContext{ProviderMode: plan.ProviderMode, Command: plan.Command, SelectedAPIBaseURL: plan.APIBaseURL, RepositoryBindingID: plan.LiveRepositoryBinding.RepoID, CachePathPresent: strings.TrimSpace(plan.CachePath) != "", AuditPathPresent: strings.TrimSpace(plan.LiveRepositoryBinding.AuditPath) != ""}
	var schemaErr *cache.SchemaVersionError
	if errors.As(err, &schemaErr) {
		ctx.CacheReadiness = "schema_blocked"
		ctx.CacheSchemaDetected = schemaErr.Compat.DetectedVersion
		ctx.CacheSchemaExpected = schemaErr.Compat.ExpectedVersion
	}
	var writeErr service.ErrWriteFailure
	if errors.As(err, &writeErr) {
		ctx.HTTPAttempted = writeErr.Code == "write_unauthorized" || writeErr.Code == "write_network_unavailable" || writeErr.Code == "write_provider_error" || writeErr.Code == "write_conflict" || writeErr.Code == "schema_decode"
		ctx.FixtureFallbackSentinel = writeErr.Code == "write_fixture_fallback_detected"
		ctx.MissingCredential = writeErr.Code == "write_missing_credential"
		ctx.UnsupportedPayload = writeErr.Code == "live_graph_invalid" || writeErr.Code == "unsupported_mock_payload"
		ctx.SchemaDecodeFailure = writeErr.Code == "schema_decode" || writeErr.PayloadSource == "partial_response"
		ctx.PayloadSource = writeErr.PayloadSource
		ctx.FailureSource = writeErr.PayloadSource
		ctx.LocalPayloadTooLarge = writeErr.PayloadSource == "local_body_limit"
	}
	var syncErr service.ErrSyncFailure
	if errors.As(err, &syncErr) {
		ctx.HTTPAttempted = syncErr.Mode == "live_auth_failure" || syncErr.Mode == "network_timeout" || syncErr.Mode == "rate_limited" || syncErr.Mode == "partial_response" || syncErr.Mode == "live_graph_invalid" || syncErr.Mode == "payload_too_large" || syncErr.Mode == "remote_not_found" || syncErr.Mode == "conflict" || syncErr.Mode == "remote_collision"
		ctx.UnsupportedPayload = syncErr.Mode == "live_graph_invalid"
		ctx.PayloadSource = syncErr.PayloadSource
		ctx.FailureSource = syncErr.PayloadSource
		ctx.LocalPayloadTooLarge = syncErr.Mode == "payload_too_large" && syncErr.PayloadSource == "local_body_limit"
		ctx.SchemaDecodeFailure = syncErr.Mode == "partial_response" || syncErr.Mode == "schema_decode"
		if syncErr.Mode == "partial_response" {
			ctx.FailureSource = "partial_response"
		}
	}
	var partialSync *service.PartialSyncError
	if errors.As(err, &partialSync) {
		switch partialSync.Diagnostic {
		case service.SyncDiagnosticTimeout, service.SyncDiagnosticCancelled:
			ctx.HTTPAttempted = true
			ctx.TransportFailure = true
			ctx.FailureSource = string(partialSync.Diagnostic)
		case service.SyncDiagnosticEmptyWiki:
			ctx.FailureSource = string(partialSync.Diagnostic)
		}
	}
	var apiValidation gitcode.ErrAPIValidation
	if errors.As(err, &apiValidation) {
		ctx.HTTPAttempted = true
		ctx.HTTPStatus = apiValidation.Status
		ctx.APIFailure = true
	}
	var network gitcode.ErrNetworkUnavailable
	if errors.As(err, &network) {
		ctx.HTTPAttempted = true
		ctx.HTTPStatus = network.Status
		ctx.TransportFailure = true
	}
	var notFound gitcode.ErrNotFound
	if errors.As(err, &notFound) {
		ctx.HTTPAttempted = true
		ctx.HTTPStatus = http.StatusNotFound
	}
	var conflict gitcode.ErrConflict
	if errors.As(err, &conflict) {
		ctx.HTTPAttempted = true
		ctx.HTTPStatus = conflict.Status
		if ctx.HTTPStatus == 0 {
			ctx.HTTPStatus = http.StatusConflict
		}
	}
	var remoteCollision gitcode.ErrRemoteCollision
	if errors.As(err, &remoteCollision) {
		ctx.HTTPAttempted = true
		ctx.HTTPStatus = http.StatusConflict
	}
	var remoteNotFound gitcode.ErrRemoteNotFound
	if errors.As(err, &remoteNotFound) {
		ctx.HTTPAttempted = true
		ctx.HTTPStatus = http.StatusNotFound
	}
	var rateLimited gitcode.ErrRateLimited
	if errors.As(err, &rateLimited) {
		ctx.HTTPAttempted = true
		ctx.HTTPStatus = http.StatusTooManyRequests
	}
	var auth gitcode.ErrAuthExpired
	if errors.As(err, &auth) {
		ctx.HTTPAttempted = true
		ctx.HTTPStatus = auth.Status
	}
	var forbidden gitcode.ErrForbidden
	if errors.As(err, &forbidden) {
		ctx.HTTPAttempted = true
		ctx.HTTPStatus = forbidden.Status
	}
	var tooLarge gitcode.ErrPayloadTooLarge
	if errors.As(err, &tooLarge) {
		ctx.HTTPAttempted = true
		ctx.FailureSource = tooLarge.Source
		ctx.LocalPayloadTooLarge = tooLarge.Source == "local_body_limit"
	}
	var partial gitcode.ErrPartialResponse
	if errors.As(err, &partial) {
		ctx.HTTPAttempted = true
		ctx.FailureSource = "partial_response"
		ctx.SchemaDecodeFailure = true
	}
	var schema *gitcode.ErrSchemaDecode
	if errors.As(err, &schema) {
		ctx.HTTPAttempted = true
		ctx.SchemaDecodeFailure = true
	}
	return ctx
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
	var missing config.MissingCredentialError
	if errors.As(err, &missing) {
		return "missing_credential"
	}
	var invalid service.ErrInvalidQuery
	if errors.As(err, &invalid) {
		return "invalid_query"
	}
	var conflict service.ErrConflict
	if errors.As(err, &conflict) {
		return "conflict"
	}
	var unsupported gitcode.ErrUnsupportedCapability
	if errors.As(err, &unsupported) {
		return "unsupported_capability"
	}
	var lockContention cache.ErrLockContention
	if errors.As(err, &lockContention) {
		return "cache_busy"
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
	var missing config.MissingCredentialError
	if errors.As(err, &missing) {
		return 1
	}
	var invalid service.ErrInvalidQuery
	if errors.As(err, &invalid) {
		return 4
	}
	var conflict service.ErrConflict
	if errors.As(err, &conflict) {
		return 6
	}
	var unsupported gitcode.ErrUnsupportedCapability
	if errors.As(err, &unsupported) {
		return 4
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

func executeDoctorCommand(ctx context.Context, opts options, plan startupPlan, stdout io.Writer, stderr io.Writer, deps localCommandDeps) int {
	_ = stderr
	cachePath := firstNonEmpty(plan.CachePath, opts.cachePath)
	var cred *config.CredentialStatus
	if plan.CredentialStatus.Source != "" || plan.CredentialStatus.Present {
		status := plan.CredentialStatus
		cred = &status
	}
	report, err := doctor.Build(ctx, doctor.Request{Version: buildinfo.Version, Source: deps.Source, CredentialReporter: deps.CredentialReporter, CredentialStatus: cred, CachePath: cachePath, Live: plan.ProviderMode == "live-http", ProviderMode: plan.ProviderMode, MCPToolAccess: plan.MCPToolAccess, APIBaseURL: plan.APIBaseURL, RepoID: opts.repo, LiveBinding: plan.LiveRepositoryBinding})
	if err != nil {
		return writeError(stderr, opts.format, err)
	}
	if opts.format == "json" {
		return renderJSON(stdout, report)
	}
	doctor.RenderText(stdout, report)
	return 0
}

type migrateCacheResult struct {
	CachePath   string `json:"cache_path"`
	FromVersion int    `json:"from_version"`
	ToVersion   int    `json:"to_version"`
	Status      string `json:"status"`
	Applied     []int  `json:"applied,omitempty"`
	BackupPath  string `json:"backup_path,omitempty"`
	Remediation string `json:"remediation,omitempty"`
}

type repoLocalInitResult struct {
	RepoRoot         string                    `json:"repo_root"`
	ConfigPath       string                    `json:"config_path"`
	ConfigStatus     string                    `json:"config_status"`
	GitignorePath    string                    `json:"gitignore_path"`
	GitignoreUpdated bool                      `json:"gitignore_updated"`
	CachePath        string                    `json:"cache_path"`
	BindingStatus    string                    `json:"binding_status"`
	Binding          service.RepositoryBinding `json:"binding"`
}

func executeRepoInitLocalCommand(ctx context.Context, opts options, stdout io.Writer, stderr io.Writer, deps localCommandDeps) int {
	if deps.Source == nil {
		deps.Source = config.OSSource{}
	}
	if strings.TrimSpace(opts.cachePath) != "" {
		return writeError(stderr, opts.format, service.ErrInvalidQuery{Field: "cache-path", Message: "repo init-local always selects <git-root>/.gitcode/mcp/cache.db; omit --cache-path"})
	}
	repoRoot, err := discoverGitRoot(deps.Source)
	if err != nil {
		return writeError(stderr, opts.format, err)
	}
	configPath := filepath.Join(repoRoot, ".gitcode", "gitcode-mcp.yaml")
	configStatus, err := ensureRepoLocalConfig(configPath, opts.overwrite)
	if err != nil {
		return writeError(stderr, opts.format, err)
	}
	gitignorePath := filepath.Join(repoRoot, ".gitignore")
	gitignoreUpdated, err := ensureRepoLocalGitignore(gitignorePath)
	if err != nil {
		return writeError(stderr, opts.format, err)
	}
	cachePath := filepath.Join(repoRoot, ".gitcode", "mcp", "cache.db")
	if err := ensureCacheParentDir(cachePath); err != nil {
		return writeError(stderr, opts.format, err)
	}
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		return writeError(stderr, opts.format, err)
	}
	defer store.Close()
	svc := service.New(store)
	apiBaseURL := strings.TrimSpace(opts.apiBaseURL)
	if apiBaseURL == "" {
		apiBaseURL = defaultAPIBaseURL(deps.Source)
	}
	scopes := strings.TrimSpace(opts.scopes)
	if scopes == "" {
		scopes = "issues,wiki,pulls,comments"
	}
	binding, bindingStatus, err := addOrReuseRepositoryBinding(ctx, svc, service.AddRepositoryRequest{RepoID: opts.repo, Owner: opts.owner, Name: opts.name, APIBaseURL: apiBaseURL, Scopes: []string{scopes}, DisplayName: opts.displayName, Aliases: []string(opts.alias)})
	if err != nil {
		return writeError(stderr, opts.format, err)
	}
	result := repoLocalInitResult{RepoRoot: repoRoot, ConfigPath: configPath, ConfigStatus: configStatus, GitignorePath: gitignorePath, GitignoreUpdated: gitignoreUpdated, CachePath: cachePath, BindingStatus: bindingStatus, Binding: binding}
	return render(stdout, opts.format, result, renderRepoLocalInitText)
}

func discoverGitRoot(src config.Source) (string, error) {
	cwd, err := os.Getwd()
	if wd, ok := src.(config.WorkingDirSource); ok {
		cwd, err = wd.WorkingDir()
	}
	if err != nil {
		return "", err
	}
	dir, err := filepath.Abs(cwd)
	if err != nil {
		return "", err
	}
	for {
		if pathExists(filepath.Join(dir, ".git"), src) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", service.ErrInvalidQuery{Field: "repo", Message: "repo init-local must run inside a Git worktree"}
		}
		dir = parent
	}
}

func pathExists(path string, src config.Source) bool {
	if _, err := os.Stat(path); err == nil {
		return true
	}
	if stat, ok := src.(config.StatSource); ok {
		if _, err := stat.Stat(path); err == nil {
			return true
		}
	}
	return false
}

func ensureRepoLocalConfig(path string, overwrite bool) (string, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("repo-local config: cannot create directory %s: %w", filepath.Dir(path), err)
	}
	const body = "cache_mode: repo-local\n"
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			return "", fmt.Errorf("repo-local config: cannot write %s: %w", path, err)
		}
		return "created", nil
	}
	if err != nil {
		return "", fmt.Errorf("repo-local config: cannot read %s: %w", path, err)
	}
	if overwrite {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			return "", fmt.Errorf("repo-local config: cannot overwrite %s: %w", path, err)
		}
		return "overwritten", nil
	}
	text := string(content)
	if hasRepoLocalCacheMode(text) {
		return "existing", nil
	}
	if hasCacheMode(text) {
		return "", service.ErrInvalidQuery{Field: "config", Message: "repo-local config already sets cache_mode; rerun with --overwrite or edit .gitcode/gitcode-mcp.yaml"}
	}
	next := strings.TrimRight(text, "\n")
	if next != "" {
		next += "\n"
	}
	next += body
	if err := os.WriteFile(path, []byte(next), 0o600); err != nil {
		return "", fmt.Errorf("repo-local config: cannot update %s: %w", path, err)
	}
	return "updated", nil
}

func hasRepoLocalCacheMode(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "cache_mode:") && strings.Contains(trimmed, "repo-local") {
			return true
		}
	}
	return false
}

func hasCacheMode(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "cache_mode:") {
			return true
		}
	}
	return false
}

func ensureRepoLocalGitignore(path string) (bool, error) {
	const rule = ".gitcode/mcp/"
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(path, []byte(rule+"\n"), 0o644); err != nil {
			return false, fmt.Errorf("gitignore: cannot write %s: %w", path, err)
		}
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("gitignore: cannot read %s: %w", path, err)
	}
	for _, line := range strings.Split(string(content), "\n") {
		if strings.TrimSpace(line) == rule {
			return false, nil
		}
	}
	next := string(content)
	if next != "" && !strings.HasSuffix(next, "\n") {
		next += "\n"
	}
	next += rule + "\n"
	if err := os.WriteFile(path, []byte(next), 0o644); err != nil {
		return false, fmt.Errorf("gitignore: cannot update %s: %w", path, err)
	}
	return true, nil
}

func defaultAPIBaseURL(src config.Source) string {
	eff, err := config.LoadEffective(src, config.Overrides{})
	if err == nil && strings.TrimSpace(eff.Config.GitCodeBaseURL) != "" {
		return eff.Config.GitCodeBaseURL
	}
	cfg, err := config.Load(src, config.Overrides{})
	if err == nil && strings.TrimSpace(cfg.GitCodeBaseURL) != "" {
		return cfg.GitCodeBaseURL
	}
	return config.Default().GitCodeBaseURL
}

func addOrReuseRepositoryBinding(ctx context.Context, svc *service.Service, req service.AddRepositoryRequest) (service.RepositoryBinding, string, error) {
	binding, err := svc.AddRepository(ctx, req)
	if err == nil {
		return binding, "created", nil
	}
	var conflict service.ErrConflict
	if !errors.As(err, &conflict) || strings.TrimSpace(req.RepoID) == "" {
		return service.RepositoryBinding{}, "", err
	}
	status, statusErr := svc.RepositoryStatus(ctx, service.RepositoryStatusRequest{RepoID: req.RepoID})
	if statusErr != nil {
		return service.RepositoryBinding{}, "", err
	}
	if strings.TrimSpace(req.Owner) != "" && status.Owner != strings.TrimSpace(req.Owner) {
		return service.RepositoryBinding{}, "", err
	}
	if strings.TrimSpace(req.Name) != "" && status.Name != strings.TrimSpace(req.Name) {
		return service.RepositoryBinding{}, "", err
	}
	return service.RepositoryBinding{RepoID: status.RepoID, Owner: status.Owner, Name: status.Name, APIBaseURL: status.APIBaseURL, Scopes: status.Scopes, DisplayName: status.DisplayName, Aliases: status.Aliases}, "existing", nil
}

func executeMigrateCacheCommand(ctx context.Context, opts options, stdout io.Writer, stderr io.Writer, deps localCommandDeps) int {
	if deps.Source == nil {
		deps.Source = config.OSSource{}
	}
	eff, err := config.LoadEffective(deps.Source, config.Overrides{CachePath: opts.cachePath})
	if err != nil {
		return writeError(stderr, opts.format, err)
	}
	cachePath := eff.Config.CachePath
	if err := ensureCacheParentDir(cachePath); err != nil {
		return writeError(stderr, opts.format, err)
	}

	result, err := cache.MigrateCacheWithConfirm(ctx, cachePath, false, cache.Confirmation{Confirmed: opts.confirm})
	if err != nil {
		return writeError(stderr, opts.format, err)
	}

	mr := migrateCacheResult{
		CachePath:   cachePath,
		FromVersion: result.FromVersion,
		ToVersion:   result.ToVersion,
		Applied:     result.Applied,
		BackupPath:  result.BackupPath,
	}

	if !result.Compatibility.Compatible && result.Compatibility.Remediation != "" {
		mr.Status = "incompatible"
		mr.Remediation = result.Compatibility.Remediation
	} else if result.FromVersion == 0 {
		mr.Status = "no_cache"
		mr.Remediation = "no initialized cache found; re-initialize the cache before migrating"
	} else if result.FromVersion <= 1 {
		mr.Status = "incompatible"
		mr.Remediation = fmt.Sprintf("cache schema version %d is incompatible with in-place migration; re-initialize the cache", result.FromVersion)
	} else if result.FromVersion > result.ToVersion {
		mr.Status = "incompatible"
		mr.Remediation = fmt.Sprintf("cache schema version %d is newer than the supported version %d; upgrade gitcode-mcp binary or re-initialize the cache", result.FromVersion, result.ToVersion)
	} else if result.FromVersion == result.ToVersion {
		mr.Status = "up_to_date"
	} else if len(result.Applied) == 0 && !opts.confirm {
		mr.Status = "confirmation_required"
		mr.Remediation = fmt.Sprintf("to migrate from schema version %d to %d, re-run with --confirm", result.FromVersion, result.ToVersion)
	} else {
		mr.Status = "migrated"
	}

	if opts.format == "json" {
		code := renderJSON(stdout, mr)
		if mr.Status == "incompatible" || mr.Status == "confirmation_required" {
			return 1
		}
		return code
	}
	renderMigrateCacheText(stdout, mr)
	if mr.Status == "incompatible" || mr.Status == "confirmation_required" {
		return 1
	}
	return 0
}

func renderMigrateCacheText(w io.Writer, mr migrateCacheResult) {
	fmt.Fprintf(w, "cache_path: %s\n", mr.CachePath)
	fmt.Fprintf(w, "from_version: %d\n", mr.FromVersion)
	fmt.Fprintf(w, "to_version: %d\n", mr.ToVersion)
	fmt.Fprintf(w, "status: %s\n", mr.Status)
	if len(mr.Applied) > 0 {
		fmt.Fprintf(w, "applied_migrations:")
		for _, v := range mr.Applied {
			fmt.Fprintf(w, " %d", v)
		}
		fmt.Fprintln(w)
	}
	if mr.BackupPath != "" {
		fmt.Fprintf(w, "backup_path: %s\n", mr.BackupPath)
	}
	if mr.Remediation != "" {
		fmt.Fprintf(w, "remediation: %s\n", mr.Remediation)
	}
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
	fmt.Fprintln(w, "  scoped live cache repair -> cache reset --live")
	fmt.Fprintln(w, "  minimum replacement sequence: sync -> search -> list -> get -> backlinks")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Global query flags:")
	fmt.Fprintln(w, "  --format text|json")
	fmt.Fprintln(w, "  --kind KIND")
	fmt.Fprintln(w, "  --status STATUS")
	fmt.Fprintln(w, "  --provenance live|fixture|remote|projection|bridge")
	fmt.Fprintln(w, "  --limit N")
	fmt.Fprintln(w, "  --offset N")
	fmt.Fprintln(w, "  --line-start N")
	fmt.Fprintln(w, "  --line-end N")
	fmt.Fprintln(w, "  --cache-path PATH")
	fmt.Fprintln(w, "  --strict")
	fmt.Fprintln(w, "  --full | --incremental")
	fmt.Fprintln(w, "  --input PATH --output PATH")
	fmt.Fprintln(w, "  --owner OWNER --repo REPO --name NAME --api-base-url URL --scopes issues,wiki --alias ALIAS")
	fmt.Fprintln(w, "  --number N --slug SLUG")
	fmt.Fprintln(w, "  record IDs are positional for get, backlinks, and snippet commands")
	fmt.Fprintln(w, "  --title TITLE --body BODY --label LABEL --labels A,B --tag TAG")
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

func printCommandHelp(command string, w io.Writer) {
	switch command {
	case "ingest":
		fmt.Fprintf(w, "Usage: gitcode-mcp %s --input PATH [--output PATH] [--strict]\n\n", command)
		fmt.Fprintln(w, "Ingest source documents into the cache from a file path.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --input PATH      input file path (required)")
		fmt.Fprintln(w, "  --output PATH     output report path")
		fmt.Fprintln(w, "  --strict          exit non-zero on findings")
		fmt.Fprintln(w, "  --cache-path PATH cache database path")
		fmt.Fprintln(w, "  --format FORMAT   output format (text, json)")
	case "index":
		fmt.Fprintf(w, "Usage: gitcode-mcp %s --repo REPO [--full | --incremental] [--strict]\n\n", command)
		fmt.Fprintln(w, "Build or update the text index for cached sources.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO       repository id")
		fmt.Fprintln(w, "  --full            run full index rebuild")
		fmt.Fprintln(w, "  --incremental     run incremental index")
		fmt.Fprintln(w, "  --strict          exit non-zero on findings")
		fmt.Fprintln(w, "  --cache-path PATH cache database path")
		fmt.Fprintln(w, "  --format FORMAT   output format (text, json)")
	case "search", "search_sources":
		fmt.Fprintf(w, "Usage: gitcode-mcp %s --repo REPO QUERY [--kind KIND] [--provenance PROVENANCE] [--limit N] [--offset N]\n\n", command)
		fmt.Fprintln(w, "Search cached sources with full-text matching. This is exact/token text search, not fuzzy or semantic retrieval.")
		fmt.Fprintln(w, "If no results are returned, retry with exact terms or keyword variants.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO       repository id")
		fmt.Fprintln(w, "  --kind KIND       filter by source kind (issue, wiki, doc, task)")
		fmt.Fprintln(w, "  --provenance P    filter by provenance (live, fixture, remote, projection, bridge)")
		fmt.Fprintln(w, "  --limit N         maximum results")
		fmt.Fprintln(w, "  --offset N        result offset")
		fmt.Fprintln(w, "  --cache-path PATH cache database path")
		fmt.Fprintln(w, "  --format FORMAT   output format (text, json)")
	case "list":
		fmt.Fprintf(w, "Usage: gitcode-mcp %s --repo REPO [--kind KIND] [--status STATUS] [--provenance PROVENANCE] [--limit N] [--offset N]\n\n", command)
		fmt.Fprintln(w, "List cached sources with optional filters.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO       repository id")
		fmt.Fprintln(w, "  --kind KIND       filter by source kind")
		fmt.Fprintln(w, "  --status STATUS   filter by status")
		fmt.Fprintln(w, "  --provenance P    filter by provenance (live, fixture, remote, projection, bridge)")
		fmt.Fprintln(w, "  --limit N         maximum results")
		fmt.Fprintln(w, "  --offset N        result offset")
		fmt.Fprintln(w, "  --cache-path PATH cache database path")
		fmt.Fprintln(w, "  --format FORMAT   output format (text, json)")
	case "get":
		fmt.Fprintf(w, "Usage: gitcode-mcp %s --repo REPO ID\n\n", command)
		fmt.Fprintln(w, "Retrieve a full source record by id.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO       repository id")
		fmt.Fprintln(w, "  --cache-path PATH cache database path")
		fmt.Fprintln(w, "  --format FORMAT   output format (text, json)")
	case "backlinks":
		fmt.Fprintf(w, "Usage: gitcode-mcp %s --repo REPO ID [--limit N] [--offset N]\n\n", command)
		fmt.Fprintln(w, "List sources that link to the given id.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO       repository id")
		fmt.Fprintln(w, "  --limit N         maximum results")
		fmt.Fprintln(w, "  --offset N        result offset")
		fmt.Fprintln(w, "  --cache-path PATH cache database path")
		fmt.Fprintln(w, "  --format FORMAT   output format (text, json)")
	case "get-snippet", "snippet", "snippets":
		fmt.Fprintf(w, "Usage: gitcode-mcp %s --repo REPO ID [--line-start N] [--line-end N]\n", command)
		fmt.Fprintf(w, "       gitcode-mcp %s --repo REPO [--source-id ID] [--record-id ID] [--snapshot-id ID] --chunk-id ID\n\n", command)
		fmt.Fprintln(w, "Retrieve a line range or chunk snippet from a source.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO       repository id")
		fmt.Fprintln(w, "  --line-start N    start line (1-indexed)")
		fmt.Fprintln(w, "  --line-end N      end line (1-indexed)")
		fmt.Fprintln(w, "  --source-id ID    source id for chunk addressing")
		fmt.Fprintln(w, "  --record-id ID    record id for chunk addressing")
		fmt.Fprintln(w, "  --snapshot-id ID  snapshot id for chunk addressing")
		fmt.Fprintln(w, "  --chunk-id ID     chunk id")
		fmt.Fprintln(w, "  --policy POLICY   chunk policy (heading)")
		fmt.Fprintln(w, "  --cache-path PATH cache database path")
		fmt.Fprintln(w, "  --format FORMAT   output format (text, json)")
	case "list-chunks":
		fmt.Fprintf(w, "Usage: gitcode-mcp %s --repo REPO [--source-id ID] [--record-id ID] [--snapshot-id ID] [--policy POLICY] [--limit N] [--offset N]\n\n", command)
		fmt.Fprintln(w, "List index chunks for cached sources.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO       repository id")
		fmt.Fprintln(w, "  --source-id ID    filter by source id")
		fmt.Fprintln(w, "  --record-id ID    filter by record id")
		fmt.Fprintln(w, "  --snapshot-id ID  filter by snapshot id")
		fmt.Fprintln(w, "  --policy POLICY   filter by chunk policy")
		fmt.Fprintln(w, "  --limit N         maximum results")
		fmt.Fprintln(w, "  --offset N        result offset")
		fmt.Fprintln(w, "  --cache-path PATH cache database path")
		fmt.Fprintln(w, "  --format FORMAT   output format (text, json)")
	case "recent":
		fmt.Fprintf(w, "Usage: gitcode-mcp %s --repo REPO [--kind KIND] [--status STATUS] [--limit N] [--offset N]\n\n", command)
		fmt.Fprintln(w, "List recently changed sources from cache.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO       repository id")
		fmt.Fprintln(w, "  --kind KIND       filter by source kind")
		fmt.Fprintln(w, "  --status STATUS   filter by status")
		fmt.Fprintln(w, "  --limit N         maximum results")
		fmt.Fprintln(w, "  --offset N        result offset")
		fmt.Fprintln(w, "  --cache-path PATH cache database path")
		fmt.Fprintln(w, "  --format FORMAT   output format (text, json)")
	case "link-check":
		fmt.Fprintf(w, "Usage: gitcode-mcp %s --repo REPO [--strict]\n\n", command)
		fmt.Fprintln(w, "Scan cached sources for broken cross-reference links.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO       repository id")
		fmt.Fprintln(w, "  --strict          exit non-zero on findings")
		fmt.Fprintln(w, "  --cache-path PATH cache database path")
		fmt.Fprintln(w, "  --format FORMAT   output format (text, json)")
	case "stale-index":
		fmt.Fprintf(w, "Usage: gitcode-mcp %s --repo REPO [--strict]\n\n", command)
		fmt.Fprintln(w, "Detect index entries with stale content hashes.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO       repository id")
		fmt.Fprintln(w, "  --strict          exit non-zero on findings")
		fmt.Fprintln(w, "  --cache-path PATH cache database path")
		fmt.Fprintln(w, "  --format FORMAT   output format (text, json)")
	case "sync":
		fmt.Fprintf(w, "Usage: gitcode-mcp %s [--offline|--fixture] --repo REPO [--issues] [--wiki] [--pulls] [--comments] [--index] [--details] [--id ID] [--input REMOTE_ALIAS] [--idempotency-key KEY]\n\n", command)
		fmt.Fprintln(w, "Synchronize cached records. Uses live GitCode by default; use --offline/--fixture for deterministic fixture sync.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --live              compatibility alias for live provider selection")
		fmt.Fprintln(w, "  --offline           use offline/fixture provider")
		fmt.Fprintln(w, "  --fixture           use fixture provider")
		fmt.Fprintln(w, "  --repo REPO         repository id")
		fmt.Fprintln(w, "  --issues            sync issue records")
		fmt.Fprintln(w, "  --wiki              sync wiki records")
		fmt.Fprintln(w, "  --pulls             sync pull request records")
		fmt.Fprintln(w, "  --comments          sync pull request comments")
		fmt.Fprintln(w, "  --index             build index after sync")
		fmt.Fprintln(w, "  --max-pages N       maximum pages to sync; omit to traverse until end/frontier")
		fmt.Fprintln(w, "  --max-records N     maximum records to sync; omit to traverse until end/frontier")
		fmt.Fprintln(w, "  --per-page N        records per page")
		fmt.Fprintln(w, "  --progress MODE     progress mode: auto, spinner, lines, jsonl, off")
		fmt.Fprintln(w, "  --quiet             suppress non-result progress output")
		fmt.Fprintln(w, "  --id ID             stable record id")
		fmt.Fprintln(w, "  --input ALIAS       remote alias for single-record sync")
		fmt.Fprintln(w, "  --idempotency-key KEY  idempotency key")
		fmt.Fprintln(w, "  --details, --records   include per-record sync results")
		fmt.Fprintln(w, "  --cache-path PATH   cache database path")
		fmt.Fprintln(w, "  --format FORMAT     output format (text, json)")
	case "pr-discussions", "pr-review-discussions":
		fmt.Fprintf(w, "Usage: gitcode-mcp %s --repo REPO --number N [--unresolved-only]\n\n", command)
		fmt.Fprintln(w, "List cached pull request review discussions grouped by discussion thread.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO         repository id (required)")
		fmt.Fprintln(w, "  --number N          pull request number (required)")
		fmt.Fprintln(w, "  --unresolved-only   only include unresolved or unknown-resolution discussions")
		fmt.Fprintln(w, "  --cache-path PATH   cache database path")
		fmt.Fprintln(w, "  --format FORMAT     output format (text, json)")
	case "cache":
		fmt.Fprintln(w, "Usage: gitcode-mcp cache reset --live --repo REPO")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Reset repo-scoped live GitCode cache records and sync frontiers without deleting the cache file or unrelated repositories.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --live            required safety acknowledgement")
		fmt.Fprintln(w, "  --repo REPO       repository id")
		fmt.Fprintln(w, "  --cache-path PATH cache database path")
		fmt.Fprintln(w, "  --format FORMAT   output format (text, json)")
	case "cache-status":
		fmt.Fprintf(w, "Usage: gitcode-mcp %s --repo REPO\n\n", command)
		fmt.Fprintln(w, "Report cache storage health and record counts.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO       repository id")
		fmt.Fprintln(w, "  --cache-path PATH cache database path")
		fmt.Fprintln(w, "  --format FORMAT   output format (text, json)")
	case "sync-status", "sync_status":
		fmt.Fprintf(w, "Usage: gitcode-mcp %s --repo REPO [ID] [--kind KIND] [--status STATUS] [--limit N] [--offset N] [--details]\n\n", command)
		fmt.Fprintln(w, "Report sync freshness for cached sources.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO       repository id")
		fmt.Fprintln(w, "  --kind KIND       filter by source kind")
		fmt.Fprintln(w, "  --status STATUS   filter by status")
		fmt.Fprintln(w, "  --limit N         maximum results")
		fmt.Fprintln(w, "  --offset N        result offset")
		fmt.Fprintln(w, "  --details, --records include per-record status results")
		fmt.Fprintln(w, "  --cache-path PATH cache database path")
		fmt.Fprintln(w, "  --format FORMAT   output format (text, json)")
	case "export", "export-snapshot":
		fmt.Fprintf(w, "Usage: gitcode-mcp %s --repo REPO [--id ID | --snapshot-id ID] [--output PATH]\n\n", command)
		fmt.Fprintln(w, "Export a deterministic snapshot of cached sources.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO       repository id")
		fmt.Fprintln(w, "  --id ID           snapshot id")
		fmt.Fprintln(w, "  --snapshot-id ID  snapshot id")
		fmt.Fprintln(w, "  --output PATH     output file path")
		fmt.Fprintln(w, "  --cache-path PATH cache database path")
		fmt.Fprintln(w, "  --format FORMAT   output format (text, json)")
	case "diff", "diff-snapshot":
		fmt.Fprintf(w, "Usage: gitcode-mcp %s --repo REPO [--base ID|PATH] [--head ID|PATH]\n\n", command)
		fmt.Fprintln(w, "Diff two snapshots or the current cache state.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO       repository id")
		fmt.Fprintln(w, "  --base ID|PATH    base snapshot id or path")
		fmt.Fprintln(w, "  --head ID|PATH    head snapshot id or path")
		fmt.Fprintln(w, "  --cache-path PATH cache database path")
		fmt.Fprintln(w, "  --format FORMAT   output format (text, json)")
	case "create-issue":
		fmt.Fprintf(w, "Usage: gitcode-mcp %s --repo REPO --title TITLE [--body BODY] [--state STATE] [--labels A,B] [--idempotency-key KEY]\n\n", command)
		fmt.Fprintln(w, "Create a new issue. Executes live by default; use --dry-run for no-mutation validation.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO         repository id (required)")
		fmt.Fprintln(w, "  --title TITLE       issue title (required)")
		fmt.Fprintln(w, "  --body BODY         issue body")
		fmt.Fprintln(w, "  --state STATE       issue state")
		fmt.Fprintln(w, "  --labels A,B        comma-separated labels")
		fmt.Fprintln(w, "  --idempotency-key KEY  idempotency key")
		fmt.Fprintln(w, "  --dry-run           validate without mutation")
		fmt.Fprintln(w, "  --live              compatibility alias for live write")
		fmt.Fprintln(w, "  --cache-path PATH   cache database path")
		fmt.Fprintln(w, "  --format FORMAT     output format (text, json)")
	case "update-issue":
		fmt.Fprintf(w, "Usage: gitcode-mcp %s --repo REPO --number N [--title TITLE] [--body BODY] [--state STATE] [--labels A,B] [--idempotency-key KEY]\n\n", command)
		fmt.Fprintln(w, "Update an existing issue. Executes live by default; use --dry-run for no-mutation validation.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO         repository id (required)")
		fmt.Fprintln(w, "  --number N          issue number (required)")
		fmt.Fprintln(w, "  --title TITLE       updated title")
		fmt.Fprintln(w, "  --body BODY         updated body")
		fmt.Fprintln(w, "  --state STATE       updated state")
		fmt.Fprintln(w, "  --labels A,B        comma-separated labels")
		fmt.Fprintln(w, "  --idempotency-key KEY  idempotency key")
		fmt.Fprintln(w, "  --dry-run           validate without mutation")
		fmt.Fprintln(w, "  --live              compatibility alias for live write")
		fmt.Fprintln(w, "  --cache-path PATH   cache database path")
		fmt.Fprintln(w, "  --format FORMAT     output format (text, json)")
	case "create-pr", "create-mr":
		fmt.Fprintf(w, "Usage: gitcode-mcp %s --repo REPO --title TITLE --head BRANCH --base BRANCH [--body BODY] [--idempotency-key KEY]\n\n", command)
		fmt.Fprintln(w, "Create a new pull request / merge request. Executes live by default; use --dry-run for no-mutation validation.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO         repository id (required)")
		fmt.Fprintln(w, "  --title TITLE       pull request title (required)")
		fmt.Fprintln(w, "  --head BRANCH       source branch (required)")
		fmt.Fprintln(w, "  --base BRANCH       target branch (required)")
		fmt.Fprintln(w, "  --body BODY         pull request body")
		fmt.Fprintln(w, "  --idempotency-key KEY  idempotency key")
		fmt.Fprintln(w, "  --dry-run           validate without mutation")
		fmt.Fprintln(w, "  --live              compatibility alias for live write")
		fmt.Fprintln(w, "  --cache-path PATH   cache database path")
		fmt.Fprintln(w, "  --format FORMAT     output format (text, json)")
	case "create-page":
		fmt.Fprintf(w, "Usage: gitcode-mcp %s --repo REPO --title TITLE --body BODY [--slug SLUG] [--idempotency-key KEY]\n\n", command)
		fmt.Fprintln(w, "Create a new wiki page. Executes live by default; use --dry-run for no-mutation validation.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO         repository id (required)")
		fmt.Fprintln(w, "  --title TITLE       page title (required)")
		fmt.Fprintln(w, "  --body BODY         page body (required)")
		fmt.Fprintln(w, "  --slug SLUG         page slug")
		fmt.Fprintln(w, "  --idempotency-key KEY  idempotency key")
		fmt.Fprintln(w, "  --dry-run           validate without mutation")
		fmt.Fprintln(w, "  --live              compatibility alias for live write")
		fmt.Fprintln(w, "  --cache-path PATH   cache database path")
		fmt.Fprintln(w, "  --format FORMAT     output format (text, json)")
	case "update-page":
		fmt.Fprintf(w, "Usage: gitcode-mcp %s --repo REPO --slug SLUG [--title TITLE] [--body BODY] [--idempotency-key KEY]\n\n", command)
		fmt.Fprintln(w, "Update an existing wiki page. Executes live by default; use --dry-run for no-mutation validation.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO         repository id (required)")
		fmt.Fprintln(w, "  --slug SLUG         page slug (required)")
		fmt.Fprintln(w, "  --title TITLE       updated title")
		fmt.Fprintln(w, "  --body BODY         updated body")
		fmt.Fprintln(w, "  --idempotency-key KEY  idempotency key")
		fmt.Fprintln(w, "  --dry-run           validate without mutation")
		fmt.Fprintln(w, "  --live              compatibility alias for live write")
		fmt.Fprintln(w, "  --cache-path PATH   cache database path")
		fmt.Fprintln(w, "  --format FORMAT     output format (text, json)")
	case "add-comment":
		fmt.Fprintf(w, "Usage: gitcode-mcp %s --repo REPO --number N --body BODY [--idempotency-key KEY]\n\n", command)
		fmt.Fprintln(w, "Add a comment to an issue. Executes live by default; use --dry-run for no-mutation validation.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO         repository id (required)")
		fmt.Fprintln(w, "  --number N          issue number (required)")
		fmt.Fprintln(w, "  --body BODY         comment body (required)")
		fmt.Fprintln(w, "  --idempotency-key KEY  idempotency key")
		fmt.Fprintln(w, "  --dry-run           validate without mutation")
		fmt.Fprintln(w, "  --live              compatibility alias for live write")
		fmt.Fprintln(w, "  --cache-path PATH   cache database path")
		fmt.Fprintln(w, "  --format FORMAT     output format (text, json)")
	case "add-pr-review-comment":
		fmt.Fprintf(w, "Usage: gitcode-mcp %s --repo REPO --number N --path PATH --body BODY (--line N | --position N) [--start-line N] [--end-line N] [--idempotency-key KEY]\n\n", command)
		fmt.Fprintln(w, "Create an inline pull request review comment. Executes live by default; use --dry-run for no-mutation validation.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO         repository id (required)")
		fmt.Fprintln(w, "  --number N          pull request number (required)")
		fmt.Fprintln(w, "  --path PATH         changed file path (required)")
		fmt.Fprintln(w, "  --line N            file line number")
		fmt.Fprintln(w, "  --position N        diff position")
		fmt.Fprintln(w, "  --start-line N      optional start line for ranges")
		fmt.Fprintln(w, "  --end-line N        optional end line for ranges")
		fmt.Fprintln(w, "  --body BODY         comment body (required)")
		fmt.Fprintln(w, "  --idempotency-key KEY  idempotency key")
		fmt.Fprintln(w, "  --dry-run           validate without mutation")
		fmt.Fprintln(w, "  --live              compatibility alias for live write")
		fmt.Fprintln(w, "  --cache-path PATH   cache database path")
		fmt.Fprintln(w, "  --format FORMAT     output format (text, json)")
	case "update-comment":
		fmt.Fprintf(w, "Usage: gitcode-mcp %s --repo REPO --comment-id ID --body BODY [--number N] [--idempotency-key KEY]\n\n", command)
		fmt.Fprintln(w, "Update an existing issue comment. Executes live by default; use --dry-run for no-mutation validation.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO         repository id (required)")
		fmt.Fprintln(w, "  --comment-id ID     issue comment id (required)")
		fmt.Fprintln(w, "  --number N          issue number hint for cache parent resolution")
		fmt.Fprintln(w, "  --body BODY         updated comment body (required)")
		fmt.Fprintln(w, "  --idempotency-key KEY  idempotency key")
		fmt.Fprintln(w, "  --dry-run           validate without mutation")
		fmt.Fprintln(w, "  --live              compatibility alias for live write")
		fmt.Fprintln(w, "  --cache-path PATH   cache database path")
		fmt.Fprintln(w, "  --format FORMAT     output format (text, json)")
	case "add-label":
		fmt.Fprintf(w, "Usage: gitcode-mcp %s --repo REPO --number N --label LABEL [--idempotency-key KEY]\n\n", command)
		fmt.Fprintln(w, "Add a label to an issue. Executes live by default; use --dry-run for no-mutation validation.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO         repository id (required)")
		fmt.Fprintln(w, "  --number N          issue number (required)")
		fmt.Fprintln(w, "  --label LABEL       label to add (required)")
		fmt.Fprintln(w, "  --idempotency-key KEY  idempotency key")
		fmt.Fprintln(w, "  --dry-run           validate without mutation")
		fmt.Fprintln(w, "  --live              compatibility alias for live write")
		fmt.Fprintln(w, "  --cache-path PATH   cache database path")
		fmt.Fprintln(w, "  --format FORMAT     output format (text, json)")
	case "publish-release":
		fmt.Fprintf(w, "Usage: gitcode-mcp %s --repo REPO --tag TAG --title TITLE (--body BODY | --input BODY.md) [--ref REF] [--status latest|prerelease|unset] [--asset NAME=URL] [--idempotency-key KEY]\n\n", command)
		fmt.Fprintln(w, "Create or update a GitCode release. Executes live by default; use --dry-run for no-mutation validation.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO         repository id (required)")
		fmt.Fprintln(w, "  --tag TAG           release tag (required)")
		fmt.Fprintln(w, "  --title TITLE       release title (required)")
		fmt.Fprintln(w, "  --body BODY         release description")
		fmt.Fprintln(w, "  --input PATH        release description file")
		fmt.Fprintln(w, "  --ref REF           source ref for release creation (default: main)")
		fmt.Fprintln(w, "  --status STATUS     latest, prerelease, or unset (default: latest)")
		fmt.Fprintln(w, "  --asset NAME=URL    release asset link; may be repeated")
		fmt.Fprintln(w, "  --idempotency-key KEY  idempotency key")
		fmt.Fprintln(w, "  --dry-run           validate without mutation")
		fmt.Fprintln(w, "  --live              compatibility alias for live write")
		fmt.Fprintln(w, "  --cache-path PATH   cache database path")
		fmt.Fprintln(w, "  --format FORMAT     output format (text, json)")
	case "config":
		fmt.Fprintf(w, "Usage: gitcode-mcp %s SUBCOMMAND\n\n", command)
		fmt.Fprintln(w, "Manage gitcode-mcp configuration.")
		fmt.Fprintln(w, "Subcommands:")
		fmt.Fprintln(w, "  init        create default config file")
		fmt.Fprintln(w, "  locate      show config file location")
		fmt.Fprintln(w, "  show        display effective config (requires --redacted)")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Run gitcode-mcp config SUBCOMMAND --help for details.")
	case "auth":
		fmt.Fprintf(w, "Usage: gitcode-mcp %s SUBCOMMAND\n\n", command)
		fmt.Fprintln(w, "Inspect authentication state.")
		fmt.Fprintln(w, "Subcommands:")
		fmt.Fprintln(w, "  status      report token source and credential state")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Run gitcode-mcp auth SUBCOMMAND --help for details.")
	case "doctor":
		fmt.Fprintf(w, "Usage: gitcode-mcp %s [--repo REPO] [--offline|--fixture] [--runtime-audit] [--cache-path PATH]\n\n", command)
		fmt.Fprintln(w, "Aggregate subsystem diagnostics with public-safe output.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO         repository id")
		fmt.Fprintln(w, "  --live              compatibility alias for live provider checks")
		fmt.Fprintln(w, "  --offline           report explicit offline/fixture provider mode")
		fmt.Fprintln(w, "  --fixture           report explicit fixture provider mode")
		fmt.Fprintln(w, "  --runtime-audit     emit runtime audit report")
		fmt.Fprintln(w, "  --cache-path PATH   cache database path")
		fmt.Fprintln(w, "  --format FORMAT     output format (text, json)")
	case "migrate-cache":
		fmt.Fprintf(w, "Usage: gitcode-mcp %s --confirm [--cache-path PATH]\n\n", command)
		fmt.Fprintln(w, "Run cache schema migration from supported older versions.")
		fmt.Fprintln(w, "A backup is created before migration at {cache-path}.backup-{timestamp}.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --confirm           required to apply migration")
		fmt.Fprintln(w, "  --cache-path PATH   cache database path")
		fmt.Fprintln(w, "  --format FORMAT     output format (text, json)")
	case "repo":
		fmt.Fprintf(w, "Usage: gitcode-mcp %s SUBCOMMAND\n\n", command)
		fmt.Fprintln(w, "Manage GitCode repository bindings.")
		fmt.Fprintln(w, "Subcommands:")
		fmt.Fprintln(w, "  add         bind a GitCode repository to the cache")
		fmt.Fprintln(w, "  init-local  create repo-local cache config and bind the repository")
		fmt.Fprintln(w, "  status      show repository binding status")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Run gitcode-mcp repo SUBCOMMAND --help for details.")
	case "bind":
		fmt.Fprintln(w, "Usage: gitcode-mcp bind --repo-owner OWNER --repo REPO")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Bind a GitCode repository to the cache. This compatibility help surface maps to repo add.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo-owner OWNER  repository owner (required)")
		fmt.Fprintln(w, "  --repo REPO         repository name (required)")
		fmt.Fprintln(w, "  --cache-path PATH   cache database path")
		fmt.Fprintln(w, "  --format FORMAT     output format (text, json)")
	default:
		fmt.Fprintf(w, "Usage: gitcode-mcp %s [flags]\n\n", command)
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --cache-path PATH cache database path")
		fmt.Fprintln(w, "  --format FORMAT   output format (text, json)")
		fmt.Fprintln(w, "  -h, --help        show help")
	}
}

func printLocalSubcommandHelp(command, sub string, w io.Writer) {
	switch command + " " + sub {
	case "config init":
		fmt.Fprintln(w, "Usage: gitcode-mcp config init [--overwrite]")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Create a default gitcode-mcp configuration file at the standard location.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --overwrite         overwrite existing config file")
	case "config locate":
		fmt.Fprintln(w, "Usage: gitcode-mcp config locate [--format FORMAT]")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Show the config file path, source, format, and existence.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --format FORMAT     output format (text, json)")
	case "config show":
		fmt.Fprintln(w, "Usage: gitcode-mcp config show --redacted [--format FORMAT]")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Display the effective configuration with credential status (public-safe).")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --redacted          required safety flag (MUST be set)")
		fmt.Fprintln(w, "  --format FORMAT     output format (text, json)")
	case "auth status":
		fmt.Fprintln(w, "Usage: gitcode-mcp auth status [--live] [--owner OWNER] [--repo REPO] [--format FORMAT]")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Report token source, credential state, and optional auth probe.")
		fmt.Fprintln(w, "Credential sources are checked in order: env GITCODE_TOKEN, keyring, none.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --live              probe GitCode API with token")
		fmt.Fprintln(w, "  --owner OWNER       repository owner (for auth probe)")
		fmt.Fprintln(w, "  --repo REPO         repository id (for auth probe)")
		fmt.Fprintln(w, "  --format FORMAT     output format (text, json)")
	case "repo add":
		fmt.Fprintln(w, "Usage: gitcode-mcp repo add --repo REPO --owner OWNER --name NAME --api-base-url URL --scopes SCOPES [--alias ALIAS] [--display-name NAME]")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Bind a GitCode repository to the cache.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO         repository id (required)")
		fmt.Fprintln(w, "  --owner OWNER       repository owner (required)")
		fmt.Fprintln(w, "  --name NAME         repository name (required)")
		fmt.Fprintln(w, "  --api-base-url URL  authoritative live API base URL (required)")
		fmt.Fprintln(w, "  --scopes SCOPES     comma-separated scopes (issues, wiki, pulls, comments)")
		fmt.Fprintln(w, "  --alias ALIAS       repository alias (repeatable)")
		fmt.Fprintln(w, "  --display-name NAME human-readable display name")
		fmt.Fprintln(w, "  --cache-path PATH   cache database path")
		fmt.Fprintln(w, "  --format FORMAT     output format (text, json)")
	case "repo init-local":
		fmt.Fprintln(w, "Usage: gitcode-mcp repo init-local --repo REPO --owner OWNER --name NAME [--api-base-url URL] [--scopes SCOPES] [--alias ALIAS] [--display-name NAME] [--overwrite]")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Create repo-local cache configuration in the current Git worktree and bind the repository without syncing.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO         repository id (required)")
		fmt.Fprintln(w, "  --owner OWNER       repository owner (required)")
		fmt.Fprintln(w, "  --name NAME         repository name (required)")
		fmt.Fprintln(w, "  --api-base-url URL  authoritative live API base URL (defaults to config)")
		fmt.Fprintln(w, "  --scopes SCOPES     comma-separated scopes (defaults to issues,wiki,pulls,comments)")
		fmt.Fprintln(w, "  --alias ALIAS       repository alias (repeatable)")
		fmt.Fprintln(w, "  --display-name NAME human-readable display name")
		fmt.Fprintln(w, "  --overwrite         replace existing repo-local config file")
		fmt.Fprintln(w, "  --format FORMAT     output format (text, json)")
	case "repo status":
		fmt.Fprintln(w, "Usage: gitcode-mcp repo status --repo REPO [--format FORMAT]")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Show repository binding status.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO         repository id (required)")
		fmt.Fprintln(w, "  --cache-path PATH   cache database path")
		fmt.Fprintln(w, "  --format FORMAT     output format (text, json)")
	default:
		fmt.Fprintf(w, "Usage: gitcode-mcp %s %s [flags]\n\n", command, sub)
	}
}
