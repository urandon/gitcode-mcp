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
	"gitcode-mcp/internal/credential"
	"gitcode-mcp/internal/diagnostics"
	"gitcode-mcp/internal/doctor"
	"gitcode-mcp/internal/gitcode"
	"gitcode-mcp/internal/index"
	"gitcode-mcp/internal/service"
)

const version = "0.1.0"

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
	"sync",
	"cache",
	"cache-status",
	"sync-status", "sync_status",
	"export", "export-snapshot",
	"diff", "diff-snapshot",
	"create-issue",
	"update-issue",
	"create-page",
	"update-page",
	"delete-page",
	"add-comment",
	"add-label",
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
	BulkSyncAll(context.Context, service.BulkSyncRequest) (*service.SyncResourcesResult, error)
	ResetLiveCache(context.Context, service.ResetLiveCacheRequest) (service.ResetLiveCacheResult, error)
	CacheStatus(context.Context, service.CacheStatusRequest) (service.CacheStatusResult, error)
	ExportSnapshot(context.Context, service.ExportSnapshotRequest) (service.ExportSnapshotResult, error)
	DiffSnapshot(context.Context, service.DiffSnapshotRequest) (service.DiffSnapshotResult, error)
	AddRepository(context.Context, service.AddRepositoryRequest) (service.RepositoryBinding, error)
	RepositoryStatus(context.Context, service.RepositoryStatusRequest) (service.RepositoryStatus, error)
	CreateIssue(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error)
	UpdateIssue(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error)
	CreatePage(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error)
	UpdatePage(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error)
	DeletePage(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error)
	AddComment(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error)
	AddLabel(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error)
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
	path           string
	sha            string
	title          string
	body           string
	state          string
	label          string
	labels         string
	idempotencyKey string
	dryRun         bool
	live           bool
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
	return executeWithFactoryAndDeps(args, stdout, stderr, defaultServiceFactory, localCommandDeps{Source: src})
}

func ExecuteWithClient(args []string, stdout io.Writer, stderr io.Writer, client gitcode.Client) int {
	return executeWithFactory(args, stdout, stderr, func(ctx context.Context, cachePath string) (queryService, func() error, error) {
		path, err := resolvedCachePath(cachePath)
		if err != nil {
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
		fmt.Fprintf(stdout, "gitcode-mcp %s\n", version)
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
		printCommandHelp(command, stdout)
		return 0
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
	return dispatch(context.Background(), svc, command, rest, opts, stdout, stderr, plan)
}

func buildStartupPlan(ctx context.Context, command string, opts options, deps localCommandDeps) (startupPlan, error) {
	plan := startupPlan{Command: command, ProviderMode: "offline-fixture", CachePath: opts.cachePath, RepoID: opts.repo}
	if opts.live && isLiveStartupCommand(command) {
		plan.ProviderMode = "live-http"
	}
	eff, err := config.LoadEffective(deps.Source, config.Overrides{CachePath: opts.cachePath})
	if err != nil {
		return plan, err
	}
	plan.CachePath = firstNonEmpty(opts.cachePath, eff.Config.CachePath)
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
	plan.ServiceConfig = service.ServiceConfig{BaseURL: binding.APIBaseURL, Timeout: eff.Config.DefaultTimeout, MaxResponseSize: eff.Config.MaxResponseSize, MaxRetries: eff.Config.MaxRetries}
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
	case "sync", "create-issue", "update-issue", "create-page", "update-page", "delete-page", "add-comment", "add-label", "doctor":
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
		if opts.wiki && !opts.issues {
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
	flags.StringVar(&opts.base, "base-id", "", "base snapshot id")
	flags.StringVar(&opts.head, "head", "", "head snapshot")
	flags.StringVar(&opts.head, "head-id", "", "head snapshot id")
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
	flags.StringVar(&opts.path, "path", "", "page path")
	flags.StringVar(&opts.sha, "sha", "", "page sha")
	flags.StringVar(&opts.title, "title", "", "title")
	flags.StringVar(&opts.body, "body", "", "body")
	flags.StringVar(&opts.state, "state", "", "state")
	flags.StringVar(&opts.label, "label", "", "label")
	flags.StringVar(&opts.labels, "labels", "", "comma-separated labels")
	flags.StringVar(&opts.idempotencyKey, "idempotency-key", "", "idempotency key")
	flags.BoolVar(&opts.dryRun, "dry-run", false, "validate write without mutation")
	flags.BoolVar(&opts.live, "live", false, "execute live write")
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
	return opts, flags.Args(), nil
}

func reorderFlags(args []string) []string {
	flags := make([]string, 0, len(args))
	positionals := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "--") {
			flags = append(flags, arg)
			if strings.Contains(arg, "=") || arg == "--strict" || arg == "--full" || arg == "--incremental" || arg == "--issues" || arg == "--wiki" || arg == "--index" || arg == "--dry-run" || arg == "--live" || arg == "--overwrite" || arg == "--redacted" || arg == "--runtime-audit" || arg == "--confirm" {
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
			default:
				printCommandHelp(command, stdout)
			}
			return 0
		}
		printCommandHelp(command, stdout)
		return 0
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
	if command == "doctor" {
		plan, _ := buildStartupPlan(context.Background(), command, opts, deps)
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
	case "sync":
		if opts.issues || opts.wiki || (opts.id == "" && opts.input == "") {
			req := service.BulkSyncRequest{RepoID: opts.repo, IdempotencyKey: opts.idempotencyKey, PerPage: 100}
			var result *service.SyncResourcesResult
			var syncErr error
			switch {
			case opts.issues && !opts.wiki:
				result, syncErr = svc.BulkSyncIssues(ctx, req)
			case opts.wiki && !opts.issues:
				result, syncErr = svc.BulkSyncWiki(ctx, req)
			default:
				result, syncErr = svc.BulkSyncAll(ctx, req)
			}
			return renderSyncResources(stdout, stderr, opts.format, result, syncErr, plan)
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
	case "create-page":
		return dispatchWrite(ctx, svc.CreatePage, command, opts, stdout, stderr, plan)
	case "update-page":
		return dispatchWrite(ctx, svc.UpdatePage, command, opts, stdout, stderr, plan)
	case "delete-page":
		return dispatchWrite(ctx, svc.DeletePage, command, opts, stdout, stderr, plan)
	case "add-comment":
		return dispatchWrite(ctx, svc.AddComment, command, opts, stdout, stderr, plan)
	case "add-label":
		return dispatchWrite(ctx, svc.AddLabel, command, opts, stdout, stderr, plan)
	default:
		return writeError(stderr, opts.format, service.ErrInvalidQuery{Field: "command", Message: command + " is not a query command"})
	}
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
	if opts.dryRun == opts.live {
		return service.ErrInvalidQuery{Field: "write_mode", Message: "exactly one of --dry-run or --live is required"}
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

func renderSyncResources(stdout, stderr io.Writer, format string, result *service.SyncResourcesResult, syncErr error, plan startupPlan) int {
	if syncErr != nil {
		if partial, ok := syncErr.(*service.PartialSyncError); ok {
			if result != nil && len(result.Results) > 0 {
				if format == "json" {
					_ = renderJSON(stdout, result)
				} else {
					for _, r := range result.Results {
						renderSyncText(stdout, r)
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
			return renderJSON(stdout, result)
		}
		for _, r := range result.Results {
			renderSyncText(stdout, r)
		}
	}
	return 0
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
	if opts.live {
		mode = service.WriteModeLive
	}
	return service.WriteCommandRequest{RepoID: opts.repo, Repo: opts.repo, Mode: mode, ID: opts.id, Number: opts.number, Slug: opts.slug, Path: opts.path, Sha: opts.sha, Title: opts.title, Body: opts.body, State: opts.state, Label: opts.label, Labels: labels, IdempotencyKey: opts.idempotencyKey}
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

func renderOperationText(w io.Writer, result service.OperationResult) {
	fmt.Fprintf(w, "%s: %s processed=%d evidence=%s\n", result.Command, result.Status, result.ProcessedCount, result.Evidence)
}

func renderSyncText(w io.Writer, result service.SyncResult) {
	fmt.Fprintf(w, "sync: %s fetched=%d updated=%d inserted=%d skipped=%d conflicts=%d idempotency_key=%s replayed=%t zero_delta=%t\n", result.Status, result.Counts.Fetched, result.Counts.Updated, result.Counts.Inserted, result.Counts.Skipped, result.Counts.Conflicts, result.IdempotencyKey, result.Replayed, result.ZeroDelta)
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
	}
	return code
}

func diagnosticContext(plan startupPlan, err error) diagnostics.CommandContext {
	ctx := diagnostics.CommandContext{ProviderMode: plan.ProviderMode, Command: plan.Command, SelectedAPIBaseURL: plan.APIBaseURL, RepositoryBindingID: plan.LiveRepositoryBinding.RepoID, CachePathPresent: strings.TrimSpace(plan.CachePath) != "", AuditPathPresent: strings.TrimSpace(plan.LiveRepositoryBinding.AuditPath) != ""}
	var writeErr service.ErrWriteFailure
	if errors.As(err, &writeErr) {
		ctx.HTTPAttempted = writeErr.Code == "write_unauthorized" || writeErr.Code == "write_network_unavailable" || writeErr.Code == "write_provider_error" || writeErr.Code == "write_conflict"
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
		ctx.HTTPAttempted = syncErr.Mode == "live_auth_failure" || syncErr.Mode == "network_timeout" || syncErr.Mode == "rate_limited" || syncErr.Mode == "partial_response" || syncErr.Mode == "live_graph_invalid" || syncErr.Mode == "payload_too_large" || syncErr.Mode == "remote_not_found" || syncErr.Mode == "conflict"
		ctx.UnsupportedPayload = syncErr.Mode == "live_graph_invalid"
		ctx.PayloadSource = syncErr.PayloadSource
		ctx.FailureSource = syncErr.PayloadSource
		ctx.LocalPayloadTooLarge = syncErr.Mode == "payload_too_large" && syncErr.PayloadSource == "local_body_limit"
		ctx.SchemaDecodeFailure = syncErr.Mode == "partial_response" || syncErr.Mode == "schema_decode"
		if syncErr.Mode == "partial_response" {
			ctx.FailureSource = "partial_response"
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
	report, err := doctor.Build(ctx, doctor.Request{Version: version, Source: deps.Source, CredentialReporter: deps.CredentialReporter, CredentialStatus: cred, CachePath: cachePath, Live: opts.live, ProviderMode: plan.ProviderMode, APIBaseURL: plan.APIBaseURL, RepoID: opts.repo, LiveBinding: plan.LiveRepositoryBinding})
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

func executeMigrateCacheCommand(ctx context.Context, opts options, stdout io.Writer, stderr io.Writer, deps localCommandDeps) int {
	cachePath := opts.cachePath
	if cachePath == "" {
		if deps.Source == nil {
			deps.Source = config.OSSource{}
		}
		cachePath = doctor.DefaultCachePath(deps.Source)
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
		fmt.Fprintf(w, "Usage: gitcode-mcp %s --repo REPO QUERY [--kind KIND] [--limit N] [--offset N]\n\n", command)
		fmt.Fprintln(w, "Search cached sources with full-text matching.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO       repository id")
		fmt.Fprintln(w, "  --kind KIND       filter by source kind (issue, wiki, doc, task)")
		fmt.Fprintln(w, "  --limit N         maximum results")
		fmt.Fprintln(w, "  --offset N        result offset")
		fmt.Fprintln(w, "  --cache-path PATH cache database path")
		fmt.Fprintln(w, "  --format FORMAT   output format (text, json)")
	case "list":
		fmt.Fprintf(w, "Usage: gitcode-mcp %s --repo REPO [--kind KIND] [--status STATUS] [--limit N] [--offset N]\n\n", command)
		fmt.Fprintln(w, "List cached sources with optional filters.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO       repository id")
		fmt.Fprintln(w, "  --kind KIND       filter by source kind")
		fmt.Fprintln(w, "  --status STATUS   filter by status")
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
		fmt.Fprintf(w, "Usage: gitcode-mcp %s [--live] --repo REPO [--issues] [--wiki] [--index] [--id ID] [--input REMOTE_ALIAS] [--idempotency-key KEY]\n\n", command)
		fmt.Fprintln(w, "Synchronize cached records with the configured provider.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --live              use live GitCode API provider for sync")
		fmt.Fprintln(w, "  --repo REPO         repository id")
		fmt.Fprintln(w, "  --issues            sync issue records")
		fmt.Fprintln(w, "  --wiki              sync wiki records")
		fmt.Fprintln(w, "  --index             build index after sync")
		fmt.Fprintln(w, "  --id ID             stable record id")
		fmt.Fprintln(w, "  --input ALIAS       remote alias for single-record sync")
		fmt.Fprintln(w, "  --idempotency-key KEY  idempotency key")
		fmt.Fprintln(w, "  --cache-path PATH   cache database path")
		fmt.Fprintln(w, "  --format FORMAT     output format (text, json)")
	case "cache":
		fmt.Fprintln(w, "Usage: gitcode-mcp cache reset --live --repo REPO")
	case "cache-status":
		fmt.Fprintf(w, "Usage: gitcode-mcp %s --repo REPO\n\n", command)
		fmt.Fprintln(w, "Report cache storage health and record counts.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO       repository id")
		fmt.Fprintln(w, "  --cache-path PATH cache database path")
		fmt.Fprintln(w, "  --format FORMAT   output format (text, json)")
	case "sync-status", "sync_status":
		fmt.Fprintf(w, "Usage: gitcode-mcp %s --repo REPO [ID] [--kind KIND] [--status STATUS] [--limit N] [--offset N]\n\n", command)
		fmt.Fprintln(w, "Report sync freshness for cached sources.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO       repository id")
		fmt.Fprintln(w, "  --kind KIND       filter by source kind")
		fmt.Fprintln(w, "  --status STATUS   filter by status")
		fmt.Fprintln(w, "  --limit N         maximum results")
		fmt.Fprintln(w, "  --offset N        result offset")
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
		fmt.Fprintf(w, "Usage: gitcode-mcp %s --repo REPO --title TITLE [--body BODY] [--state STATE] [--labels A,B] [--idempotency-key KEY] (--dry-run | --live)\n\n", command)
		fmt.Fprintln(w, "Create a new issue. Requires exactly one of --dry-run or --live.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO         repository id (required)")
		fmt.Fprintln(w, "  --title TITLE       issue title (required)")
		fmt.Fprintln(w, "  --body BODY         issue body")
		fmt.Fprintln(w, "  --state STATE       issue state")
		fmt.Fprintln(w, "  --labels A,B        comma-separated labels")
		fmt.Fprintln(w, "  --idempotency-key KEY  idempotency key")
		fmt.Fprintln(w, "  --dry-run           validate without mutation")
		fmt.Fprintln(w, "  --live              execute live write")
		fmt.Fprintln(w, "  --cache-path PATH   cache database path")
		fmt.Fprintln(w, "  --format FORMAT     output format (text, json)")
	case "update-issue":
		fmt.Fprintf(w, "Usage: gitcode-mcp %s --repo REPO --number N [--title TITLE] [--body BODY] [--state STATE] [--labels A,B] [--idempotency-key KEY] (--dry-run | --live)\n\n", command)
		fmt.Fprintln(w, "Update an existing issue. Requires exactly one of --dry-run or --live.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO         repository id (required)")
		fmt.Fprintln(w, "  --number N          issue number (required)")
		fmt.Fprintln(w, "  --title TITLE       updated title")
		fmt.Fprintln(w, "  --body BODY         updated body")
		fmt.Fprintln(w, "  --state STATE       updated state")
		fmt.Fprintln(w, "  --labels A,B        comma-separated labels")
		fmt.Fprintln(w, "  --idempotency-key KEY  idempotency key")
		fmt.Fprintln(w, "  --dry-run           validate without mutation")
		fmt.Fprintln(w, "  --live              execute live write")
		fmt.Fprintln(w, "  --cache-path PATH   cache database path")
		fmt.Fprintln(w, "  --format FORMAT     output format (text, json)")
	case "create-page":
		fmt.Fprintf(w, "Usage: gitcode-mcp %s --repo REPO --title TITLE --body BODY [--slug SLUG] [--idempotency-key KEY] (--dry-run | --live)\n\n", command)
		fmt.Fprintln(w, "Create a new wiki page. Requires exactly one of --dry-run or --live.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO         repository id (required)")
		fmt.Fprintln(w, "  --title TITLE       page title (required)")
		fmt.Fprintln(w, "  --body BODY         page body (required)")
		fmt.Fprintln(w, "  --slug SLUG         page slug")
		fmt.Fprintln(w, "  --idempotency-key KEY  idempotency key")
		fmt.Fprintln(w, "  --dry-run           validate without mutation")
		fmt.Fprintln(w, "  --live              execute live write")
		fmt.Fprintln(w, "  --cache-path PATH   cache database path")
		fmt.Fprintln(w, "  --format FORMAT     output format (text, json)")
	case "update-page":
		fmt.Fprintf(w, "Usage: gitcode-mcp %s --repo REPO --slug SLUG [--title TITLE] [--body BODY] [--idempotency-key KEY] (--dry-run | --live)\n\n", command)
		fmt.Fprintln(w, "Update an existing wiki page. Requires exactly one of --dry-run or --live.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO         repository id (required)")
		fmt.Fprintln(w, "  --slug SLUG         page slug (required)")
		fmt.Fprintln(w, "  --title TITLE       updated title")
		fmt.Fprintln(w, "  --body BODY         updated body")
		fmt.Fprintln(w, "  --idempotency-key KEY  idempotency key")
		fmt.Fprintln(w, "  --dry-run           validate without mutation")
		fmt.Fprintln(w, "  --live              execute live write")
		fmt.Fprintln(w, "  --cache-path PATH   cache database path")
		fmt.Fprintln(w, "  --format FORMAT     output format (text, json)")
	case "add-comment":
		fmt.Fprintf(w, "Usage: gitcode-mcp %s --repo REPO --number N --body BODY [--idempotency-key KEY] (--dry-run | --live)\n\n", command)
		fmt.Fprintln(w, "Add a comment to an issue. Requires exactly one of --dry-run or --live.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO         repository id (required)")
		fmt.Fprintln(w, "  --number N          issue number (required)")
		fmt.Fprintln(w, "  --body BODY         comment body (required)")
		fmt.Fprintln(w, "  --idempotency-key KEY  idempotency key")
		fmt.Fprintln(w, "  --dry-run           validate without mutation")
		fmt.Fprintln(w, "  --live              execute live write")
		fmt.Fprintln(w, "  --cache-path PATH   cache database path")
		fmt.Fprintln(w, "  --format FORMAT     output format (text, json)")
	case "add-label":
		fmt.Fprintf(w, "Usage: gitcode-mcp %s --repo REPO --number N --label LABEL [--idempotency-key KEY] (--dry-run | --live)\n\n", command)
		fmt.Fprintln(w, "Add a label to an issue. Requires exactly one of --dry-run or --live.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO         repository id (required)")
		fmt.Fprintln(w, "  --number N          issue number (required)")
		fmt.Fprintln(w, "  --label LABEL       label to add (required)")
		fmt.Fprintln(w, "  --idempotency-key KEY  idempotency key")
		fmt.Fprintln(w, "  --dry-run           validate without mutation")
		fmt.Fprintln(w, "  --live              execute live write")
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
		fmt.Fprintf(w, "Usage: gitcode-mcp %s [--repo REPO] [--live] [--runtime-audit] [--cache-path PATH]\n\n", command)
		fmt.Fprintln(w, "Aggregate subsystem diagnostics with public-safe output.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO         repository id")
		fmt.Fprintln(w, "  --live              include live provider checks")
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
		fmt.Fprintln(w, "Credential sources are checked in order: env GITCODE_TOKEN, keychain, none.")
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
		fmt.Fprintln(w, "  --scopes SCOPES     comma-separated scopes (issues, wiki)")
		fmt.Fprintln(w, "  --alias ALIAS       repository alias (repeatable)")
		fmt.Fprintln(w, "  --display-name NAME human-readable display name")
		fmt.Fprintln(w, "  --cache-path PATH   cache database path")
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
