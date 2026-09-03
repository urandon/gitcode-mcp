package cli

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mattn/go-isatty"

	"gitcode-mcp/internal/adminhttp"
	"gitcode-mcp/internal/buildinfo"
	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/credential"
	"gitcode-mcp/internal/diagnostics"
	"gitcode-mcp/internal/doctor"
	"gitcode-mcp/internal/feedback"
	"gitcode-mcp/internal/gitcode"
	"gitcode-mcp/internal/index"
	"gitcode-mcp/internal/rag"
	"gitcode-mcp/internal/repositorydocs"
	"gitcode-mcp/internal/service"
	"gitcode-mcp/internal/servicectl"
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
	"update-pr",
	"merge-pr", "merge-mr",
	"milestones",
	"list-push-mirrors", "push-mirrors",
	"trigger-push-mirror",
	"wait-push-mirror",
	"create-milestone",
	"update-milestone",
	"set-issue-milestone",
	"clear-issue-milestone",
	"create-page",
	"update-page",
	"delete-page",
	"add-comment",
	"add-pr-review-comment",
	"reply-pr-review-comment",
	"update-comment",
	"add-label",
	"publish-release",
	"feedback",
	"config",
	"auth",
	"service",
	"admin",
	"maintenance",
	"rag",
	"rag-status",
	"rag-search",
	"repo-docs",
	"doctor",
	"migrate-cache",
	"repo",
	"bind",
}

const maxMarkdownBodyBytes int64 = 10 << 20

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
	BulkSyncIssueComments(context.Context, service.BulkSyncRequest) (*service.SyncResourcesResult, error)
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
	UpdatePR(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error)
	MergePR(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error)
	ListMilestones(context.Context, service.MilestoneListRequest) (service.MilestoneListResult, error)
	ListPushRemoteMirrors(context.Context, service.PushMirrorListRequest) (service.PushMirrorListResult, error)
	TriggerPushRemoteMirror(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error)
	WaitPushRemoteMirror(context.Context, service.PushMirrorWaitRequest) (service.PushMirrorWaitResult, error)
	CreateMilestone(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error)
	UpdateMilestone(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error)
	SetIssueMilestone(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error)
	ClearIssueMilestone(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error)
	CreatePage(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error)
	UpdatePage(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error)
	DeletePage(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error)
	AddComment(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error)
	AddPRReviewComment(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error)
	ReplyPRReviewComment(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error)
	UpdateComment(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error)
	AddLabel(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error)
	PublishRelease(context.Context, service.PublishReleaseRequest) (service.PublishReleaseResult, error)
	PrepareFeedback(context.Context, feedback.Draft) (feedback.PreparedReport, error)
	SubmitFeedback(context.Context, service.SubmitFeedbackRequest) (feedback.SubmissionResult, error)
}

type serviceFactory func(context.Context, string) (queryService, func() error, error)

type localCommandDeps struct {
	Source             config.Source
	CredentialReporter config.CredentialStatusReporter
	RAGRuntime         rag.Runtime
	OpenURL            func(string) error
	Stdin              io.Reader
	IsTerminal         func() bool
	MigrationService   cacheMigrationService
}

type cacheMigrationService interface {
	Status() (servicectl.Status, error)
	QuiesceForCacheMigration(context.Context) (servicectl.Status, error)
	Install(bool) (servicectl.Status, error)
	Start(context.Context) (servicectl.Status, error)
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
	RAGConfig             config.Config
}

type options struct {
	format                       string
	kind                         string
	status                       string
	provenance                   string
	searchMode                   string
	revision                     string
	includeWorktree              bool
	registrationID               string
	sourceRegistrationID         string
	sourceRegistrationGeneration int64
	repositoryPath               string
	limit                        int
	offset                       int
	lineStart                    int
	lineEnd                      int
	cachePath                    string
	strict                       bool
	base                         string
	head                         string
	full                         bool
	incremental                  bool
	issues                       bool
	wiki                         bool
	pulls                        bool
	comments                     bool
	issueComments                bool
	prComments                   bool
	syncIndex                    bool
	maxPages                     int
	maxRecords                   int
	perPage                      int
	progress                     string
	quiet                        bool
	details                      bool
	input                        string
	output                       string
	owner                        string
	repo                         string
	name                         string
	id                           string
	issueID                      string
	mirrorID                     string
	after                        string
	timeoutSeconds               int
	number                       int
	commentID                    string
	discussionID                 string
	parentID                     string
	slug                         string
	path                         string
	line                         int
	startLine                    int
	endLine                      int
	position                     int
	sha                          string
	strategy                     string
	title                        string
	body                         string
	bodyFile                     string
	bodySet                      bool
	bodyFileSet                  bool
	allowLiteralBackslashN       bool
	bodyInput                    *service.WriteBodyInputMetadata
	description                  string
	dueOn                        string
	milestone                    string
	clearMilestone               bool
	state                        string
	label                        string
	labels                       string
	tag                          string
	ref                          string
	profile                      string
	asset                        multiFlag
	idempotencyKey               string
	dryRun                       bool
	live                         bool
	offline                      bool
	fixture                      bool
	overwrite                    bool
	redacted                     bool
	runtimeAudit                 bool
	unresolvedOnly               bool
	apiBaseURL                   string
	scopes                       string
	alias                        multiFlag
	displayName                  string
	policy                       string
	chunkID                      string
	sourceID                     string
	recordID                     string
	snapshotID                   string
	confirm                      bool
	yes                          bool
	helpRequested                bool
	steps                        int
	intervalMS                   int
	batchSize                    int
	topK                         int
	category                     string
	surface                      string
	reporterType                 string
	observed                     string
	expected                     string
	impact                       string
	fallbackUsed                 string
	workaround                   string
	relatedTask                  string
	acceptanceSignal             string
	proposal                     string
	toolName                     string
	errorCode                    string
	failureClass                 string
	correlationID                string
	jobID                        string
	duplicateOverride            string
	reproductionSteps            multiFlag
	evidence                     multiFlag
	detach                       bool
	daemon                       bool
	syncMode                     string
	ragMode                      string
	collections                  string
	noServiceInstall             bool
	noModelDownload              bool
	noBrowser                    bool
	admin                        bool
	adminBind                    string
	adminUnsafe                  bool
}

type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }
func (m *multiFlag) Set(value string) error {
	*m = append(*m, value)
	return nil
}

// Execute runs the gitcode-mcp CLI.
func Execute(args []string, stdout io.Writer, stderr io.Writer) int {
	return executeWithFactory(args, stdout, stderr, nil)
}

func ExecuteWithSource(args []string, stdout io.Writer, stderr io.Writer, src config.Source) int {
	return ExecuteWithSourceContext(context.Background(), args, stdout, stderr, src)
}

func ExecuteWithSourceContext(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer, src config.Source) int {
	return executeWithFactoryAndDepsContext(ctx, args, stdout, stderr, nil, localCommandDeps{Source: src})
}

func ExecuteWithSourceContextAndInput(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer, src config.Source) int {
	return executeWithFactoryAndDepsContext(ctx, args, stdout, stderr, nil, localCommandDeps{Source: src, Stdin: stdin})
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
		fmt.Fprintf(stdout, "gitcode-mcp %s\n", buildinfo.Current().Version)
		return 0
	}
	if args[0] == "config" || args[0] == "auth" || args[0] == "service" || args[0] == "admin" || args[0] == "maintenance" || args[0] == "rag" || args[0] == "rag-status" || args[0] == "rag-search" || args[0] == "repo-docs" || args[0] == "doctor" || args[0] == "migrate-cache" {
		return executeLocalCommand(ctx, args, stdout, stderr, deps)
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
	opts, err = resolveMarkdownBodyInput(command, opts, deps.Stdin)
	if err != nil {
		return writeError(stderr, opts.format, err)
	}
	if command == "bind" {
		owner := strings.TrimSpace(opts.owner)
		name := strings.TrimSpace(opts.repo)
		if owner == "" {
			return writeError(stderr, opts.format, service.ErrInvalidQuery{Field: "repo-owner", Message: "repository owner is required"})
		}
		if name == "" {
			return writeError(stderr, opts.format, service.ErrInvalidQuery{Field: "repo", Message: "repository name is required"})
		}
		if len(rest) != 0 {
			return writeError(stderr, opts.format, service.ErrInvalidQuery{Field: "bind", Message: "unexpected positional arguments"})
		}
		opts.owner = owner
		opts.name = name
		opts.repo = owner + "/" + name
		if strings.TrimSpace(opts.scopes) == "" {
			opts.scopes = "issues,wiki,pulls,comments"
		}
		command = "repo"
		rest = []string{"add"}
	}
	if command == "repo" {
		if sub, ok := firstArg(rest); ok && sub == "init-local" {
			return executeRepoInitLocalCommand(ctx, opts, stdout, stderr, deps)
		}
	}
	planCommand := command
	if command == "feedback" {
		sub, _ := firstArg(rest)
		if sub == "submit" && opts.live && !opts.dryRun {
			planCommand = "submit-feedback"
		} else {
			planCommand = "prepare-feedback"
		}
	}
	plan, planErr := buildStartupPlan(context.Background(), planCommand, opts, deps)
	if planErr != nil {
		return writeCommandError(stderr, opts.format, plan, planErr)
	}
	svc, cleanup, err := serviceFromStartupPlan(context.Background(), plan, factory)
	if err != nil && command == "repo" && firstPositional(rest) == "status" {
		svc, cleanup, err = repoStatusReadOnlyFallback(context.Background(), plan, err)
	}
	if err != nil {
		return writeCommandError(stderr, opts.format, plan, err)
	}
	if cleanup != nil {
		defer cleanup()
	}
	return dispatch(ctx, svc, command, rest, opts, stdout, stderr, plan, deps)
}

func firstPositional(args []string) string {
	value, _ := firstArg(args)
	return value
}

func repoStatusReadOnlyFallback(ctx context.Context, plan startupPlan, openErr error) (queryService, func() error, error) {
	var schemaErr *cache.SchemaVersionError
	if !errors.As(openErr, &schemaErr) || !schemaErr.Compat.Compatible {
		return nil, nil, openErr
	}
	path, err := resolvedCachePath(plan.CachePath)
	if err != nil {
		return nil, nil, err
	}
	store, err := cache.NewSQLiteReadOnlyStore(ctx, path)
	if err != nil {
		return nil, nil, err
	}
	return service.New(store), store.Close, nil
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
	plan.RAGConfig = eff.Config
	plan.MCPToolAccess = eff.Config.MCPToolAccess
	plan.ServiceConfig = service.ServiceConfig{LockPath: eff.Config.LockPath, Feedback: eff.Config.Feedback}
	if command == "submit-feedback" {
		plan.RepoID = eff.Config.Feedback.RepoID
	}
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
	binding, err := resolveStartupLiveRepositoryBinding(ctx, plan.CachePath, plan.RepoID, liveRequestedScope(command, opts), eff.Config.GitCodeBaseURL)
	if err != nil {
		return plan, err
	}
	plan.LiveRepositoryBinding = binding
	plan.APIBaseURL = binding.APIBaseURL
	plan.ServiceConfig = service.ServiceConfig{BaseURL: binding.APIBaseURL, LockPath: eff.Config.LockPath, Timeout: eff.Config.DefaultTimeout, MaxResponseSize: eff.Config.MaxResponseSize, MaxRetries: eff.Config.MaxRetries, RateLimitRPS: eff.Config.RateLimitRPS, RateLimitBurst: eff.Config.RateLimitBurst, Feedback: eff.Config.Feedback}
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
	case "sync", "submit-feedback", "create-issue", "update-issue", "create-pr", "create-mr", "update-pr", "merge-pr", "merge-mr", "milestones", "list-push-mirrors", "push-mirrors", "trigger-push-mirror", "wait-push-mirror", "create-milestone", "update-milestone", "set-issue-milestone", "clear-issue-milestone", "create-page", "update-page", "delete-page", "add-comment", "add-pr-review-comment", "reply-pr-review-comment", "update-comment", "add-label", "publish-release", "doctor":
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
		if opts.wiki && !opts.issues && !opts.pulls && !opts.comments && !opts.issueComments && !opts.prComments {
			return service.RepositoryScopeWiki
		}
	}
	return service.RepositoryScopeIssues
}

func serviceFromStartupPlan(ctx context.Context, plan startupPlan, factory serviceFactory) (queryService, func() error, error) {
	if plan.ProviderMode != "live-http" {
		configureRAG := factory == nil
		if factory == nil {
			factory = defaultServiceFactory
		}
		svc, cleanup, err := factory(ctx, plan.CachePath)
		if err == nil {
			if configurable, ok := svc.(interface{ ConfigureFeedback(feedback.Config) }); ok {
				configurable.ConfigureFeedback(plan.ServiceConfig.Feedback)
			}
			if configureRAG {
				if configurable, ok := svc.(interface{ ConfigureRAGSearch(config.Config) }); ok {
					configurable.ConfigureRAGSearch(plan.RAGConfig)
				}
			}
		}
		return svc, cleanup, err
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
	svc.ConfigureRAGSearch(plan.RAGConfig)
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
	svc, err := service.NewWithMode(store, gitcode.ProviderModeFixture, "", service.ServiceConfig{LockPath: path + ".lock"})
	if err != nil {
		_ = store.Close()
		return nil, nil, err
	}
	return svc, store.Close, nil
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
	flags.StringVar(&opts.searchMode, "mode", "", "search mode: hybrid or full_text")
	flags.StringVar(&opts.revision, "revision", "", "local Git revision (defaults to HEAD)")
	flags.BoolVar(&opts.includeWorktree, "include-worktree", false, "explicitly include tracked worktree changes")
	flags.StringVar(&opts.registrationID, "registration-id", "", "opaque repository documentation registration id")
	flags.StringVar(&opts.sourceRegistrationID, "source-registration-id", "", "opaque repository documentation source id")
	flags.Int64Var(&opts.sourceRegistrationGeneration, "source-registration-generation", 0, "repository documentation source generation")
	flags.StringVar(&opts.repositoryPath, "repository-path", "", "local Git repository path for explicit source registration")
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
	flags.BoolVar(&opts.issueComments, "issue-comments", false, "sync the durable issue comment queue with aggregate-first collection")
	flags.BoolVar(&opts.prComments, "pr-comments", false, "sync pull request comments")
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
	flags.StringVar(&opts.owner, "repo-owner", "", "repository owner (bind compatibility alias)")
	flags.StringVar(&opts.repo, "repo", "", "configured repository id")
	flags.StringVar(&opts.name, "name", "", "repository name")
	flags.StringVar(&opts.id, "id", "", "record id")
	flags.StringVar(&opts.issueID, "issue-id", "", "stable source id or known issue alias")
	flags.StringVar(&opts.mirrorID, "mirror-id", "", "push mirror id")
	flags.StringVar(&opts.after, "after", "", "RFC3339 freshness barrier")
	flags.IntVar(&opts.timeoutSeconds, "timeout-seconds", 0, "wait timeout in seconds")
	flags.IntVar(&opts.number, "number", 0, "issue number")
	flags.StringVar(&opts.commentID, "comment-id", "", "comment id")
	flags.StringVar(&opts.discussionID, "discussion-id", "", "review discussion id")
	flags.StringVar(&opts.parentID, "parent-comment-id", "", "parent review comment id")
	flags.StringVar(&opts.slug, "slug", "", "page slug")
	flags.StringVar(&opts.path, "path", "", "page path")
	flags.IntVar(&opts.line, "line", 0, "line number")
	flags.IntVar(&opts.startLine, "start-line", 0, "start line")
	flags.IntVar(&opts.endLine, "end-line", 0, "end line")
	flags.IntVar(&opts.position, "position", 0, "diff position")
	flags.StringVar(&opts.sha, "sha", "", "expected revision or head sha")
	flags.StringVar(&opts.strategy, "strategy", "", "merge strategy: merge, squash, or rebase")
	flags.StringVar(&opts.title, "title", "", "title")
	flags.StringVar(&opts.body, "body", "", "body")
	flags.StringVar(&opts.bodyFile, "body-file", "", "UTF-8 Markdown body file, or - for stdin")
	flags.BoolVar(&opts.allowLiteralBackslashN, "allow-literal-backslash-n", false, "allow suspicious inline bodies containing literal backslash-n sequences")
	flags.StringVar(&opts.description, "description", "", "description")
	flags.StringVar(&opts.dueOn, "due-on", "", "due date YYYY-MM-DD")
	flags.StringVar(&opts.milestone, "milestone", "", "milestone id or title")
	flags.BoolVar(&opts.clearMilestone, "clear-milestone", false, "clear issue milestone")
	flags.StringVar(&opts.state, "state", "", "state")
	flags.StringVar(&opts.label, "label", "", "label")
	flags.StringVar(&opts.labels, "labels", "", "comma-separated labels")
	flags.StringVar(&opts.tag, "tag", "", "release tag")
	flags.StringVar(&opts.ref, "ref", "", "release ref")
	flags.StringVar(&opts.profile, "profile", "", "RAG profile")
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
	flags.BoolVar(&opts.yes, "yes", false, "answer yes to setup prompts")
	flags.IntVar(&opts.steps, "steps", 0, "fake job step count")
	flags.IntVar(&opts.intervalMS, "interval-ms", 0, "fake job interval in milliseconds")
	flags.IntVar(&opts.batchSize, "batch-size", 0, "RAG embedding batch size")
	flags.IntVar(&opts.topK, "top-k", 0, "RAG semantic candidate count")
	flags.StringVar(&opts.category, "category", "", "feedback category")
	flags.StringVar(&opts.surface, "surface", "", "feedback surface")
	flags.StringVar(&opts.reporterType, "reporter-type", "", "feedback reporter type")
	flags.StringVar(&opts.observed, "observed", "", "observed behavior")
	flags.StringVar(&opts.expected, "expected", "", "expected behavior")
	flags.StringVar(&opts.impact, "impact", "", "feedback impact")
	flags.StringVar(&opts.fallbackUsed, "fallback-used", "", "fallback used")
	flags.StringVar(&opts.workaround, "workaround", "", "workaround")
	flags.StringVar(&opts.relatedTask, "related-task", "", "related task reference")
	flags.StringVar(&opts.acceptanceSignal, "acceptance-signal", "", "feedback acceptance signal")
	flags.StringVar(&opts.proposal, "proposal", "", "optional proposal")
	flags.StringVar(&opts.toolName, "tool-name", "", "tool or command name")
	flags.StringVar(&opts.errorCode, "error-code", "", "structured error code")
	flags.StringVar(&opts.failureClass, "failure-class", "", "structured failure class")
	flags.StringVar(&opts.correlationID, "correlation-id", "", "sanitized correlation id")
	flags.StringVar(&opts.jobID, "job-id", "", "sanitized job id")
	flags.StringVar(&opts.duplicateOverride, "duplicate-override", "", "explicit duplicate action")
	flags.Var(&opts.reproductionSteps, "step", "feedback reproduction step; repeatable")
	flags.Var(&opts.evidence, "evidence", "sanitized evidence fact; repeatable")
	flags.BoolVar(&opts.detach, "detach", false, "start job without attaching progress")
	flags.BoolVar(&opts.daemon, "daemon", false, "start sync as a daemon job")
	flags.StringVar(&opts.syncMode, "sync", "", "maintenance sync policy: off, head, or head-and-backfill")
	flags.StringVar(&opts.ragMode, "rag", "", "maintenance RAG policy: off or maintain")
	flags.StringVar(&opts.collections, "collections", "", "comma-separated maintained collections")
	flags.BoolVar(&opts.noServiceInstall, "no-service-install", false, "do not install the user service")
	flags.BoolVar(&opts.noModelDownload, "no-model-download", false, "do not download an embedding model")
	flags.BoolVar(&opts.noBrowser, "no-browser", false, "print URL without opening a browser")
	flags.BoolVar(&opts.admin, "admin", false, "start the embedded admin listener")
	flags.StringVar(&opts.adminBind, "admin-bind", "", "admin listener bind address")
	flags.BoolVar(&opts.adminUnsafe, "admin-unsafe-allow-non-loopback", false, "allow an unsafe non-loopback admin listener")
	if err := flags.Parse(reorderFlags(args)); err != nil {
		return opts, nil, service.ErrInvalidQuery{Field: "flags", Message: err.Error()}
	}
	flags.Visit(func(current *flag.Flag) {
		switch current.Name {
		case "body":
			opts.bodySet = true
		case "body-file":
			opts.bodyFileSet = true
		}
	})
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
			if strings.Contains(arg, "=") || arg == "--strict" || arg == "--full" || arg == "--incremental" || arg == "--issues" || arg == "--wiki" || arg == "--pulls" || arg == "--comments" || arg == "--issue-comments" || arg == "--pr-comments" || arg == "--index" || arg == "--quiet" || arg == "--dry-run" || arg == "--live" || arg == "--offline" || arg == "--fixture" || arg == "--overwrite" || arg == "--redacted" || arg == "--runtime-audit" || arg == "--confirm" || arg == "--yes" || arg == "--detach" || arg == "--daemon" || arg == "--no-service-install" || arg == "--no-model-download" || arg == "--no-browser" || arg == "--admin" || arg == "--admin-unsafe-allow-non-loopback" || arg == "--allow-literal-backslash-n" {
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

func executeLocalCommand(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer, deps localCommandDeps) int {
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
		if sub != "" && (command == "config" || command == "auth" || command == "repo" || command == "service" || command == "admin" || command == "maintenance" || command == "rag" || command == "repo-docs") {
			switch command + " " + sub {
			case "config init", "config locate", "config show":
				printLocalSubcommandHelp(command, sub, stdout)
			case "auth status":
				printLocalSubcommandHelp(command, sub, stdout)
			case "repo add", "repo status":
				printLocalSubcommandHelp(command, sub, stdout)
			case "repo init-local":
				printLocalSubcommandHelp(command, sub, stdout)
			case "service run", "service install", "service repair", "service uninstall", "service start", "service stop", "service status", "service doctor", "service maintenance", "service reconcile", "service fake-job", "service jobs", "service job", "service attach", "service cancel":
				printLocalSubcommandHelp(command, sub, stdout)
			case "admin open", "admin status":
				printLocalSubcommandHelp(command, sub, stdout)
			case "maintenance plan", "maintenance enable":
				printLocalSubcommandHelp(command, sub, stdout)
			case "rag setup", "rag enable", "rag index", "rag status", "rag search":
				printLocalSubcommandHelp(command, sub, stdout)
			case "repo-docs register", "repo-docs rebind", "repo-docs policy", "repo-docs status", "repo-docs plan", "repo-docs index", "repo-docs search":
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
		report := config.BuildRuntimeAuditConfigReport(deps.Source, config.Overrides{}, deps.CredentialReporter, buildinfo.Current().Version)
		payload := runtimeAuditPayload{RepoID: opts.repo, Config: report}
		if opts.format == "json" {
			return renderJSON(stdout, payload)
		}
		renderRuntimeAuditText(stdout, payload)
		return 0
	}
	if command == "doctor" {
		plan, planErr := buildStartupPlan(ctx, command, opts, deps)
		var invalid service.ErrInvalidQuery
		if errors.As(planErr, &invalid) {
			return writeError(stderr, opts.format, planErr)
		}
		return executeDoctorCommand(ctx, opts, plan, stdout, stderr, deps)
	}
	if command == "migrate-cache" {
		return executeMigrateCacheCommand(ctx, opts, stdout, stderr, deps)
	}
	if command == "service" {
		return executeServiceCommand(ctx, rest, opts, stdout, stderr, deps)
	}
	if command == "admin" {
		return executeAdminCommand(ctx, rest, opts, stdout, stderr, deps)
	}
	if command == "maintenance" {
		return executeMaintenanceCommand(ctx, rest, opts, stdout, stderr, deps)
	}
	if command == "rag" {
		return executeRAGCommand(ctx, rest, opts, stdout, stderr, deps)
	}
	if command == "rag-status" {
		return executeRAGStatusCommand(ctx, opts, stdout, stderr, deps)
	}
	if command == "rag-search" {
		return executeRAGSearchCommand(ctx, rest, opts, stdout, stderr, deps)
	}
	if command == "repo-docs" {
		return executeRepositoryDocsCommand(ctx, rest, opts, stdout, stderr, deps)
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
		status := deps.CredentialReporter.Status(ctx, eff)
		if opts.live {
			resolution, _ := resolveLiveCredential(ctx, eff, deps)
			status = resolution.Status()
			status = probeAuthStatus(ctx, deps.Source, eff, opts, status, resolution.Token)
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
	provider, err := gitcode.NewLiveProvider(gitcode.ProviderConfig{Mode: gitcode.ProviderModeLive, LiveAllowed: true, BaseURL: eff.Config.GitCodeBaseURL, Token: token, Timeout: eff.Config.DefaultTimeout, MaxResponseSize: eff.Config.MaxResponseSize, MaxRetries: eff.Config.MaxRetries, RateLimitRPS: eff.Config.RateLimitRPS, RateLimitBurst: eff.Config.RateLimitBurst})
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

func executeMaintenanceCommand(ctx context.Context, args []string, opts options, stdout io.Writer, stderr io.Writer, deps localCommandDeps) int {
	sub, ok := firstArg(args)
	if !ok {
		return writeError(stderr, opts.format, service.ErrInvalidQuery{Field: "maintenance", Message: "subcommand is required"})
	}
	if sub != "plan" && sub != "enable" {
		return writeError(stderr, opts.format, service.ErrInvalidQuery{Field: "maintenance", Message: "unknown subcommand"})
	}
	if strings.TrimSpace(opts.repo) == "" {
		return writeError(stderr, opts.format, service.ErrInvalidQuery{Field: "repo", Message: "repository id is required"})
	}
	interactive := maintenanceInputIsTerminal(deps)
	if sub == "enable" && !opts.dryRun {
		if !interactive && !opts.yes {
			return writeError(stderr, opts.format, service.ErrInvalidQuery{Field: "confirmation", Message: "non-interactive enable requires --yes and --idempotency-key KEY"})
		}
		if !interactive && strings.TrimSpace(opts.idempotencyKey) == "" {
			return writeError(stderr, opts.format, service.ErrInvalidQuery{Field: "idempotency_key", Message: "non-interactive enable requires --idempotency-key KEY; reuse the same key when retrying"})
		}
		if interactive && !opts.yes && opts.format != "text" {
			return writeError(stderr, opts.format, service.ErrInvalidQuery{Field: "format", Message: "interactive confirmation requires --format text; use --yes for structured output"})
		}
	}
	eff, err := config.LoadEffective(deps.Source, config.Overrides{CachePath: opts.cachePath})
	if err != nil {
		fmt.Fprintln(stderr, config.RedactDiagnostic(err.Error(), deps.Source))
		return 1
	}
	setup := servicectl.MaintenanceSetup{
		Manager:         servicectl.Manager{Source: deps.Source, BinaryPath: os.Args[0], Version: buildinfo.Current().Version, Commit: buildinfo.Current().Commit, RuntimeDir: eff.Config.Service.RuntimeDir},
		Config:          eff.Config,
		CachePath:       eff.Config.CachePath,
		CachePathSource: eff.CachePathSource,
		RAGRuntime:      deps.RAGRuntime,
		ConfigReference: maintenanceConfigReference(eff),
	}
	collections := []string{}
	if strings.TrimSpace(opts.collections) != "" {
		collections = strings.Split(opts.collections, ",")
	}
	req := servicectl.MaintenanceSetupRequest{
		RepoID: opts.repo, Profile: opts.profile, SyncMode: opts.syncMode, Collections: collections,
		RAGMode: opts.ragMode, NoServiceInstall: opts.noServiceInstall, NoModelDownload: opts.noModelDownload,
		Detach: opts.detach, IdempotencyKey: opts.idempotencyKey,
	}
	plan, err := setup.Plan(ctx, req)
	if err != nil {
		return writeError(stderr, opts.format, err)
	}
	if sub == "plan" || opts.dryRun {
		return render(stdout, opts.format, plan, renderMaintenancePlanText)
	}
	if !opts.yes {
		if code := render(stdout, opts.format, plan, renderMaintenancePlanText); code != 0 {
			return code
		}
		confirmed, promptErr := confirmMaintenancePlan(deps, stderr)
		if promptErr != nil {
			return writeError(stderr, opts.format, service.ErrInvalidQuery{Field: "confirmation", Message: promptErr.Error()})
		}
		if !confirmed {
			return writeError(stderr, opts.format, service.ErrInvalidQuery{Field: "confirmation", Message: "plan was not confirmed; no changes were applied"})
		}
	}
	if strings.TrimSpace(req.IdempotencyKey) == "" {
		req.IdempotencyKey = generatedMaintenanceIdempotencyKey(plan)
	}
	req.PlanID = plan.PlanID
	req.Confirmed = true
	req.AllowMachineChange = true
	result, err := setup.Apply(ctx, req)
	if err != nil {
		return writeError(stderr, opts.format, err)
	}
	code := render(stdout, opts.format, result, renderMaintenanceApplyText)
	if result.Status == "blocked" {
		return 1
	}
	return code
}

func maintenanceInputIsTerminal(deps localCommandDeps) bool {
	if deps.IsTerminal != nil {
		return deps.IsTerminal()
	}
	input := deps.Stdin
	if input == nil {
		input = os.Stdin
	}
	file, ok := input.(*os.File)
	if !ok {
		return false
	}
	fd := file.Fd()
	return isatty.IsTerminal(fd) || isatty.IsCygwinTerminal(fd)
}

func confirmMaintenancePlan(deps localCommandDeps, prompt io.Writer) (bool, error) {
	input := deps.Stdin
	if input == nil {
		input = os.Stdin
	}
	fmt.Fprint(prompt, "Apply this maintenance plan? [y/N] ")
	answer, err := bufio.NewReader(input).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, errors.New("could not read confirmation")
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes", nil
}

func generatedMaintenanceIdempotencyKey(plan servicectl.MaintenancePlan) string {
	intent := struct {
		SchemaVersion     string                       `json:"schema_version"`
		RepoID            string                       `json:"repo_id"`
		CacheUUID         string                       `json:"cache_uuid"`
		RepositoryBinding string                       `json:"repository_binding_hash"`
		ConfigurationHash string                       `json:"configuration_hash"`
		Policy            servicectl.MaintenancePolicy `json:"policy"`
	}{
		SchemaVersion:     plan.SchemaVersion,
		RepoID:            plan.RepoID,
		CacheUUID:         plan.Cache.UUID,
		RepositoryBinding: plan.Cache.RepositoryBinding,
		ConfigurationHash: plan.ConfigurationHash,
		Policy:            plan.Policy,
	}
	value, _ := json.Marshal(intent)
	digest := sha256.Sum256(value)
	return "cli-maintenance-" + hex.EncodeToString(digest[:16])
}

func maintenanceConfigReference(eff config.EffectiveConfig) string {
	if strings.TrimSpace(eff.RepoLocalConfigPath) != "" {
		return eff.RepoLocalConfigPath
	}
	if eff.Location.Exists || eff.Location.Explicit {
		return eff.Location.Path
	}
	return ""
}

func renderMaintenancePlanText(w io.Writer, plan servicectl.MaintenancePlan) {
	fmt.Fprintf(w, "status: %s\nplan_id: %s\nrepo_id: %s\n", plan.Status, plan.PlanID, plan.RepoID)
	fmt.Fprintf(w, "configuration_hash: %s\n", plan.ConfigurationHash)
	fmt.Fprintf(w, "cache_ref: %s\ncache_uuid: %s\ncache_location: %s (%s)\n", plan.Cache.Ref, plan.Cache.UUID, plan.Cache.LocationKind, plan.Cache.PathFingerprint)
	fmt.Fprintf(w, "service: %s\nprovider: %s model=%s revision=%s boundary=%s\n", plan.Service.Status, cliEmptyAsNone(plan.Provider.Provider), cliEmptyAsNone(plan.Provider.Model), cliEmptyAsNone(plan.Provider.ModelRevision), plan.Provider.DataBoundary)
	fmt.Fprintf(w, "policy: sync=%s rag=%t profile=%s\n", plan.Policy.SyncMode, plan.Policy.RAGEnabled, cliEmptyAsNone(plan.Policy.Profile))
	for _, action := range plan.Actions {
		fmt.Fprintf(w, "action: %s class=%s status=%s boundary=%s — %s\n", action.ID, action.Class, action.Status, cliEmptyAsNone(action.DataBoundary), action.Summary)
		if action.Handoff != "" {
			fmt.Fprintf(w, "  handoff: %s\n", action.Handoff)
		}
	}
	for _, blocker := range plan.Blockers {
		fmt.Fprintf(w, "blocker: %s\n", blocker)
	}
	fmt.Fprintf(w, "next_action: %s\n", plan.NextAction)
}

func renderMaintenanceApplyText(w io.Writer, result servicectl.MaintenanceApplyResult) {
	fmt.Fprintf(w, "status: %s\nplan_id: %s\nrepo_id: %s\n", result.Status, result.PlanID, result.RepoID)
	if result.Registration != nil {
		fmt.Fprintf(w, "registration_id: %s\ncache_uuid: %s\n", result.Registration.RegistrationID, result.Registration.CacheUUID)
	}
	if len(result.CompletedStages) > 0 {
		fmt.Fprintf(w, "completed_stages: %s\n", strings.Join(result.CompletedStages, ","))
	}
	if len(result.JobsStarted) > 0 {
		fmt.Fprintf(w, "jobs_started: %s\n", strings.Join(result.JobsStarted, ","))
	}
	for _, action := range result.PendingActions {
		fmt.Fprintf(w, "pending: %s class=%s — %s\n", action.ID, action.Class, action.Summary)
	}
	if result.AuditReceipt != "" {
		fmt.Fprintf(w, "audit_receipt: %s\n", result.AuditReceipt)
	}
	if result.FailureClass != "" {
		fmt.Fprintf(w, "failure_class: %s\n", result.FailureClass)
	}
	if result.Message != "" {
		fmt.Fprintf(w, "message: %s\n", result.Message)
	}
	fmt.Fprintf(w, "next_action: %s\n", result.NextAction)
}

func executeRAGCommand(ctx context.Context, args []string, opts options, stdout io.Writer, stderr io.Writer, deps localCommandDeps) int {
	sub, ok := firstArg(args)
	if !ok {
		return writeError(stderr, opts.format, service.ErrInvalidQuery{Field: "rag", Message: "subcommand is required"})
	}
	switch sub {
	case "enable":
		return executeMaintenanceCommand(ctx, []string{"enable"}, opts, stdout, stderr, deps)
	case "setup":
		eff, err := config.LoadEffective(deps.Source, config.Overrides{})
		if err != nil {
			fmt.Fprintln(stderr, config.RedactDiagnostic(err.Error(), deps.Source))
			return 1
		}
		var progress func(rag.SetupProgress)
		if opts.format == "text" {
			progress = func(event rag.SetupProgress) {
				if event.Phase == "model_pull_started" {
					fmt.Fprintf(stderr, "rag setup: pulling model %s; this can take several minutes...\n", event.Model)
				}
			}
		}
		result, err := rag.Setup(ctx, rag.SetupRequest{Config: eff.Config, Profile: opts.profile, Yes: opts.yes, DryRun: opts.dryRun, Runtime: deps.RAGRuntime, Progress: progress})
		if err != nil {
			return writeError(stderr, opts.format, err)
		}
		code := render(stdout, opts.format, result, renderRAGSetupText)
		if result.Status != "ready" && !opts.dryRun {
			return 1
		}
		return code
	case "index":
		manager := servicectl.Manager{Source: deps.Source, BinaryPath: os.Args[0], Version: buildinfo.Current().Version, Commit: buildinfo.Current().Commit}
		client, clientErr := manager.Client()
		if clientErr != nil {
			return writeError(stderr, opts.format, clientErr)
		}
		var job servicectl.Job
		if err := client.Call(ctx, "Jobs.StartRAGIndex", servicectl.StartRAGIndexJobRequest{RepoID: opts.repo, Profile: opts.profile, CachePath: opts.cachePath, BatchSize: opts.batchSize, ChunkPolicy: opts.policy}, &job); err != nil {
			return writeError(stderr, opts.format, err)
		}
		if opts.detach {
			return render(stdout, opts.format, job, renderServiceJobText)
		}
		return attachRAGIndexJob(ctx, client, job.ID, opts, stdout, stderr)
	case "status":
		return executeRAGStatusCommand(ctx, opts, stdout, stderr, deps)
	case "search":
		return executeRAGSearchCommand(ctx, args[1:], opts, stdout, stderr, deps)
	default:
		return writeError(stderr, opts.format, service.ErrInvalidQuery{Field: "rag", Message: "unknown subcommand"})
	}
}

type repositoryDocsPlan struct {
	RepoID          string                          `json:"repo_id"`
	CommitOID       string                          `json:"commit_oid"`
	Policy          repositorydocs.PolicyResolution `json:"policy"`
	IncludeWorktree bool                            `json:"include_worktree"`
	EligibleFiles   int                             `json:"eligible_files"`
	EligibleBytes   int64                           `json:"eligible_bytes"`
	TrackedChanges  int                             `json:"tracked_changes,omitempty"`
	OverlayDigest   string                          `json:"overlay_digest,omitempty"`
}

type repositoryDocsStatus struct {
	RepoID          string                           `json:"repo_id"`
	CommitOID       string                           `json:"commit_oid"`
	Policy          repositorydocs.PolicyResolution  `json:"policy"`
	IncludeWorktree bool                             `json:"include_worktree"`
	OverlayDigest   string                           `json:"overlay_digest,omitempty"`
	RevisionSets    []cache.RepositoryDocRevisionSet `json:"revision_sets"`
}

func executeRepositoryDocsCommand(ctx context.Context, args []string, opts options, stdout io.Writer, stderr io.Writer, deps localCommandDeps) int {
	sub, ok := firstArg(args)
	if !ok {
		return writeError(stderr, opts.format, service.ErrInvalidQuery{Field: "repo-docs", Message: "subcommand is required"})
	}
	if strings.TrimSpace(opts.repo) == "" {
		return writeError(stderr, opts.format, service.ErrInvalidQuery{Field: "repo", Message: "repository id is required"})
	}
	eff, err := config.LoadEffective(deps.Source, config.Overrides{CachePath: opts.cachePath})
	if err != nil {
		return writeError(stderr, opts.format, err)
	}
	manager := servicectl.Manager{Source: deps.Source, BinaryPath: os.Args[0], Version: buildinfo.Current().Version, Commit: buildinfo.Current().Commit, RuntimeDir: eff.Config.Service.RuntimeDir}
	client, err := manager.Client()
	if err != nil {
		return writeError(stderr, opts.format, err)
	}
	if sub == "register" {
		if strings.TrimSpace(opts.repositoryPath) == "" {
			return writeError(stderr, opts.format, service.ErrInvalidQuery{Field: "repository-path", Message: "repository path is required for explicit source registration"})
		}
		var entry servicectl.MaintenanceEntry
		if err := client.Call(ctx, "RepositoryDocs.RegisterSource", servicectl.RegisterRepositoryDocsSourceRequest{RepoID: opts.repo, RepositoryPath: opts.repositoryPath, Profile: opts.profile, CachePath: eff.Config.CachePath}, &entry); err != nil {
			return writeError(stderr, opts.format, err)
		}
		return render(stdout, opts.format, entry, func(w io.Writer, value servicectl.MaintenanceEntry) {
			fmt.Fprintf(w, "registration_id: %s\nrepo_id: %s\nsource_registration_id: %s\nsource_registration_generation: %d\n", value.RegistrationID, value.RepoID, value.RepositoryDocs.SourceRegistrationID, value.RepositoryDocs.SourceRegistrationGeneration)
		})
	}
	if sub == "rebind" {
		if strings.TrimSpace(opts.registrationID) == "" || opts.sourceRegistrationGeneration <= 0 || strings.TrimSpace(opts.repositoryPath) == "" {
			return writeError(stderr, opts.format, service.ErrInvalidQuery{Field: "repository-docs-source", Message: "registration-id, source-registration-generation, and repository-path are required for explicit rebind"})
		}
		var entry servicectl.MaintenanceEntry
		if err := client.Call(ctx, "RepositoryDocs.RebindSource", servicectl.RepositoryDocsSourceRebindRequest{
			RepoID: opts.repo, RegistrationID: opts.registrationID, SourceRegistrationID: opts.sourceRegistrationID, ExpectedGeneration: opts.sourceRegistrationGeneration,
			RepositoryPath: opts.repositoryPath, Profile: opts.profile,
		}, &entry); err != nil {
			return writeError(stderr, opts.format, err)
		}
		return render(stdout, opts.format, entry, func(w io.Writer, value servicectl.MaintenanceEntry) {
			fmt.Fprintf(w, "registration_id: %s\nrepo_id: %s\nsource_registration_id: %s\nsource_registration_generation: %d\n", value.RegistrationID, value.RepoID, value.RepositoryDocs.SourceRegistrationID, value.RepositoryDocs.SourceRegistrationGeneration)
		})
	}
	selector := servicectl.RepositoryDocsSourceSelector{RegistrationID: opts.registrationID, SourceRegistrationID: opts.sourceRegistrationID, SourceRegistrationGeneration: opts.sourceRegistrationGeneration}
	if selector.RegistrationID == "" || (selector.SourceRegistrationID == "") != (selector.SourceRegistrationGeneration <= 0) {
		return writeError(stderr, opts.format, service.ErrInvalidQuery{Field: "repository-docs-source", Message: "registration-id is required; source-registration-id and source-registration-generation must be supplied together when selecting among multiple authorities"})
	}
	queryRequest := servicectl.RepositoryDocsQueryRequest{RepositoryDocsSourceSelector: selector, RepoID: opts.repo, Revision: opts.revision, IncludeWorktree: opts.includeWorktree, Mode: opts.searchMode, Limit: opts.limit}
	switch sub {
	case "policy":
		var value repositorydocs.PolicyResult
		if err := client.Call(ctx, "RepositoryDocs.Policy", queryRequest, &value); err != nil {
			return writeError(stderr, opts.format, err)
		}
		return render(stdout, opts.format, value, func(w io.Writer, value repositorydocs.PolicyResult) {
			fmt.Fprintf(w, "repo_id: %s\ncommit_oid: %s\npolicy_status: %s\npolicy_source: %s\npolicy_hash: %s\ninclude_worktree: %t\n", value.RepoID, value.CommitOID, value.Policy.Status, value.Policy.Source, value.Policy.PolicyHash, value.IncludeWorktree)
			if value.OverlayDigest != "" {
				fmt.Fprintf(w, "overlay_digest: %s\n", value.OverlayDigest)
			}
		})
	case "plan":
		var value repositorydocs.PlanResult
		if err := client.Call(ctx, "RepositoryDocs.Plan", queryRequest, &value); err != nil {
			return writeError(stderr, opts.format, err)
		}
		return render(stdout, opts.format, value, func(w io.Writer, value repositorydocs.PlanResult) {
			fmt.Fprintf(w, "repo_id: %s\ncommit_oid: %s\npolicy_hash: %s\neligible_files: %d\neligible_bytes: %d\ninclude_worktree: %t\ntracked_changes: %d\n", value.RepoID, value.CommitOID, value.Policy.PolicyHash, value.EligibleFiles, value.EligibleBytes, value.IncludeWorktree, value.TrackedChanges)
		})
	case "status":
		var value repositorydocs.StatusResult
		if err := client.Call(ctx, "RepositoryDocs.Status", queryRequest, &value); err != nil {
			return writeError(stderr, opts.format, err)
		}
		return render(stdout, opts.format, value, func(w io.Writer, value repositorydocs.StatusResult) {
			fmt.Fprintf(w, "repo_id: %s\ncommit_oid: %s\npolicy_status: %s\nrevision_sets: %d\n", value.RepoID, value.CommitOID, value.Policy.Status, len(value.RevisionSets))
			for _, set := range value.RevisionSets {
				fmt.Fprintf(w, "set: %s state=%s coverage=%d/%d reused=%d failed=%d missing=%d\n", set.ID, set.State, set.EmbeddedChunks+set.ReusedChunks, set.EligibleChunks, set.ReusedChunks, set.FailedChunks, set.MissingObjects)
			}
		})
	case "index":
		var job servicectl.Job
		request := servicectl.StartRepositoryDocsIndexJobRequest{
			RepoID: opts.repo, RegistrationID: selector.RegistrationID, SourceRegistrationID: selector.SourceRegistrationID, SourceRegistrationGeneration: selector.SourceRegistrationGeneration, Revision: opts.revision,
			IncludeWorktree: opts.includeWorktree, Profile: opts.profile,
			BatchSize: opts.batchSize,
		}
		if err := client.Call(ctx, "Jobs.StartRepositoryDocsIndex", request, &job); err != nil {
			return writeError(stderr, opts.format, err)
		}
		if opts.detach {
			return render(stdout, opts.format, job, renderServiceJobText)
		}
		return attachServiceJob(ctx, client, job.ID, opts, stdout, stderr)
	case "search":
		if len(args) < 2 {
			return writeError(stderr, opts.format, service.ErrInvalidQuery{Field: "query", Message: "query is required"})
		}
		queryRequest.Query = strings.Join(args[1:], " ")
		var result repositorydocs.SearchResult
		if err := client.Call(ctx, "RepositoryDocs.Search", queryRequest, &result); err != nil {
			return writeError(stderr, opts.format, err)
		}
		return render(stdout, opts.format, result, func(w io.Writer, value repositorydocs.SearchResult) {
			fmt.Fprintf(w, "repo_id: %s\ncorpus_kind: %s\neffective_revision: %s\nrequested_mode: %s\neffective_mode: %s\nauthority: %s\nrevision_set_id: %s\nresults: %d\n", value.RepoID, value.CorpusKind, value.EffectiveRevision, value.RequestedMode, value.EffectiveMode, value.Authority, value.RevisionSetID, len(value.Hits))
			for _, hit := range value.Hits {
				fmt.Fprintf(w, "%d. %s:%d-%d score=%.6f authority=%s\n%s\n", hit.Rank, hit.Citation.Path, hit.Citation.LineStart, hit.Citation.LineEnd, hit.Score, hit.Citation.Authority, hit.Snippet)
			}
			for _, warning := range value.WarningDetails {
				fmt.Fprintf(w, "warning: %s: %s\n", warning.Code, warning.Message)
			}
		})
	default:
		return writeError(stderr, opts.format, service.ErrInvalidQuery{Field: "repo-docs", Message: "unknown subcommand"})
	}
}

func executeRAGSearchCommand(ctx context.Context, args []string, opts options, stdout io.Writer, stderr io.Writer, deps localCommandDeps) int {
	if len(args) == 0 {
		return writeError(stderr, opts.format, service.ErrInvalidQuery{Field: "query", Message: "query is required"})
	}
	eff, err := config.LoadEffective(deps.Source, config.Overrides{CachePath: opts.cachePath})
	if err != nil {
		fmt.Fprintln(stderr, config.RedactDiagnostic(err.Error(), deps.Source))
		return 1
	}
	store, err := cache.NewSQLiteReadOnlyStore(ctx, eff.Config.CachePath)
	if err != nil {
		return writeError(stderr, opts.format, err)
	}
	defer store.Close()
	ops := rag.NewOperations(store, eff.Config, rag.OperationsOptions{})
	result, err := ops.Search(ctx, rag.SearchRequest{
		RepoID:        opts.repo,
		Query:         strings.Join(args, " "),
		ProfileID:     opts.profile,
		SourceID:      opts.sourceID,
		RecordID:      opts.recordID,
		SnapshotID:    opts.snapshotID,
		ChunkPolicyID: opts.policy,
		TopK:          opts.topK,
		Limit:         opts.limit,
	})
	if err != nil {
		return writeError(stderr, opts.format, err)
	}
	return render(stdout, opts.format, result, renderRAGSearchText)
}

func executeRAGStatusCommand(ctx context.Context, opts options, stdout io.Writer, stderr io.Writer, deps localCommandDeps) int {
	eff, err := config.LoadEffective(deps.Source, config.Overrides{CachePath: opts.cachePath})
	if err != nil {
		fmt.Fprintln(stderr, config.RedactDiagnostic(err.Error(), deps.Source))
		return 1
	}
	store, err := cache.NewSQLiteReadOnlyStore(ctx, eff.Config.CachePath)
	if err != nil {
		return writeError(stderr, opts.format, err)
	}
	defer store.Close()
	manager := servicectl.Manager{Source: deps.Source, BinaryPath: os.Args[0], Version: buildinfo.Current().Version, Commit: buildinfo.Current().Commit, RuntimeDir: eff.Config.Service.RuntimeDir}
	ops := rag.NewOperations(store, eff.Config, rag.OperationsOptions{ServiceState: func(ctx context.Context, repoID string) (*rag.ServiceStatus, *rag.JobStatus) {
		return lookupRAGServiceState(ctx, manager, repoID)
	}})
	result, err := ops.Status(ctx, rag.StatusRequest{
		RepoID:    opts.repo,
		ProfileID: opts.profile,
	})
	if err != nil {
		return writeError(stderr, opts.format, err)
	}
	return render(stdout, opts.format, result, renderRAGStatusText)
}

func lookupRAGServiceState(ctx context.Context, manager servicectl.Manager, repoID string) (*rag.ServiceStatus, *rag.JobStatus) {
	status, err := manager.Status()
	var serviceStatus *rag.ServiceStatus
	if err == nil {
		serviceStatus = &rag.ServiceStatus{
			Status:        status.Status,
			Running:       status.Running,
			PID:           status.PID,
			SocketPresent: status.SocketPresent,
			SocketPath:    status.SocketPath,
			Message:       status.Message,
		}
	}
	if serviceStatus == nil || !serviceStatus.Running {
		return serviceStatus, nil
	}
	client, err := manager.Client()
	if err != nil {
		serviceStatus.Message = firstNonEmpty(serviceStatus.Message, err.Error())
		return serviceStatus, nil
	}
	var result servicectl.JobListResult
	if err := client.Call(ctx, "Jobs.List", nil, &result); err != nil {
		serviceStatus.Message = firstNonEmpty(serviceStatus.Message, err.Error())
		return serviceStatus, nil
	}
	for _, job := range result.Jobs {
		if job.Type != servicectl.RAGIndexJobType || job.RepoID != repoID {
			continue
		}
		if job.Status != servicectl.JobStatusQueued && job.Status != servicectl.JobStatusRunning {
			continue
		}
		return serviceStatus, ragJobStatusFromService(job)
	}
	return serviceStatus, nil
}

func ragJobStatusFromService(job servicectl.Job) *rag.JobStatus {
	return &rag.JobStatus{
		ID:        job.ID,
		Type:      job.Type,
		RepoID:    job.RepoID,
		ProfileID: job.ProfileID,
		Status:    job.Status,
		Steps:     job.Steps,
		Completed: job.Completed,
		Error:     job.Error,
		Progress:  append([]service.ProgressEvent(nil), job.Progress...),
	}
}

func renderRAGSetupText(w io.Writer, result rag.SetupResult) {
	fmt.Fprintf(w, "status: %s\n", result.Status)
	fmt.Fprintf(w, "profile: %s\n", result.Profile)
	fmt.Fprintf(w, "provider: %s\n", result.Provider)
	fmt.Fprintf(w, "provider_type: %s\n", result.ProviderType)
	fmt.Fprintf(w, "endpoint: %s\n", result.Endpoint)
	if result.Executable != "" {
		fmt.Fprintf(w, "executable: %s\n", result.Executable)
	}
	if result.ExecutablePath != "" {
		fmt.Fprintf(w, "executable_path: %s\n", result.ExecutablePath)
	}
	fmt.Fprintf(w, "provider_installed: %t\n", result.ProviderInstalled)
	fmt.Fprintf(w, "provider_live: %t\n", result.ProviderLive)
	fmt.Fprintf(w, "autostart: %t\n", result.Autostart)
	fmt.Fprintf(w, "model: %s\n", result.Model)
	fmt.Fprintf(w, "model_available: %t\n", result.ModelAvailable)
	if result.ModelStorePath != "" {
		fmt.Fprintf(w, "model_store_path: %s\n", result.ModelStorePath)
	}
	if result.ProviderModelEnv != "" {
		fmt.Fprintf(w, "provider_model_env: %s\n", result.ProviderModelEnv)
	}
	if result.ProviderModelPath != "" {
		fmt.Fprintf(w, "provider_model_path: %s\n", result.ProviderModelPath)
	}
	fmt.Fprintf(w, "pull_attempted: %t\n", result.PullAttempted)
	fmt.Fprintf(w, "embedding_smoke: %s\n", result.EmbeddingSmoke)
	if len(result.Actions) > 0 {
		fmt.Fprintf(w, "actions: %s\n", strings.Join(result.Actions, "; "))
	}
	if len(result.NextActions) > 0 {
		fmt.Fprintf(w, "next_actions: %s\n", strings.Join(result.NextActions, "; "))
	}
	if len(result.InstallInstructions) > 0 {
		fmt.Fprintf(w, "install_instructions: %s\n", strings.Join(result.InstallInstructions, " "))
	}
	if len(result.Diagnostics) > 0 {
		fmt.Fprintf(w, "diagnostics: %s\n", strings.Join(result.Diagnostics, "; "))
	}
}

func renderRAGStatusText(w io.Writer, result rag.StatusResult) {
	fmt.Fprintf(w, "status: %s\n", result.Status)
	fmt.Fprintf(w, "repo_id: %s\n", result.RepoID)
	fmt.Fprintf(w, "profile: %s\n", cliEmptyAsNone(result.Provider.ProfileID))
	fmt.Fprintf(w, "provider: %s\n", cliEmptyAsNone(result.Provider.ProviderID))
	fmt.Fprintf(w, "provider_type: %s\n", cliEmptyAsNone(result.Provider.ProviderType))
	fmt.Fprintf(w, "endpoint: %s\n", cliEmptyAsNone(result.Provider.Endpoint))
	fmt.Fprintf(w, "model: %s\n", cliEmptyAsNone(result.Provider.Model))
	if result.Provider.Revision != "" {
		fmt.Fprintf(w, "revision: %s\n", result.Provider.Revision)
	}
	fmt.Fprintf(w, "provider_ready: %t\n", result.Provider.Ready)
	fmt.Fprintf(w, "namespace_exists: %t\n", result.Namespace.Exists)
	if result.Namespace.ID != "" {
		fmt.Fprintf(w, "namespace_id: %s\n", result.Namespace.ID)
	}
	fmt.Fprintf(w, "coverage: %d/%d embedded, %d missing, %d stale, %d failed, %d skipped\n", result.Coverage.EmbeddedChunks, result.Coverage.TotalChunks, result.Coverage.MissingChunks, result.Coverage.StaleChunks, result.Coverage.FailedChunks, result.Coverage.SkippedChunks)
	if result.ActiveJob != nil {
		fmt.Fprintf(w, "active_job: %s %s %d/%d\n", result.ActiveJob.ID, result.ActiveJob.Status, result.ActiveJob.Completed, result.ActiveJob.Steps)
	}
	if result.LastRun != nil {
		fmt.Fprintf(w, "last_run: %s %s %d/%d\n", result.LastRun.ID, result.LastRun.Status, result.LastRun.EmbeddedChunks+result.LastRun.SkippedChunks, result.LastRun.TotalChunks)
	}
	if result.Service != nil {
		fmt.Fprintf(w, "service: %s\n", result.Service.Status)
	}
	if result.FailureClass != "" {
		fmt.Fprintf(w, "failure_class: %s\n", result.FailureClass)
	}
	if result.Provider.Message != "" {
		fmt.Fprintf(w, "provider_message: %s\n", result.Provider.Message)
	}
	if result.Message != "" {
		fmt.Fprintf(w, "message: %s\n", result.Message)
	}
}

func renderRAGSearchText(w io.Writer, result rag.SearchResult) {
	fmt.Fprintf(w, "search_mode: %s status: %s\n", result.SearchMode, result.Status)
	if result.Namespace.ID != "" {
		fmt.Fprintf(w, "namespace: %s model: %s\n", result.Namespace.ID, cliEmptyAsNone(result.Provider.Model))
	}
	for _, item := range result.Results {
		locator := item.Path
		if locator == "" {
			locator = item.SourceID
		}
		if item.LineStart > 0 {
			locator = fmt.Sprintf("%s:%d", locator, item.LineStart)
		}
		fmt.Fprintf(w, "%d %.4f sem=%.4f lex=%.4f %s %s %s\n", item.Rank, item.Score.Hybrid, item.Score.Semantic, item.Score.Lexical, item.SourceID, locator, item.Snippet)
	}
	for _, warning := range result.Warnings {
		fmt.Fprintf(w, "warning: %s\n", warning)
	}
	if result.FailureClass != "" {
		fmt.Fprintf(w, "failure_class: %s\n", result.FailureClass)
	}
	if result.Message != "" {
		fmt.Fprintf(w, "message: %s\n", result.Message)
	}
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

func executeServiceCommand(ctx context.Context, args []string, opts options, stdout io.Writer, stderr io.Writer, deps localCommandDeps) int {
	sub, ok := firstArg(args)
	if !ok {
		return writeError(stderr, opts.format, service.ErrInvalidQuery{Field: "service", Message: "subcommand is required"})
	}
	rest := args[1:]
	eff, configErr := config.LoadEffective(deps.Source, config.Overrides{})
	if configErr != nil {
		return writeError(stderr, opts.format, configErr)
	}
	manager := servicectl.Manager{Source: deps.Source, BinaryPath: os.Args[0], Version: buildinfo.Current().Version, Commit: buildinfo.Current().Commit, RuntimeDir: eff.Config.Service.RuntimeDir, AdminBind: opts.adminBind, AdminAutoStart: opts.admin, AdminAllowNonLoopback: opts.adminUnsafe, AdminCachePath: eff.Config.CachePath, JobRetention: &eff.Config.Service.JobRetention}
	var (
		status servicectl.Status
		err    error
	)
	switch sub {
	case "install":
		status, err = manager.Install(opts.overwrite)
	case "repair":
		status, err = manager.Repair(ctx)
	case "uninstall":
		status, err = manager.Uninstall()
	case "start":
		status, err = manager.Start(ctx)
	case "stop":
		status, err = manager.Stop(ctx)
	case "status":
		status, err = manager.Status()
		if err == nil && status.Running {
			if client, clientErr := manager.Client(); clientErr == nil {
				var remote servicectl.Status
				if callErr := client.Call(ctx, "Service.Status", nil, &remote); callErr == nil {
					status = remote
				}
			}
		}
	case "doctor":
		status, err = manager.Doctor()
	case "run":
		err = manager.Run(ctx)
		if errors.Is(err, context.Canceled) {
			return 0
		}
		if err == nil {
			return 0
		}
		return writeError(stderr, opts.format, err)
	case "fake-job":
		client, clientErr := manager.Client()
		if clientErr != nil {
			return writeError(stderr, opts.format, clientErr)
		}
		var job servicectl.Job
		if err := client.Call(ctx, "Jobs.StartFake", servicectl.StartFakeJobRequest{Steps: opts.steps, IntervalMS: opts.intervalMS}, &job); err != nil {
			return writeError(stderr, opts.format, err)
		}
		if opts.detach {
			return render(stdout, opts.format, job, renderServiceJobText)
		}
		return attachServiceJob(ctx, client, job.ID, opts, stdout, stderr)
	case "jobs":
		client, clientErr := manager.Client()
		if clientErr != nil {
			return writeError(stderr, opts.format, clientErr)
		}
		var result servicectl.JobListResult
		if err := client.Call(ctx, "Jobs.List", nil, &result); err != nil {
			return writeError(stderr, opts.format, err)
		}
		return render(stdout, opts.format, result, renderServiceJobListText)
	case "maintenance", "reconcile":
		client, clientErr := manager.Client()
		if clientErr != nil {
			return writeError(stderr, opts.format, clientErr)
		}
		if sub == "maintenance" {
			var result servicectl.MaintenanceListResult
			if err := client.Call(ctx, "Maintenance.List", nil, &result); err != nil {
				return writeError(stderr, opts.format, err)
			}
			return render(stdout, opts.format, result, renderMaintenanceListText)
		}
		var result servicectl.MaintenanceReconcileResult
		if err := client.Call(ctx, "Maintenance.Reconcile", nil, &result); err != nil {
			return writeError(stderr, opts.format, err)
		}
		return render(stdout, opts.format, result, renderMaintenanceReconcileText)
	case "job", "attach", "cancel":
		id, idOK := firstArg(rest)
		if !idOK {
			return writeError(stderr, opts.format, service.ErrInvalidQuery{Field: "job_id", Message: "job id is required"})
		}
		client, clientErr := manager.Client()
		if clientErr != nil {
			return writeError(stderr, opts.format, clientErr)
		}
		if sub == "attach" {
			return attachServiceJob(ctx, client, id, opts, stdout, stderr)
		}
		var job servicectl.Job
		method := "Jobs.Get"
		if sub == "cancel" {
			method = "Jobs.Cancel"
		}
		if err := client.Call(ctx, method, map[string]string{"job_id": id}, &job); err != nil {
			return writeError(stderr, opts.format, err)
		}
		return render(stdout, opts.format, job, renderServiceJobText)
	default:
		return writeError(stderr, opts.format, service.ErrInvalidQuery{Field: "service", Message: "unknown subcommand"})
	}
	if err != nil {
		return writeError(stderr, opts.format, err)
	}
	return render(stdout, opts.format, status, renderServiceStatusText)
}

func executeAdminCommand(ctx context.Context, args []string, opts options, stdout io.Writer, stderr io.Writer, deps localCommandDeps) int {
	sub, ok := firstArg(args)
	if !ok {
		return writeError(stderr, opts.format, service.ErrInvalidQuery{Field: "admin", Message: "subcommand is required"})
	}
	eff, err := config.LoadEffective(deps.Source, config.Overrides{})
	if err != nil {
		return writeError(stderr, opts.format, err)
	}
	manager := servicectl.Manager{Source: deps.Source, BinaryPath: os.Args[0], Version: buildinfo.Current().Version, Commit: buildinfo.Current().Commit, RuntimeDir: eff.Config.Service.RuntimeDir}
	client, err := manager.Client()
	if err != nil {
		return writeError(stderr, opts.format, err)
	}
	switch sub {
	case "status":
		var status adminhttp.Status
		if err := client.Call(ctx, "Admin.Status", nil, &status); err != nil {
			return writeError(stderr, opts.format, fmt.Errorf("admin: existing daemon is unavailable: %w", err))
		}
		return render(stdout, opts.format, status, renderAdminStatusText)
	case "open":
		token, tokenHash, err := adminhttp.NewLaunchToken()
		if err != nil {
			return writeError(stderr, opts.format, err)
		}
		var result adminhttp.OpenResult
		if err := client.Call(ctx, "Admin.Open", adminhttp.OpenRequest{LaunchTokenHash: tokenHash}, &result); err != nil {
			return writeError(stderr, opts.format, fmt.Errorf("admin: existing daemon is unavailable; start or install the service first: %w", err))
		}
		launchURL := result.URL + "/#launch=" + url.QueryEscape(token)
		if !opts.noBrowser {
			opener := deps.OpenURL
			if opener == nil {
				opener = openBrowserURL
			}
			if err := opener(launchURL); err != nil {
				return writeError(stderr, opts.format, fmt.Errorf("admin: open browser: %w", err))
			}
		}
		output := result
		if opts.noBrowser {
			output.URL = launchURL
		}
		return render(stdout, opts.format, output, func(w io.Writer, value adminhttp.OpenResult) {
			fmt.Fprintf(w, "admin_status: running\nadmin_url: %s\n", value.URL)
		})
	default:
		return writeError(stderr, opts.format, service.ErrInvalidQuery{Field: "admin", Message: "unknown subcommand"})
	}
}

func renderAdminStatusText(w io.Writer, status adminhttp.Status) {
	fmt.Fprintf(w, "admin_status: %s\nadmin_url: %s\nadmin_bind: %s\n", map[bool]string{true: "running", false: "stopped"}[status.Running], cliEmptyAsNone(status.URL), cliEmptyAsNone(status.Bind))
}

func openBrowserURL(value string) error {
	var command string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		command, args = "open", []string{value}
	case "windows":
		command, args = "rundll32", []string{"url.dll,FileProtocolHandler", value}
	default:
		command, args = "xdg-open", []string{value}
	}
	return exec.Command(command, args...).Start()
}

func attachServiceJob(ctx context.Context, client *servicectl.RPCClient, id string, opts options, stdout io.Writer, stderr io.Writer) int {
	seen := 0
	for {
		var job servicectl.Job
		if err := client.Call(ctx, "Jobs.Get", map[string]string{"job_id": id}, &job); err != nil {
			return writeError(stderr, opts.format, err)
		}
		for ; seen < len(job.Progress); seen++ {
			renderServiceJobProgress(stderr, job.ID, job.Progress[seen])
		}
		if serviceJobTerminal(job.Status) {
			return render(stdout, opts.format, job, renderServiceJobText)
		}
		select {
		case <-ctx.Done():
			return writeError(stderr, opts.format, ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func attachRAGIndexJob(ctx context.Context, client *servicectl.RPCClient, id string, opts options, stdout io.Writer, stderr io.Writer) int {
	mode := syncProgressMode(opts, stderr)
	seen := 0
	state := ragIndexProgressState{Started: time.Now().UTC()}
	encoder := json.NewEncoder(stderr)
	renderedSpinner := false
	for {
		var job servicectl.Job
		if err := client.Call(ctx, "Jobs.Get", map[string]string{"job_id": id}, &job); err != nil {
			return writeError(stderr, opts.format, err)
		}
		state.ApplyJob(job)
		for ; seen < len(job.Progress); seen++ {
			state.ApplyEvent(job.Progress[seen])
			switch mode {
			case "jsonl":
				_ = encoder.Encode(ragIndexProgressJSONEvent(state, time.Now().UTC()))
			case "lines":
				renderRAGIndexProgressLine(stderr, state, time.Now().UTC())
			}
		}
		if mode == "spinner" {
			renderRAGIndexProgressSpinnerFrame(stderr, &state, time.Now().UTC())
			renderedSpinner = true
		}
		if serviceJobTerminal(job.Status) {
			if mode == "spinner" && renderedSpinner {
				renderRAGIndexProgressSpinnerFinal(stderr, &state, time.Now().UTC())
			}
			code := render(stdout, opts.format, job, renderServiceJobText)
			if job.Status == servicectl.JobStatusFailed || job.Status == servicectl.JobStatusCancelled || job.Status == servicectl.JobStatusInterrupted {
				return 1
			}
			return code
		}
		select {
		case <-ctx.Done():
			return writeError(stderr, opts.format, ctx.Err())
		case <-time.After(120 * time.Millisecond):
		}
	}
}

func attachSyncJob(ctx context.Context, client *servicectl.RPCClient, id string, opts options, stdout io.Writer, stderr io.Writer) int {
	mode := syncProgressMode(opts, stderr)
	seen := 0
	started := time.Now().UTC()
	state := syncProgressSpinnerState{Started: started}
	encoder := json.NewEncoder(stderr)
	renderedSpinner := false
	for {
		var job servicectl.Job
		if err := client.Call(ctx, "Jobs.Get", map[string]string{"job_id": id}, &job); err != nil {
			return writeError(stderr, opts.format, err)
		}
		if job.StartedAt != nil {
			started = *job.StartedAt
			if state.Started.IsZero() || state.Started.After(started) {
				state.Started = started
			}
		}
		for ; seen < len(job.Progress); seen++ {
			ev := job.Progress[seen]
			state.Apply(ev)
			switch mode {
			case "jsonl":
				_ = encoder.Encode(syncProgressJSONEvent(ev, started))
			case "lines":
				renderSyncProgressLine(stderr, ev, started)
			}
		}
		if mode == "spinner" {
			renderSyncProgressSpinnerFrame(stderr, &state)
			renderedSpinner = true
		}
		if serviceJobTerminal(job.Status) {
			if mode == "spinner" && renderedSpinner {
				fmt.Fprint(stderr, "\r\033[K")
			}
			code := render(stdout, opts.format, job, renderServiceJobText)
			if job.Status == servicectl.JobStatusFailed || job.Status == servicectl.JobStatusCancelled || job.Status == servicectl.JobStatusInterrupted {
				return 1
			}
			return code
		}
		select {
		case <-ctx.Done():
			return writeError(stderr, opts.format, ctx.Err())
		case <-time.After(120 * time.Millisecond):
		}
	}
}

func serviceJobTerminal(status string) bool {
	switch status {
	case servicectl.JobStatusSucceeded, servicectl.JobStatusFailed, servicectl.JobStatusCancelled, servicectl.JobStatusInterrupted:
		return true
	default:
		return false
	}
}

func renderServiceStatusText(w io.Writer, status servicectl.Status) {
	fmt.Fprintf(w, "status: %s\n", status.Status)
	fmt.Fprintf(w, "installed: %t\n", status.Installed)
	fmt.Fprintf(w, "running: %t\n", status.Running)
	if status.PID > 0 {
		fmt.Fprintf(w, "pid: %d\n", status.PID)
	}
	fmt.Fprintf(w, "pid_alive: %t\n", status.PIDAlive)
	fmt.Fprintf(w, "socket_present: %t\n", status.SocketPresent)
	fmt.Fprintf(w, "socket_path: %s\n", status.SocketPath)
	fmt.Fprintf(w, "runtime_dir: %s\n", status.RuntimeDir)
	fmt.Fprintf(w, "log_dir: %s\n", status.LogDir)
	fmt.Fprintf(w, "state_path: %s\n", status.StatePath)
	fmt.Fprintf(w, "install_kind: %s\n", status.InstallKind)
	fmt.Fprintf(w, "install_path: %s\n", status.InstallPath)
	if status.RAG != nil {
		fmt.Fprintf(w, "rag_status: %s\n", status.RAG.Status)
		fmt.Fprintf(w, "rag_profile: %s\n", status.RAG.Profile)
		fmt.Fprintf(w, "rag_provider: %s\n", status.RAG.Provider)
		fmt.Fprintf(w, "rag_endpoint: %s\n", status.RAG.Endpoint)
		fmt.Fprintf(w, "rag_model: %s\n", status.RAG.Model)
		fmt.Fprintf(w, "rag_model_available: %t\n", status.RAG.ModelAvailable)
		fmt.Fprintf(w, "rag_provider_installed: %t\n", status.RAG.ProviderInstalled)
		fmt.Fprintf(w, "rag_provider_live: %t\n", status.RAG.ProviderLive)
		if status.RAG.ModelStorePath != "" {
			fmt.Fprintf(w, "rag_model_store_path: %s\n", status.RAG.ModelStorePath)
		}
		if status.RAG.ProviderModelEnv != "" {
			fmt.Fprintf(w, "rag_provider_model_env: %s\n", status.RAG.ProviderModelEnv)
		}
		if len(status.RAG.Actions) > 0 {
			fmt.Fprintf(w, "rag_actions: %s\n", strings.Join(status.RAG.Actions, "; "))
		}
	}
	if status.UpdatedAt != nil && !status.UpdatedAt.IsZero() {
		fmt.Fprintf(w, "updated_at: %s\n", status.UpdatedAt.Format(time.RFC3339))
	}
	if status.Message != "" {
		fmt.Fprintf(w, "message: %s\n", status.Message)
	}
	if status.CacheReadiness != "" {
		fmt.Fprintf(w, "cache_readiness: %s\n", status.CacheReadiness)
	}
	for _, block := range status.CacheSchemaBlocks {
		renderCacheSchemaBlockText(w, "cache_schema_block", block)
	}
}

func renderServiceJobListText(w io.Writer, result servicectl.JobListResult) {
	if result.CacheReadiness != "" {
		fmt.Fprintf(w, "cache_readiness: %s\n", result.CacheReadiness)
	}
	for _, block := range result.CacheSchemaBlocks {
		renderCacheSchemaBlockText(w, "cache_schema_block", block)
	}
	for _, job := range result.Jobs {
		fmt.Fprintf(w, "%s\t%s\t%s\t%d/%d\n", job.ID, job.Type, job.Status, job.Completed, job.Steps)
	}
}

func renderMaintenanceListText(w io.Writer, result servicectl.MaintenanceListResult) {
	fmt.Fprintf(w, "schema_version: %s\n", result.SchemaVersion)
	fmt.Fprintf(w, "generation: %d\n", result.Generation)
	fmt.Fprintf(w, "managed_caches: %d\n", len(result.Entries))
	for _, entry := range result.Entries {
		fmt.Fprintf(w, "%s\t%s\t%s\tcontent=%d covered=%d\n", entry.RegistrationID, entry.RepoID, entry.State, entry.ContentGeneration, entry.CoveredGeneration)
		if len(entry.Aliases) > 0 {
			fmt.Fprintf(w, "  aliases: %s\n", strings.Join(entry.Aliases, ", "))
		}
		if len(entry.LegacyRegistrationIDs) > 0 {
			fmt.Fprintf(w, "  legacy_registration_ids: %s\n", strings.Join(entry.LegacyRegistrationIDs, ", "))
		}
		if entry.State == "cache_schema_blocked" {
			renderCacheSchemaBlockText(w, "  schema_block", servicectl.CacheSchemaBlock{
				RegistrationID: entry.RegistrationID, RepoID: entry.RepoID, CacheUUID: entry.CacheUUID,
				DetectedVersion: entry.DetectedSchemaVersion, ExpectedVersion: entry.ExpectedSchemaVersion,
				DaemonBinaryVersion: entry.DaemonBinaryVersion, DaemonBinaryCommit: entry.DaemonBinaryCommit,
				QuiesceState: entry.QuiesceState,
			})
		}
		if entry.IdentityConflict != nil {
			fmt.Fprintf(w, "  identity_conflict: %s details_available=%t candidates=%d paths=%d\n", entry.IdentityConflict.Kind, entry.IdentityConflict.DetailsAvailable, len(entry.IdentityConflict.Candidates), len(entry.IdentityConflict.PathFingerprints))
			candidates := append([]servicectl.MaintenanceIdentityCandidate(nil), entry.IdentityConflict.Candidates...)
			sort.Slice(candidates, func(i, j int) bool { return candidates[i].CandidateRef < candidates[j].CandidateRef })
			for _, candidate := range candidates {
				renderMaintenanceIdentityCandidateText(w, "    ", candidate)
			}
		}
	}
}

func renderCacheSchemaBlockText(w io.Writer, prefix string, block servicectl.CacheSchemaBlock) {
	fmt.Fprintf(w, "%s: detected=%d expected=%d", prefix, block.DetectedVersion, block.ExpectedVersion)
	if block.RegistrationID != "" {
		fmt.Fprintf(w, " registration_id=%s", block.RegistrationID)
	}
	if block.RepoID != "" {
		fmt.Fprintf(w, " repo_id=%s", block.RepoID)
	}
	if block.DaemonBinaryVersion != "" {
		fmt.Fprintf(w, " daemon_version=%s", block.DaemonBinaryVersion)
	}
	if block.DaemonBinaryCommit != "" {
		fmt.Fprintf(w, " daemon_commit=%s", block.DaemonBinaryCommit)
	}
	if block.DaemonSchemaMin != 0 || block.DaemonSchemaMax != 0 {
		fmt.Fprintf(w, " daemon_schema_range=%d..%d", block.DaemonSchemaMin, block.DaemonSchemaMax)
	}
	if block.QuiesceState != "" {
		fmt.Fprintf(w, " quiesce_state=%s", block.QuiesceState)
	}
	fmt.Fprintln(w)
}

func renderMaintenanceIdentityCandidateText(w io.Writer, indent string, candidate servicectl.MaintenanceIdentityCandidate) {
	fmt.Fprintf(w, "%scandidate: %s\n", indent, candidate.CandidateRef)
	if candidate.SelectionKind != "" {
		fmt.Fprintf(w, "%s  selection_kind: %s\n", indent, candidate.SelectionKind)
	}
	if candidate.RegistrationID != "" {
		fmt.Fprintf(w, "%s  registration_id: %s\n", indent, candidate.RegistrationID)
	}
	if candidate.RepoID != "" {
		fmt.Fprintf(w, "%s  repo_id: %s\n", indent, candidate.RepoID)
	}
	if candidate.PathFingerprint != "" {
		fmt.Fprintf(w, "%s  path_fingerprint: %s\n", indent, candidate.PathFingerprint)
	}
	if candidate.PolicyHash != "" {
		fmt.Fprintf(w, "%s  policy_hash: %s\n", indent, candidate.PolicyHash)
	}
	if candidate.ConfigHash != "" {
		fmt.Fprintf(w, "%s  config_hash: %s\n", indent, candidate.ConfigHash)
	}
	if candidate.SourceAuthorityHash != "" {
		fmt.Fprintf(w, "%s  source_authority_hash: %s\n", indent, candidate.SourceAuthorityHash)
	}
	if len(candidate.SourceRefs) > 0 {
		fmt.Fprintf(w, "%s  source_refs: %s\n", indent, strings.Join(candidate.SourceRefs, ", "))
	}
	if len(candidate.CohortRegistrationIDs) > 0 {
		fmt.Fprintf(w, "%s  cohort_registration_ids: %s\n", indent, strings.Join(candidate.CohortRegistrationIDs, ", "))
	}
	if len(candidate.CohortRepoIDs) > 0 {
		fmt.Fprintf(w, "%s  cohort_repo_ids: %s\n", indent, strings.Join(candidate.CohortRepoIDs, ", "))
	}
	fmt.Fprintf(w, "%s  was_enabled: %t\n", indent, candidate.WasEnabled)
	members := append([]servicectl.MaintenanceIdentityCandidate(nil), candidate.Members...)
	sort.Slice(members, func(i, j int) bool { return members[i].CandidateRef < members[j].CandidateRef })
	for _, member := range members {
		fmt.Fprintf(w, "%s  member:\n", indent)
		renderMaintenanceIdentityCandidateText(w, indent+"    ", member)
	}
}

func renderMaintenanceReconcileText(w io.Writer, result servicectl.MaintenanceReconcileResult) {
	fmt.Fprintf(w, "checked_at: %s\n", result.CheckedAt.Format(time.RFC3339))
	fmt.Fprintf(w, "managed_caches: %d\n", len(result.Entries))
	fmt.Fprintf(w, "jobs_started: %d\n", len(result.JobsStarted))
	for _, entry := range result.Entries {
		fmt.Fprintf(w, "%s\t%s\t%s\tcontent=%d covered=%d\n", entry.RegistrationID, entry.RepoID, entry.State, entry.ContentGeneration, entry.CoveredGeneration)
	}
}

func renderServiceJobText(w io.Writer, job servicectl.Job) {
	fmt.Fprintf(w, "job_id: %s\n", job.ID)
	fmt.Fprintf(w, "type: %s\n", job.Type)
	fmt.Fprintf(w, "status: %s\n", job.Status)
	fmt.Fprintf(w, "completed: %d\n", job.Completed)
	fmt.Fprintf(w, "steps: %d\n", job.Steps)
	fmt.Fprintf(w, "created_at: %s\n", job.CreatedAt.Format(time.RFC3339))
	fmt.Fprintf(w, "updated_at: %s\n", job.UpdatedAt.Format(time.RFC3339))
	if job.StartedAt != nil {
		fmt.Fprintf(w, "started_at: %s\n", job.StartedAt.Format(time.RFC3339))
	}
	if job.FinishedAt != nil {
		fmt.Fprintf(w, "finished_at: %s\n", job.FinishedAt.Format(time.RFC3339))
	}
	if job.Error != "" {
		fmt.Fprintf(w, "message: %s\n", job.Error)
	}
	if stage := job.SyncStage; stage != nil {
		fmt.Fprintf(w, "sync_phase: %s\n", stage.Phase)
		fmt.Fprintf(w, "sync_collection: %s\n", stage.Collection)
		fmt.Fprintf(w, "sync_stage_ref: %s\n", stage.StageRef)
		fmt.Fprintf(w, "sync_cache_ref: %s\n", stage.CacheRef)
		fmt.Fprintf(w, "sync_fetched: %d\n", stage.Fetched)
		fmt.Fprintf(w, "sync_staged: %d\n", stage.Staged)
		fmt.Fprintf(w, "sync_staged_bytes: %d\n", stage.StagedBytes)
		fmt.Fprintf(w, "sync_committed: %d\n", stage.Committed)
		fmt.Fprintf(w, "sync_retry: %d/%d\n", stage.Attempt, stage.RetryBudget)
		if !stage.RetryAfter.IsZero() {
			fmt.Fprintf(w, "sync_retry_after: %s\n", stage.RetryAfter.Format(time.RFC3339))
		}
		if stage.BlockerClass != "" {
			fmt.Fprintf(w, "sync_blocker_class: %s\n", stage.BlockerClass)
		}
		if stage.BlockingOp != "" {
			fmt.Fprintf(w, "sync_blocking_operation: %s\n", stage.BlockingOp)
		}
		if stage.BlockingJobRef != "" {
			fmt.Fprintf(w, "sync_blocking_job_ref: %s\n", stage.BlockingJobRef)
		}
		if !stage.FetchedAt.IsZero() {
			fmt.Fprintf(w, "sync_fetched_at: %s\n", stage.FetchedAt.Format(time.RFC3339))
		}
		if !stage.StagedAt.IsZero() {
			fmt.Fprintf(w, "sync_staged_at: %s\n", stage.StagedAt.Format(time.RFC3339))
		}
		if !stage.CommittedAt.IsZero() {
			fmt.Fprintf(w, "sync_committed_at: %s\n", stage.CommittedAt.Format(time.RFC3339))
		}
		if stage.TerminalCause != "" {
			fmt.Fprintf(w, "sync_terminal_reason: %s\n", stage.TerminalCause)
		}
	}
	if len(job.Progress) > 0 {
		fmt.Fprintf(w, "progress_events: %d\n", len(job.Progress))
	}
}

func renderServiceJobProgress(w io.Writer, jobID string, ev service.ProgressEvent) {
	fmt.Fprintf(w, "job progress: job_id=%s", jobID)
	if ev.Type != "" {
		fmt.Fprintf(w, " type=%s", ev.Type)
	}
	if ev.Phase != "" {
		fmt.Fprintf(w, " phase=%s", ev.Phase)
	}
	if ev.Collection != "" {
		fmt.Fprintf(w, " collection=%s", ev.Collection)
	}
	if ev.Page > 0 {
		fmt.Fprintf(w, " step=%d", ev.Page)
	}
	if ev.RecordsFetched > 0 {
		fmt.Fprintf(w, " records=%d", ev.RecordsFetched)
	}
	if ev.Message != "" {
		fmt.Fprintf(w, " message=%q", ev.Message)
	}
	fmt.Fprintln(w)
}

type ragIndexProgressState struct {
	Started  time.Time
	Frame    int
	JobID    string
	Status   string
	Phase    string
	Total    int
	Embedded int
	Skipped  int
	Failed   int
	Message  string
}

func (s *ragIndexProgressState) ApplyJob(job servicectl.Job) {
	s.JobID = job.ID
	if job.Status != "" {
		s.Status = job.Status
	}
	if job.StartedAt != nil && !job.StartedAt.IsZero() {
		s.Started = job.StartedAt.UTC()
	} else if !job.CreatedAt.IsZero() && s.Started.IsZero() {
		s.Started = job.CreatedAt.UTC()
	}
	if job.Steps > 0 {
		s.Total = job.Steps
	}
	if job.Error != "" {
		s.Message = job.Error
	}
}

func (s *ragIndexProgressState) ApplyEvent(ev service.ProgressEvent) {
	if ev.Phase != "" {
		s.Phase = ev.Phase
	}
	if ev.RecordsListed > 0 {
		s.Total = ev.RecordsListed
	}
	if ev.RecordsFetched > 0 {
		s.Embedded = ev.RecordsFetched
	}
	if ev.RecordsSkipped > 0 {
		s.Skipped = ev.RecordsSkipped
	}
	if ev.RecordsFailed > 0 {
		s.Failed = ev.RecordsFailed
	}
	if ev.Message != "" {
		s.Message = ev.Message
	}
}

func (s ragIndexProgressState) completed() int {
	return s.Embedded + s.Skipped
}

func renderRAGIndexProgressSpinnerFrame(w io.Writer, state *ragIndexProgressState, now time.Time) {
	frames := []string{"-", "\\", "|", "/"}
	frame := frames[state.Frame%len(frames)]
	state.Frame++
	fmt.Fprintf(w, "\r\033[K%s ", frame)
	renderRAGIndexProgressSummary(w, *state, now)
}

func renderRAGIndexProgressSpinnerFinal(w io.Writer, state *ragIndexProgressState, now time.Time) {
	fmt.Fprint(w, "\r\033[K")
	renderRAGIndexProgressSummary(w, *state, now)
	fmt.Fprintln(w)
}

func renderRAGIndexProgressLine(w io.Writer, state ragIndexProgressState, now time.Time) {
	fmt.Fprint(w, "rag index progress: ")
	renderRAGIndexProgressSummary(w, state, now)
	fmt.Fprintln(w)
}

func renderRAGIndexProgressSummary(w io.Writer, state ragIndexProgressState, now time.Time) {
	status := firstNonEmpty(state.Status, state.Phase, "running")
	fmt.Fprintf(w, "rag index %s", status)
	if state.Total > 0 {
		completed := state.completed()
		percent := float64(completed) * 100 / float64(state.Total)
		fmt.Fprintf(w, " %d/%d %.1f%%", completed, state.Total, percent)
	} else if state.completed() > 0 {
		fmt.Fprintf(w, " %d chunks", state.completed())
	}
	fmt.Fprintf(w, " %.1f chunks/s", ragIndexProgressSpeed(state, now))
	if state.Skipped > 0 {
		fmt.Fprintf(w, " skipped=%d", state.Skipped)
	}
	if state.Failed > 0 {
		fmt.Fprintf(w, " failed=%d", state.Failed)
	}
	fmt.Fprintf(w, " elapsed=%s", ragIndexProgressElapsed(state, now))
	if state.Message != "" && status != servicectl.JobStatusRunning && status != rag.RAGIndexStatusRunning {
		fmt.Fprintf(w, " message=%q", state.Message)
	}
}

type ragIndexProgressEventJSON struct {
	JobID      string  `json:"job_id,omitempty"`
	Status     string  `json:"status"`
	Total      int     `json:"total_chunks,omitempty"`
	Embedded   int     `json:"embedded_chunks"`
	Skipped    int     `json:"skipped_chunks,omitempty"`
	Failed     int     `json:"failed_chunks,omitempty"`
	Completed  int     `json:"completed_chunks"`
	Percent    float64 `json:"percent,omitempty"`
	ChunksPerS float64 `json:"chunks_per_second"`
	ElapsedMS  int64   `json:"elapsed_ms"`
	Message    string  `json:"message,omitempty"`
}

func ragIndexProgressJSONEvent(state ragIndexProgressState, now time.Time) ragIndexProgressEventJSON {
	completed := state.completed()
	var percent float64
	if state.Total > 0 {
		percent = float64(completed) * 100 / float64(state.Total)
	}
	return ragIndexProgressEventJSON{
		JobID:      state.JobID,
		Status:     firstNonEmpty(state.Status, state.Phase, "running"),
		Total:      state.Total,
		Embedded:   state.Embedded,
		Skipped:    state.Skipped,
		Failed:     state.Failed,
		Completed:  completed,
		Percent:    percent,
		ChunksPerS: ragIndexProgressSpeed(state, now),
		ElapsedMS:  ragIndexProgressElapsed(state, now).Milliseconds(),
		Message:    state.Message,
	}
}

func ragIndexProgressSpeed(state ragIndexProgressState, now time.Time) float64 {
	elapsed := ragIndexProgressElapsed(state, now)
	if elapsed <= 0 {
		return 0
	}
	return float64(state.Embedded) / elapsed.Seconds()
}

func ragIndexProgressElapsed(state ragIndexProgressState, now time.Time) time.Duration {
	started := state.Started
	if started.IsZero() {
		started = now
	}
	if now.Before(started) {
		return 0
	}
	return now.Sub(started).Round(time.Millisecond)
}

func dispatch(ctx context.Context, svc queryService, command string, args []string, opts options, stdout io.Writer, stderr io.Writer, plan startupPlan, deps localCommandDeps) int {
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
		results, err := svc.SearchSources(ctx, service.SearchSourcesRequest{RepoID: opts.repo, Query: strings.Join(args, " "), Mode: opts.searchMode, Kind: opts.kind, Provenance: opts.provenance, Limit: opts.limit, Offset: opts.offset})
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
		if err := validateSyncCommentSurface(opts); err != nil {
			return writeError(stderr, opts.format, err)
		}
		if err := validateSyncTargetRouting(opts); err != nil {
			return writeError(stderr, opts.format, err)
		}
		if opts.daemon || opts.detach {
			if opts.id != "" || opts.input != "" {
				return writeError(stderr, opts.format, service.ErrInvalidQuery{Field: "sync", Message: "daemon sync supports collection selectors only; omit --id/--input"})
			}
			manager := servicectl.Manager{Source: deps.Source, BinaryPath: os.Args[0], Version: buildinfo.Current().Version, Commit: buildinfo.Current().Commit}
			client, clientErr := manager.Client()
			if clientErr != nil {
				return writeError(stderr, opts.format, clientErr)
			}
			var job servicectl.Job
			if err := client.Call(ctx, "Jobs.StartSync", syncJobRequest(opts), &job); err != nil {
				return writeError(stderr, opts.format, err)
			}
			if opts.detach {
				return render(stdout, opts.format, job, renderServiceJobText)
			}
			return attachSyncJob(ctx, client, job.ID, opts, stdout, stderr)
		}
		if syncSingleRecordRequested(opts) {
			if opts.input != "" && (opts.prComments || opts.comments) && syncRemoteAliasSurface(opts.input) == "pull_request" {
				started := time.Now().UTC()
				result, err := svc.BulkSyncPRComments(ctx, service.BulkSyncRequest{RepoID: opts.repo, RemoteAlias: opts.input, IdempotencyKey: opts.idempotencyKey})
				return renderSyncResources(stdout, stderr, opts.format, opts.details, result, err, plan, started)
			}
			result, err := svc.SyncToCache(ctx, service.SyncRequest{RepoID: opts.repo, StableID: opts.id, RemoteAlias: opts.input, IdempotencyKey: opts.idempotencyKey})
			if err != nil {
				return writeError(stderr, opts.format, err)
			}
			return render(stdout, opts.format, result, renderSyncText)
		}
		if opts.issues || opts.wiki || opts.pulls || opts.comments || opts.issueComments || opts.prComments || (opts.id == "" && opts.input == "") {
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
			if !opts.issues && !opts.wiki && !opts.pulls && !opts.comments && !opts.issueComments && !opts.prComments {
				result, syncErr = svc.BulkSyncAll(ctx, req)
				stopProgress()
				return renderSyncResources(stdout, stderr, opts.format, opts.details, result, syncErr, plan, started)
			}
			aggregate := &service.SyncResourcesResult{Results: []service.SyncResult{}, Failures: []service.ResourceError{}}
			admissionFailed := false
			runBulk := func(fn func(context.Context, service.BulkSyncRequest) (*service.SyncResourcesResult, error)) {
				if admissionFailed {
					return
				}
				part, err := fn(ctx, req)
				mergeSyncResources(aggregate, part)
				if err != nil {
					var contention cache.ErrLockContention
					if part == nil && errors.As(err, &contention) {
						syncErr = err
						admissionFailed = true
						return
					}
					syncErr = mergeSyncError(syncErr, aggregate, err)
				}
			}
			if opts.issues {
				runBulk(svc.BulkSyncIssues)
			}
			if opts.issueComments {
				runBulk(svc.BulkSyncIssueComments)
			}
			if opts.wiki {
				runBulk(svc.BulkSyncWiki)
			}
			if opts.pulls {
				runBulk(svc.BulkSyncPullRequests)
			}
			if opts.comments || opts.prComments {
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
		return writeError(stderr, opts.format, service.ErrInvalidQuery{Field: "sync target", Message: "stable id or remote alias is required"})
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
			apiBaseURL := repositoryAPIBaseURL(opts.apiBaseURL, deps.Source)
			result, err := svc.AddRepository(ctx, service.AddRepositoryRequest{RepoID: opts.repo, Owner: opts.owner, Name: opts.name, APIBaseURL: apiBaseURL, Scopes: []string{opts.scopes}, DisplayName: opts.displayName, Aliases: []string(opts.alias)})
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
	case "update-pr":
		return dispatchWrite(ctx, svc.UpdatePR, command, opts, stdout, stderr, plan)
	case "merge-pr", "merge-mr":
		return dispatchWrite(ctx, svc.MergePR, "merge-pr", opts, stdout, stderr, plan)
	case "milestones":
		result, err := svc.ListMilestones(ctx, service.MilestoneListRequest{RepoID: opts.repo, Repo: opts.repo, State: opts.state, PerPage: opts.perPage})
		if err != nil {
			return writeError(stderr, opts.format, err)
		}
		return render(stdout, opts.format, result, renderMilestonesText)
	case "list-push-mirrors", "push-mirrors":
		result, err := svc.ListPushRemoteMirrors(ctx, service.PushMirrorListRequest{RepoID: opts.repo, Repo: opts.repo})
		if err != nil {
			return writeError(stderr, opts.format, err)
		}
		return render(stdout, opts.format, result, renderPushMirrorsText)
	case "trigger-push-mirror":
		opts.id = opts.mirrorID
		return dispatchWrite(ctx, svc.TriggerPushRemoteMirror, command, opts, stdout, stderr, plan)
	case "wait-push-mirror":
		after, err := parseOptionalRFC3339(opts.after)
		if err != nil {
			return writeError(stderr, opts.format, err)
		}
		result, err := svc.WaitPushRemoteMirror(ctx, service.PushMirrorWaitRequest{RepoID: opts.repo, Repo: opts.repo, MirrorID: opts.mirrorID, After: after, TimeoutSeconds: opts.timeoutSeconds})
		if err != nil {
			return writeError(stderr, opts.format, err)
		}
		return render(stdout, opts.format, result, renderPushMirrorWaitText)
	case "create-milestone":
		return dispatchWrite(ctx, svc.CreateMilestone, command, opts, stdout, stderr, plan)
	case "update-milestone":
		return dispatchWrite(ctx, svc.UpdateMilestone, command, opts, stdout, stderr, plan)
	case "set-issue-milestone":
		return dispatchWrite(ctx, svc.SetIssueMilestone, command, opts, stdout, stderr, plan)
	case "clear-issue-milestone":
		return dispatchWrite(ctx, svc.ClearIssueMilestone, command, opts, stdout, stderr, plan)
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
	case "reply-pr-review-comment":
		return dispatchWrite(ctx, svc.ReplyPRReviewComment, command, opts, stdout, stderr, plan)
	case "update-comment":
		return dispatchWrite(ctx, svc.UpdateComment, command, opts, stdout, stderr, plan)
	case "add-label":
		return dispatchWrite(ctx, svc.AddLabel, command, opts, stdout, stderr, plan)
	case "publish-release":
		return dispatchPublishRelease(ctx, svc, opts, stdout, stderr, plan)
	case "feedback":
		return dispatchFeedback(ctx, svc, args, opts, stdout, stderr, plan)
	default:
		return writeError(stderr, opts.format, service.ErrInvalidQuery{Field: "command", Message: command + " is not a query command"})
	}
}

func dispatchFeedback(ctx context.Context, svc queryService, args []string, opts options, stdout io.Writer, stderr io.Writer, plan startupPlan) int {
	sub, ok := firstArg(args)
	if !ok || (sub != "prepare" && sub != "submit") {
		return writeError(stderr, opts.format, service.ErrInvalidQuery{Field: "feedback", Message: "subcommand must be prepare or submit"})
	}
	draft, err := feedbackDraftFromOptions(opts)
	if err != nil {
		return writeError(stderr, opts.format, err)
	}
	if sub == "prepare" || opts.dryRun {
		if opts.live && sub == "prepare" {
			return writeError(stderr, opts.format, service.ErrInvalidQuery{Field: "write_mode", Message: "feedback prepare is read-only; omit --live"})
		}
		result, err := svc.PrepareFeedback(ctx, draft)
		if err != nil {
			return writeError(stderr, opts.format, err)
		}
		return render(stdout, opts.format, result, renderFeedbackPreparedText)
	}
	if !opts.live {
		return writeError(stderr, opts.format, service.ErrInvalidQuery{Field: "write_mode", Message: "feedback submit requires --live or --dry-run"})
	}
	if strings.TrimSpace(opts.idempotencyKey) == "" {
		return writeError(stderr, opts.format, service.ErrInvalidQuery{Field: "idempotency_key", Message: "feedback submit requires --idempotency-key"})
	}
	result, err := svc.SubmitFeedback(ctx, service.SubmitFeedbackRequest{Draft: draft, Mode: service.WriteModeLive, IdempotencyKey: opts.idempotencyKey})
	if err != nil {
		return writeCommandError(stderr, opts.format, plan, err)
	}
	return render(stdout, opts.format, result, renderFeedbackSubmissionText)
}

func feedbackDraftFromOptions(opts options) (feedback.Draft, error) {
	var draft feedback.Draft
	if strings.TrimSpace(opts.input) != "" {
		var data []byte
		var err error
		if opts.input == "-" {
			data, err = io.ReadAll(os.Stdin)
		} else {
			data, err = os.ReadFile(opts.input)
		}
		if err != nil {
			return draft, service.ErrInvalidQuery{Field: "input", Message: err.Error()}
		}
		if err := json.Unmarshal(data, &draft); err != nil {
			return draft, service.ErrInvalidQuery{Field: "input", Message: "feedback input must be a JSON object: " + err.Error()}
		}
	}
	setIfNotEmpty := func(value string, target *string) {
		if strings.TrimSpace(value) != "" {
			*target = value
		}
	}
	setIfNotEmpty(opts.title, &draft.Summary)
	setIfNotEmpty(opts.category, &draft.Category)
	setIfNotEmpty(opts.surface, &draft.Surface)
	setIfNotEmpty(opts.reporterType, &draft.ReporterType)
	setIfNotEmpty(opts.observed, &draft.Observed)
	setIfNotEmpty(opts.expected, &draft.Expected)
	setIfNotEmpty(opts.impact, &draft.Impact)
	setIfNotEmpty(opts.fallbackUsed, &draft.FallbackUsed)
	setIfNotEmpty(opts.workaround, &draft.Workaround)
	setIfNotEmpty(opts.relatedTask, &draft.RelatedTask)
	setIfNotEmpty(opts.acceptanceSignal, &draft.AcceptanceSignal)
	setIfNotEmpty(opts.proposal, &draft.Proposal)
	setIfNotEmpty(opts.toolName, &draft.ToolName)
	setIfNotEmpty(opts.errorCode, &draft.ErrorCode)
	setIfNotEmpty(opts.failureClass, &draft.FailureClass)
	setIfNotEmpty(opts.correlationID, &draft.CorrelationID)
	setIfNotEmpty(opts.jobID, &draft.JobID)
	setIfNotEmpty(opts.duplicateOverride, &draft.DuplicateOverride)
	if len(opts.reproductionSteps) > 0 {
		draft.ReproductionSteps = append([]string(nil), opts.reproductionSteps...)
	}
	if len(opts.evidence) > 0 {
		draft.Evidence = append([]string(nil), opts.evidence...)
	}
	return draft, nil
}

func renderFeedbackPreparedText(w io.Writer, result feedback.PreparedReport) {
	fmt.Fprintf(w, "status: %s\n", result.Status)
	fmt.Fprintf(w, "sink: %s\n", result.Sink)
	fmt.Fprintf(w, "repo_id: %s\n", result.RepoID)
	fmt.Fprintf(w, "fingerprint: %s\n", result.Fingerprint)
	fmt.Fprintf(w, "dedupe_decision: %s\n", result.DedupeDecision)
	fmt.Fprintf(w, "title: %s\n", result.Title)
	if result.Remediation != "" {
		fmt.Fprintf(w, "remediation: %s\n", result.Remediation)
	}
	if len(result.Candidates) > 0 {
		fmt.Fprintln(w, "candidates:")
		for _, candidate := range result.Candidates {
			fmt.Fprintf(w, "- #%d score=%.2f %s\n", candidate.Number, candidate.Score, candidate.Title)
		}
	}
	fmt.Fprintln(w, "\n--- preview ---")
	fmt.Fprintln(w, result.Body)
}

func renderFeedbackSubmissionText(w io.Writer, result feedback.SubmissionResult) {
	fmt.Fprintf(w, "status: %s\n", result.Status)
	fmt.Fprintf(w, "sink: %s\n", result.Sink)
	fmt.Fprintf(w, "fingerprint: %s\n", result.Fingerprint)
	fmt.Fprintf(w, "dedupe_decision: %s\n", result.DedupeDecision)
	if result.TicketNumber > 0 {
		fmt.Fprintf(w, "ticket: #%d\n", result.TicketNumber)
	}
	if result.TicketURL != "" {
		fmt.Fprintf(w, "url: %s\n", result.TicketURL)
	}
	if result.Remediation != "" {
		fmt.Fprintf(w, "remediation: %s\n", result.Remediation)
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
	if opts.dryRun && opts.bodyInput != nil {
		result.BodyInput = opts.bodyInput
	}
	return render(stdout, opts.format, result, renderWriteText)
}

func resolveMarkdownBodyInput(command string, opts options, stdin io.Reader) (options, error) {
	supported := command == "create-issue" || command == "update-issue" || command == "add-comment" || command == "update-comment"
	if !supported {
		if opts.bodyFileSet || opts.allowLiteralBackslashN {
			return opts, service.ErrInvalidQuery{Field: "body_input", Message: "--body-file and --allow-literal-backslash-n are supported by create-issue, update-issue, add-comment, and update-comment"}
		}
		return opts, nil
	}
	if opts.bodySet && opts.bodyFileSet {
		return opts, service.ErrInvalidQuery{Field: "body_input", Message: "--body and --body-file are mutually exclusive"}
	}
	if opts.bodyFileSet && strings.TrimSpace(opts.bodyFile) == "" {
		return opts, service.ErrInvalidQuery{Field: "body_file", Message: "--body-file requires a path or - for stdin"}
	}
	if opts.allowLiteralBackslashN && !opts.bodySet {
		return opts, service.ErrInvalidQuery{Field: "body_input", Message: "--allow-literal-backslash-n requires an inline --body"}
	}

	source := ""
	body := opts.body
	if opts.bodyFileSet {
		source = "file"
		var reader io.Reader
		var closeReader func() error
		if opts.bodyFile == "-" {
			source = "stdin"
			if stdin == nil {
				return opts, service.ErrInvalidQuery{Field: "body_file", Message: "--body-file - requires stdin"}
			}
			reader = stdin
		} else {
			file, err := os.Open(opts.bodyFile)
			if err != nil {
				return opts, service.ErrInvalidQuery{Field: "body_file", Message: fmt.Sprintf("cannot read body file: %v", err)}
			}
			reader = file
			closeReader = file.Close
		}
		data, err := io.ReadAll(io.LimitReader(reader, maxMarkdownBodyBytes+1))
		if closeReader != nil {
			closeErr := closeReader()
			if err == nil {
				err = closeErr
			}
		}
		if err != nil {
			return opts, service.ErrInvalidQuery{Field: "body_file", Message: fmt.Sprintf("cannot read body input: %v", err)}
		}
		if int64(len(data)) > maxMarkdownBodyBytes {
			return opts, service.ErrInvalidQuery{Field: "body_file", Message: fmt.Sprintf("body input exceeds %d bytes", maxMarkdownBodyBytes)}
		}
		if !utf8.Valid(data) {
			return opts, service.ErrInvalidQuery{Field: "body_file", Message: "body input must be valid UTF-8"}
		}
		body = string(data)
	} else if opts.bodySet {
		source = "inline"
	}
	if source == "" {
		return opts, nil
	}
	if source != "inline" && len(body) == 0 {
		return opts, service.ErrInvalidQuery{Field: "body_file", Message: "body input is empty"}
	}

	normalizedCarriageReturns := strings.Contains(body, "\r")
	body = strings.ReplaceAll(body, "\r\n", "\n")
	body = strings.ReplaceAll(body, "\r", "\n")
	actualNewlines := strings.Count(body, "\n")
	literalBackslashNs := strings.Count(body, `\n`)
	if source == "inline" && actualNewlines == 0 && literalBackslashNs >= 2 && !opts.allowLiteralBackslashN {
		return opts, service.ErrInvalidQuery{
			Field:   "body",
			Message: "inline body contains multiple literal \\n sequences and no real newlines; use --body-file PATH, --body-file -, or --allow-literal-backslash-n if the text is intentional",
		}
	}
	opts.body = body
	opts.bodyInput = &service.WriteBodyInputMetadata{
		Source:                    source,
		ByteCount:                 len([]byte(body)),
		ActualNewlineCount:        actualNewlines,
		LiteralBackslashNCount:    literalBackslashNs,
		CarriageReturnsNormalized: normalizedCarriageReturns,
		TrailingNewlinePreserved:  strings.HasSuffix(body, "\n"),
	}
	return opts, nil
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
	if command == "trigger-push-mirror" && !opts.live && !opts.dryRun {
		return service.ErrInvalidQuery{Field: "write_mode", Message: "trigger-push-mirror requires --live or --dry-run"}
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

func syncJobRequest(opts options) servicectl.StartSyncJobRequest {
	mode := string(gitcode.ProviderModeLive)
	if opts.offline || opts.fixture {
		mode = string(gitcode.ProviderModeFixture)
	}
	return servicectl.StartSyncJobRequest{
		RepoID:         opts.repo,
		ProviderMode:   mode,
		CachePath:      opts.cachePath,
		Issues:         opts.issues,
		Wiki:           opts.wiki,
		Pulls:          opts.pulls,
		Comments:       opts.comments,
		IssueComments:  opts.issueComments,
		PRComments:     opts.prComments,
		IdempotencyKey: opts.idempotencyKey,
		MaxPages:       opts.maxPages,
		MaxRecords:     opts.maxRecords,
		PerPage:        opts.perPage,
	}
}

func syncSingleRecordRequested(opts options) bool {
	return opts.id != "" || opts.input != ""
}

func validateSyncTargetRouting(opts options) error {
	if opts.id == "" && opts.input == "" {
		return nil
	}
	if opts.id != "" && opts.input != "" {
		return service.ErrInvalidQuery{Field: "sync target", Message: "--id and --input are mutually exclusive"}
	}
	if opts.maxPages > 0 || opts.maxRecords > 0 || opts.perPage > 0 {
		return service.ErrInvalidQuery{Field: "sync bounds", Message: "--max-pages, --max-records, and --per-page apply to collection sync only; omit them for an exact --id/--input sync"}
	}

	selected := ""
	selectedCount := 0
	if opts.issues {
		selected, selectedCount = "issue", selectedCount+1
	}
	if opts.wiki {
		selected, selectedCount = "wiki", selectedCount+1
	}
	if opts.pulls {
		selected, selectedCount = "pull_request", selectedCount+1
	}
	if selectedCount == 0 {
		return nil
	}
	if opts.id != "" {
		return service.ErrInvalidQuery{Field: "sync target", Message: "collection selectors cannot be combined with --id; omit the selector for an exact stable-id sync"}
	}
	if selectedCount > 1 {
		return service.ErrInvalidQuery{Field: "sync target", Message: "an exact --input target can have at most one matching collection selector"}
	}
	surface := syncRemoteAliasSurface(opts.input)
	if surface == "" {
		return service.ErrInvalidQuery{Field: "sync target", Message: "collection selectors require a matching issue:N, wiki:SLUG, or pr:N --input alias"}
	}
	if selected != surface {
		return service.ErrInvalidQuery{Field: "sync target", Message: fmt.Sprintf("--input targets %s but the selected collection is %s", surface, selected)}
	}
	return nil
}

func validateSyncCommentSurface(opts options) error {
	if !syncCommentSelectorUsed(opts) {
		return nil
	}
	if opts.id != "" {
		return service.ErrInvalidQuery{Field: "comments", Message: "comment sync with --id is ambiguous; use --input issue:N for issue comments or --pr-comments --input pr:N for pull request comments"}
	}
	if opts.input == "" {
		return nil
	}
	if opts.issues || opts.wiki || opts.pulls {
		return service.ErrInvalidQuery{Field: "comments", Message: "comment sync with --input cannot be combined with collection selectors; use --input issue:N alone or run a collection sync without --input"}
	}
	surface := syncRemoteAliasSurface(opts.input)
	switch surface {
	case "issue":
		if opts.prComments {
			return service.ErrInvalidQuery{Field: "comments", Message: "--pr-comments cannot target issue aliases; use --issue-comments or --comments with issue:N"}
		}
		return nil
	case "pull_request":
		if opts.issueComments {
			return service.ErrInvalidQuery{Field: "comments", Message: "--issue-comments cannot target pull request aliases; use --pr-comments with pr:N"}
		}
		return nil
	default:
		return service.ErrInvalidQuery{Field: "comments", Message: "comment sync with --input supports issue:N or pr:N"}
	}
}

func syncCommentSelectorUsed(opts options) bool {
	return opts.comments || opts.issueComments || opts.prComments
}

func syncRemoteAliasSurface(alias string) string {
	remoteType, _, ok := strings.Cut(strings.TrimSpace(alias), ":")
	if !ok {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(remoteType)) {
	case "issue", "issues":
		return "issue"
	case "wiki", "page", "remote":
		return "wiki"
	case "pull_request", "pull", "pulls", "pr":
		return "pull_request"
	default:
		return ""
	}
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
	Started         time.Time
	Frame           int
	Type            string
	Collection      string
	Phase           string
	Page            int
	RecordsFetched  int
	RecordsListed   int
	RecordsDeferred int
	RateLimitState  string
	RetryAfter      string
	Endpoint        string
	Message         string
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
	s.RecordsDeferred += ev.RecordsDeferred
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
	if state.RecordsDeferred > 0 {
		fmt.Fprintf(w, " %d def", state.RecordsDeferred)
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
	if ev.RecordsDeferred > 0 {
		fmt.Fprintf(w, " deferred=%d", ev.RecordsDeferred)
	}
	if ev.RecordsFailed > 0 {
		fmt.Fprintf(w, " failed=%d", ev.RecordsFailed)
	}
	if ev.RateLimitState != "" {
		fmt.Fprintf(w, " rate_limit=%s", ev.RateLimitState)
	}
	if ev.RateLimitRPS != "" {
		fmt.Fprintf(w, " rps=%s", ev.RateLimitRPS)
	}
	if ev.RateLimitBurst > 0 {
		fmt.Fprintf(w, " burst=%d", ev.RateLimitBurst)
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
	if src.IssueComments != nil {
		queue := *src.IssueComments
		dst.IssueComments = &queue
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
	Status             string                            `json:"status"`
	FailureClass       string                            `json:"failure_class,omitempty"`
	SuccessCount       int                               `json:"success_count"`
	FailureCount       int                               `json:"failure_count"`
	Counts             service.SyncCounts                `json:"counts"`
	PagesListed        int                               `json:"pages_listed,omitempty"`
	RecordsListed      int                               `json:"records_listed,omitempty"`
	SkippedByWatermark int                               `json:"skipped_by_watermark,omitempty"`
	StopReason         string                            `json:"stop_reason,omitempty"`
	Ordering           string                            `json:"ordering,omitempty"`
	TraversalStatus    string                            `json:"traversal_status,omitempty"`
	WatermarkStatus    string                            `json:"watermark_status,omitempty"`
	WatermarkReason    string                            `json:"watermark_reason,omitempty"`
	IssueComments      *service.IssueCommentQueueSummary `json:"issue_comments,omitempty"`
	ZeroDeltaCount     int                               `json:"zero_delta_count,omitempty"`
	Diagnostic         service.SyncDiagnostic            `json:"diagnostic,omitempty"`
	TotalRequested     int                               `json:"total_requested,omitempty"`
	FailureGroups      []syncFailureGroupSummary         `json:"failure_groups,omitempty"`
	Elapsed            string                            `json:"elapsed"`
	StartedAt          time.Time                         `json:"started_at"`
	CompletedAt        time.Time                         `json:"completed_at"`
}

type syncFailureGroupSummary struct {
	RemoteType   string `json:"remote_type"`
	FailureClass string `json:"failure_class,omitempty"`
	Endpoint     string `json:"endpoint,omitempty"`
	StatusCode   int    `json:"status_code,omitempty"`
	Count        int    `json:"count"`
}

type syncStatusCompactSummary struct {
	RepoID              string                            `json:"repo_id"`
	FreshCount          int                               `json:"fresh_count"`
	StaleCount          int                               `json:"stale_count"`
	UnknownCount        int                               `json:"unknown_count"`
	MissingRemoteCount  int                               `json:"missing_remote_count"`
	ResultCount         int                               `json:"result_count"`
	LastSyncAt          time.Time                         `json:"last_sync_at"`
	LastSyncStartedAt   time.Time                         `json:"last_sync_started_at"`
	LastSyncCompletedAt time.Time                         `json:"last_sync_completed_at"`
	ZeroDelta           bool                              `json:"zero_delta"`
	CacheEmpty          bool                              `json:"cache_empty"`
	Limit               int                               `json:"limit"`
	Offset              int                               `json:"offset"`
	IssueComments       *service.IssueCommentQueueSummary `json:"issue_comments,omitempty"`
	Warnings            []string                          `json:"warnings,omitempty"`
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
		IssueComments:       result.IssueComments,
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
			canonicalFailureClass := syncPartialFailureClass(plan, partial)
			if result != nil && len(result.Results) > 0 {
				if format == "json" {
					if details {
						_ = renderJSON(stdout, result)
					} else {
						summary := syncResourcesSummary(result, partial, started)
						summary.FailureClass = canonicalFailureClass
						_ = renderJSON(stdout, summary)
					}
				} else {
					if details {
						for _, r := range result.Results {
							renderSyncText(stdout, r)
						}
					} else {
						summary := syncResourcesSummary(result, partial, started)
						summary.FailureClass = canonicalFailureClass
						renderSyncResourcesSummaryText(stdout, summary)
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
			if format != "json" && details && canonicalFailureClass != "" {
				fmt.Fprintf(stderr, "failure_class: %s\n", canonicalFailureClass)
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

func syncPartialFailureClass(plan startupPlan, err error) string {
	if plan.ProviderMode == "live-http" {
		return string(diagnostics.Classify(err, diagnosticContext(plan, err)).Code)
	}
	return failureClass(err)
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
		summary.IssueComments = result.IssueComments
		for _, item := range result.Results {
			summary.Counts.Fetched += item.Counts.Fetched
			summary.Counts.Skipped += item.Counts.Skipped
			summary.Counts.Updated += item.Counts.Updated
			summary.Counts.Conflicts += item.Counts.Conflicts
			summary.Counts.Inserted += item.Counts.Inserted
			summary.Counts.Listed += item.Counts.Listed
			summary.Counts.FetchedDetail += item.Counts.FetchedDetail
			summary.Counts.SkippedByRevision += item.Counts.SkippedByRevision
			summary.Counts.Deferred += item.Counts.Deferred
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
	fmt.Fprintf(w, "sync: %s success_count=%d failure_count=%d fetched=%d updated=%d inserted=%d skipped=%d conflicts=%d listed=%d fetched_detail=%d skipped_by_revision=%d deferred=%d zero_delta=%d elapsed=%s",
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
		summary.Counts.Deferred,
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
	if summary.IssueComments != nil {
		fmt.Fprintf(w, " issue_comments_phase=%s issue_comments_pending=%d issue_comments_deferred=%d issue_comments_complete=%d", summary.IssueComments.Phase, summary.IssueComments.Pending, summary.IssueComments.Deferred, summary.IssueComments.Complete)
		if summary.IssueComments.Strategy != "" {
			fmt.Fprintf(w, " issue_comments_strategy=%s", summary.IssueComments.Strategy)
		}
		if summary.IssueComments.FallbackReason != "" {
			fmt.Fprintf(w, " issue_comments_fallback=%s", summary.IssueComments.FallbackReason)
		}
		if summary.IssueComments.AggregateRequests > 0 || summary.IssueComments.CommentsListed > 0 || summary.IssueComments.ParentRequestsAvoided > 0 || summary.IssueComments.Unreconciled > 0 {
			fmt.Fprintf(w, " issue_comments_aggregate_requests=%d issue_comments_listed=%d issue_comment_parent_requests_avoided=%d issue_comments_unreconciled=%d", summary.IssueComments.AggregateRequests, summary.IssueComments.CommentsListed, summary.IssueComments.ParentRequestsAvoided, summary.IssueComments.Unreconciled)
		}
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
	if summary.FailureClass != "" {
		fmt.Fprintf(w, "failure_class: %s\n", summary.FailureClass)
	}
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
	return service.WriteCommandRequest{RepoID: opts.repo, Repo: opts.repo, Mode: mode, ID: opts.id, IssueID: opts.issueID, Number: opts.number, CommentID: opts.commentID, DiscussionID: opts.discussionID, ParentID: opts.parentID, Slug: opts.slug, Path: opts.path, Line: opts.line, StartLine: opts.startLine, EndLine: opts.endLine, Position: opts.position, Sha: opts.sha, Title: opts.title, Body: opts.body, Description: opts.description, DueOn: opts.dueOn, Milestone: opts.milestone, ClearMilestone: opts.clearMilestone, Head: opts.head, Base: opts.base, State: opts.state, Label: opts.label, Labels: labels, Strategy: opts.strategy, IdempotencyKey: opts.idempotencyKey}
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
	fmt.Fprintf(w, "requested_mode: %s effective_mode: %s rag_state: %s\n", result.RequestedMode, result.EffectiveMode, result.RAGState)
	if result.FallbackReason != "" {
		fmt.Fprintf(w, "fallback_reason: %s\n", result.FallbackReason)
	}
	fmt.Fprintf(w, "coverage: %d/%d embedded, %d missing, %d stale\n", result.Coverage.EmbeddedChunks, result.Coverage.EligibleChunks, result.Coverage.MissingChunks, result.Coverage.StaleChunks)
	for _, item := range result.Results {
		line := 0
		if item.LineStart != nil {
			line = *item.LineStart
		}
		fmt.Fprintf(w, "%d %.6f %s %s %s:%d:%s\n", item.Rank, item.Match.FusionScore, item.RepoID, item.ID, item.Path, line, item.Snippet)
	}
}

func renderListText(w io.Writer, result service.ListSourcesResult) {
	for _, item := range result.Results {
		if item.IssueNumber > 0 {
			fmt.Fprintf(w, "%s stable_source_id=%s issue_number=%d %s %s %s %s\n", item.RepoID, item.StableSourceID, item.IssueNumber, item.Kind, item.Status, item.Path, item.Title)
			continue
		}
		fmt.Fprintf(w, "%s stable_source_id=%s %s %s %s %s\n", item.RepoID, item.StableSourceID, item.Kind, item.Status, item.Path, item.Title)
	}
}

func renderGetText(w io.Writer, result service.SourceRecord) {
	fmt.Fprintf(w, "repo_id: %s\nid: %s\nstable_source_id: %s\n", result.RepoID, result.ID, result.StableSourceID)
	if result.IssueNumber > 0 {
		fmt.Fprintf(w, "issue_number: %d\n", result.IssueNumber)
	}
	fmt.Fprintf(w, "kind: %s\npath: %s\nremote_alias: %s\ntitle: %s\nstatus: %s\nbody:\n%s\n", result.Kind, result.Path, result.RemoteAlias, result.Title, result.Status, result.Body)
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

func renderResetLiveCacheText(w io.Writer, result service.ResetLiveCacheResult) {
	fmt.Fprintf(w, "repo_id: %s\nreset: %s\n", result.RepoID, result.Reset)
}

func renderSyncStatusText(w io.Writer, result service.SyncStatusResult) {
	fmt.Fprintf(w, "%s %s %s %s %s %s\n", result.RepoID, result.SourceID, result.Status, result.RemoteType, result.RemoteID, result.LastFetchedAt.Format(time.RFC3339))
	if result.IssueComments != nil {
		fmt.Fprintf(w, "issue_comments: status=%s expected=%d attempts=%d last_error=%s retry_after=%s\n", result.IssueComments.Status, result.IssueComments.ExpectedCount, result.IssueComments.Attempts, result.IssueComments.LastErrorClass, result.IssueComments.RetryAfter)
	}
}

func renderSyncStatusSummaryText(w io.Writer, result service.SyncStatusSummaryResult) {
	fmt.Fprintf(w, "repo_id: %s\nfresh_count: %d\nstale_count: %d\ncache_empty: %t\nzero_delta: %t\n", result.RepoID, result.FreshCount, result.StaleCount, result.CacheEmpty, result.ZeroDelta)
	if result.IssueComments != nil {
		fmt.Fprintf(w, "issue_comments: pending=%d deferred=%d complete=%d total=%d\n", result.IssueComments.Pending, result.IssueComments.Deferred, result.IssueComments.Complete, result.IssueComments.Total)
	}
}

func renderRecentText(w io.Writer, result service.RecentChangesResult) {
	for _, item := range result.Results {
		if item.IssueNumber > 0 {
			fmt.Fprintf(w, "%s %s stable_source_id=%s issue_number=%d %s %s\n", item.UpdatedAt.UTC().Format(time.RFC3339), item.RepoID, item.StableSourceID, item.IssueNumber, item.Path, item.Title)
			continue
		}
		fmt.Fprintf(w, "%s %s stable_source_id=%s %s %s\n", item.UpdatedAt.UTC().Format(time.RFC3339), item.RepoID, item.StableSourceID, item.Path, item.Title)
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
	fmt.Fprintf(w, "repo_id: %s\nwal_capable: %t\njournal_mode: %s\nrecords: %d\ncomments: %d\nidentity_aliases: %d\nsync_events: %d\naudit_rows: %d\nsnapshots: %d\nsnapshot_chunks: %d\nchunks: %d\nremote_revisions: %d\nrag_namespaces: %d\nrag_embeddings: %d\nrag_index_runs: %d\n", result.RepoID, result.WALCapable, result.JournalMode, result.Records, result.Comments, result.IdentityAliases, result.SyncEvents, result.AuditRows, result.Snapshots, result.SnapshotChunks, result.Chunks, result.RemoteRevisions, result.RAGNamespaces, result.RAGEmbeddings, result.RAGIndexRuns)
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
	if result.BodyInput != nil {
		fmt.Fprintf(
			w,
			"body_input: source=%s bytes=%d actual_newlines=%d literal_backslash_n=%d carriage_returns_normalized=%t trailing_newline_preserved=%t\n",
			result.BodyInput.Source,
			result.BodyInput.ByteCount,
			result.BodyInput.ActualNewlineCount,
			result.BodyInput.LiteralBackslashNCount,
			result.BodyInput.CarriageReturnsNormalized,
			result.BodyInput.TrailingNewlinePreserved,
		)
	}
	if result.StableSourceID != "" || result.IssueNumber > 0 {
		fmt.Fprintf(w, "issue_identity: stable_source_id=%s issue_number=%d\n", result.StableSourceID, result.IssueNumber)
	}
	if result.Milestone != nil {
		fmt.Fprintf(w, "milestone: id=%s remote_id=%s title=%s cleared=%t\n", result.Milestone.ID, result.Milestone.RemoteID, result.Milestone.Title, result.Milestone.Cleared)
	}
	if result.PushMirror != nil {
		fmt.Fprintf(w, "push_mirror: mirror_id=%s status=%s previous_status=%s readback_status=%s triggered_at=%s\n", result.PushMirror.MirrorID, result.PushMirror.Status, result.PushMirror.PreviousStatus, result.PushMirror.ReadbackStatus, result.PushMirror.TriggeredAt.UTC().Format(time.RFC3339Nano))
	}
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

func renderMilestonesText(w io.Writer, result service.MilestoneListResult) {
	fmt.Fprintf(w, "milestones: repo_id=%s count=%d evidence=%s\n", result.RepoID, result.Count, result.Evidence)
	for _, milestone := range result.Milestones {
		fmt.Fprintf(w, "%s %s state=%s due_on=%s title=%s\n", milestone.ID, milestone.RemoteID, milestone.State, milestone.DueOn, milestone.Title)
	}
}

func renderPushMirrorsText(w io.Writer, result service.PushMirrorListResult) {
	fmt.Fprintf(w, "push-mirrors: repo_id=%s count=%d evidence=%s\n", result.RepoID, result.Count, result.Evidence)
	for _, mirror := range result.Mirrors {
		fmt.Fprintf(w, "%s %s status=%s failures=%d private=%t force=%t destination=%s\n", mirror.ID, mirror.RemoteID, mirror.UpdateStatus, mirror.NumberOfFailures, mirror.Private, mirror.Force, mirror.Destination)
	}
}

func renderPushMirrorWaitText(w io.Writer, result service.PushMirrorWaitResult) {
	fmt.Fprintf(w, "wait-push-mirror: repo_id=%s mirror_id=%s status=%s update_status=%s failures=%d last_update_at=%s last_successful_update_at=%s evidence=%s\n", result.RepoID, result.MirrorID, result.Status, result.UpdateStatus, result.NumberOfFailures, optionalTimestamp(result.LastUpdateAt), optionalTimestamp(result.LastSuccessfulUpdateAt), result.Evidence)
}

func optionalTimestamp(value time.Time) string {
	if value.IsZero() {
		return "-"
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func parseOptionalRFC3339(raw string) (time.Time, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, service.ErrInvalidQuery{Field: "after", Message: "must be an RFC3339 timestamp"}
	}
	return parsed.UTC(), nil
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
	fmt.Fprintf(
		w,
		"repo_id: %s\nowner: %s\nname: %s\napi_base_url: %s\nscopes: %s\ndisplay_name: %s\naliases: %s\nbinding_state: %s\nalias_conflict_state: %s\ncache_state: %s\nindex_state: %s\nbinary_version: %s\nbinary_commit: %s\nbinary_build_date: %s\nbinary_version_source: %s\ncache_schema_version: %d\nexpected_cache_schema_version: %d\nissue_records: %d\nissue_comments: %d\n",
		result.RepoID,
		result.Owner,
		result.Name,
		result.APIBaseURL,
		joinRepositoryScopes(result.Scopes),
		result.DisplayName,
		strings.Join(result.Aliases, ","),
		result.BindingState,
		result.AliasConflictState,
		result.CacheState,
		result.IndexState,
		result.BinaryVersion,
		result.BinaryCommit,
		result.BinaryBuildDate,
		result.BinaryVersionSource,
		result.CacheSchemaVersion,
		result.ExpectedCacheSchemaVersion,
		result.IssueRecords,
		result.IssueComments,
	)
	fmt.Fprintf(w, "issue_comment_queue_state: %s\n", result.IssueCommentQueueState)
	if result.IssueCommentQueue != nil {
		fmt.Fprintf(
			w,
			"issue_comment_queue: pending=%d deferred=%d complete=%d total=%d\n",
			result.IssueCommentQueue.Pending,
			result.IssueCommentQueue.Deferred,
			result.IssueCommentQueue.Complete,
			result.IssueCommentQueue.Total,
		)
	}
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
	publicErr := err
	var lockErr cache.ErrLockContention
	if errors.As(err, &lockErr) {
		publicErr = lockErr
	}
	message := config.RedactDiagnostic(publicErr.Error(), config.OSSource{})
	var diagnostic diagnostics.Diagnostic
	if plan.ProviderMode == "live-http" {
		diagnostic = diagnostics.Classify(publicErr, diagnosticContext(plan, err))
		failureClass = string(diagnostic.Code)
		message = diagnostic.Message
	}
	if format == "json" {
		payload := map[string]any{"error": message, "exit_code": code, "failure_class": failureClass}
		addLockContentionFields(payload, err)
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
	if contention, ok := lockContentionDetails(err); ok {
		fmt.Fprintf(stderr, "cache_ref: %s\n", contention.CacheRef)
		if contention.Operation != "" {
			fmt.Fprintf(stderr, "operation: %s\n", contention.Operation)
		}
		if contention.RepoID != "" {
			fmt.Fprintf(stderr, "repo_id: %s\n", contention.RepoID)
		}
		if contention.StartedAt != "" {
			fmt.Fprintf(stderr, "started_at: %s\n", contention.StartedAt)
		}
		if contention.PID != 0 {
			fmt.Fprintf(stderr, "pid: %d\n", contention.PID)
		}
	}
	return code
}

type lockContentionOutput struct {
	CacheRef  string
	Operation string
	RepoID    string
	StartedAt string
	PID       int
}

func lockContentionDetails(err error) (lockContentionOutput, bool) {
	var contention cache.ErrLockContention
	if !errors.As(err, &contention) {
		return lockContentionOutput{}, false
	}
	details := lockContentionOutput{CacheRef: contention.PublicCacheRef(), Operation: contention.PublicOperation(), RepoID: contention.PublicRepoID()}
	if contention.PID > 0 {
		details.PID = contention.PID
	}
	if !contention.StartedAt.IsZero() {
		details.StartedAt = contention.StartedAt.UTC().Format(time.RFC3339Nano)
	}
	return details, true
}

func addLockContentionFields(payload map[string]any, err error) {
	details, ok := lockContentionDetails(err)
	if !ok {
		return
	}
	if details.CacheRef != "" {
		payload["cache_ref"] = details.CacheRef
	}
	if details.Operation != "" {
		payload["operation"] = details.Operation
	}
	if details.RepoID != "" {
		payload["repo_id"] = details.RepoID
	}
	if details.StartedAt != "" {
		payload["started_at"] = details.StartedAt
	}
	if details.PID != 0 {
		payload["pid"] = details.PID
	}
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
		ctx.HTTPAttempted = writeErr.Code == "write_unauthorized" || writeErr.Code == "write_network_unavailable" || writeErr.Code == "write_provider_error" || writeErr.Code == "write_conflict" || writeErr.Code == "write_ambiguous_remote" || writeErr.Code == "write_ambiguous_readback_failed" || writeErr.Code == "schema_decode" || writeErr.Code == "pr_review_anchor_mismatch" || writeErr.Code == "write_confirmation_incomplete" || writeErr.Code == "discussion_reply_unavailable"
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
		ctx.HTTPAttempted = syncErr.Mode == "live_auth_failure" || syncErr.Mode == "network_timeout" || syncErr.Mode == "rate_limited" || syncErr.Mode == "partial_response" || syncErr.Mode == "live_graph_invalid" || syncErr.Mode == "remote_identity_mismatch" || syncErr.Mode == "payload_too_large" || syncErr.Mode == "remote_not_found" || syncErr.Mode == "conflict" || syncErr.Mode == "remote_collision"
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
		for _, failure := range partialSync.Errors {
			switch failure.FailureClass {
			case "schema_decode", "partial_response", "malformed_response", "unexpected_content_type":
				ctx.HTTPAttempted = true
				ctx.SchemaDecodeFailure = true
				ctx.FailureSource = failure.FailureClass
			}
		}
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
	var mirrorSyncInProgress gitcode.ErrPushMirrorSyncInProgress
	if errors.As(err, &mirrorSyncInProgress) {
		ctx.HTTPAttempted = true
		ctx.HTTPStatus = http.StatusForbidden
		ctx.APIFailure = true
		ctx.RetryableProviderFailure = true
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
	var malformed gitcode.ErrMalformedJSON
	if errors.As(err, &malformed) {
		ctx.HTTPAttempted = true
		ctx.SchemaDecodeFailure = true
		ctx.MalformedSuccess = true
		ctx.FailureSource = "malformed_response"
	}
	var unexpectedContent gitcode.ErrUnexpectedContentType
	if errors.As(err, &unexpectedContent) {
		ctx.HTTPAttempted = true
		ctx.SchemaDecodeFailure = true
		ctx.MalformedSuccess = true
		ctx.FailureSource = "unexpected_content_type"
	}
	var schema *gitcode.ErrSchemaDecode
	if errors.As(err, &schema) {
		ctx.HTTPAttempted = true
		ctx.SchemaDecodeFailure = true
	}
	return ctx
}

func failureClass(err error) string {
	var parentMissing service.ErrParentPRNotCached
	if errors.As(err, &parentMissing) {
		return parentMissing.DiagnosticCode()
	}
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
	var schemaErr *cache.SchemaVersionError
	if errors.As(err, &schemaErr) {
		return "cache_schema_blocked"
	}
	var coded interface{ DiagnosticCode() string }
	if errors.As(err, &coded) && strings.TrimSpace(coded.DiagnosticCode()) != "" {
		return strings.TrimSpace(coded.DiagnosticCode())
	}
	if isStrictFinding(err) {
		return "validation_failed"
	}
	return "internal_error"
}

func exitCode(err error) int {
	var parentMissing service.ErrParentPRNotCached
	if errors.As(err, &parentMissing) {
		return 3
	}
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
	report, err := doctor.Build(ctx, doctor.Request{Version: buildinfo.Current().Version, Source: deps.Source, CredentialReporter: deps.CredentialReporter, CredentialStatus: cred, CachePath: cachePath, Live: plan.ProviderMode == "live-http", ProviderMode: plan.ProviderMode, MCPToolAccess: plan.MCPToolAccess, APIBaseURL: plan.APIBaseURL, RepoID: opts.repo, LiveBinding: plan.LiveRepositoryBinding, RAGRuntime: deps.RAGRuntime})
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
	CachePath         string `json:"cache_path"`
	FromVersion       int    `json:"from_version"`
	ToVersion         int    `json:"to_version"`
	Status            string `json:"status"`
	Applied           []int  `json:"applied,omitempty"`
	BackupPath        string `json:"backup_path,omitempty"`
	BackupVerified    bool   `json:"backup_verified"`
	IdentityPreserved bool   `json:"identity_preserved"`
	ServiceInstalled  bool   `json:"service_installed"`
	ServiceWasRunning bool   `json:"service_was_running"`
	ServiceQuiesced   bool   `json:"service_quiesced"`
	ServiceRestarted  bool   `json:"service_restarted"`
	DaemonVersion     string `json:"daemon_version,omitempty"`
	DaemonCommit      string `json:"daemon_commit,omitempty"`
	DaemonSchemaMin   int    `json:"daemon_schema_min,omitempty"`
	DaemonSchemaMax   int    `json:"daemon_schema_max,omitempty"`
	RecoveryState     string `json:"recovery_state,omitempty"`
	Remediation       string `json:"remediation,omitempty"`
}

type cacheMigrationRecovery struct {
	SchemaVersion     string `json:"schema_version"`
	TargetSchema      int    `json:"target_schema"`
	Phase             string `json:"phase"`
	ServiceInstalled  bool   `json:"service_installed"`
	ServiceWasRunning bool   `json:"service_was_running"`
	BackupPath        string `json:"backup_path,omitempty"`
	BackupVerified    bool   `json:"backup_verified"`
	IdentityPreserved bool   `json:"identity_preserved"`
}

const cacheMigrationRecoverySchema = "gitcode-mcp.cache-migration-recovery.v1"

type cacheMigrationReceipt struct {
	SchemaVersion       string    `json:"schema_version"`
	CacheUUID           string    `json:"cache_uuid"`
	TargetSchema        int       `json:"target_schema"`
	Phase               string    `json:"phase"`
	BackupVerified      bool      `json:"backup_verified"`
	IdentityPreserved   bool      `json:"identity_preserved"`
	TargetBinaryVersion string    `json:"target_binary_version"`
	TargetBinaryCommit  string    `json:"target_binary_commit,omitempty"`
	TargetSchemaMin     int       `json:"target_schema_min"`
	TargetSchemaMax     int       `json:"target_schema_max"`
	CompletedAt         time.Time `json:"completed_at"`
}

const cacheMigrationReceiptSchema = "gitcode-mcp.cache-migration-receipt.v1"

func cacheMigrationRecoveryPath(cachePath string) string {
	return cachePath + ".migration-recovery.json"
}

func cacheMigrationReceiptPath(cachePath string) string {
	return cachePath + ".migration-receipt.json"
}

func readCacheMigrationRecovery(cachePath string) (cacheMigrationRecovery, bool, error) {
	data, err := os.ReadFile(cacheMigrationRecoveryPath(cachePath))
	if errors.Is(err, os.ErrNotExist) {
		return cacheMigrationRecovery{}, false, nil
	}
	if err != nil {
		return cacheMigrationRecovery{}, false, fmt.Errorf("cache migration recovery intent cannot be read: %w", err)
	}
	var recovery cacheMigrationRecovery
	if err := json.Unmarshal(data, &recovery); err != nil {
		return cacheMigrationRecovery{}, false, fmt.Errorf("cache migration recovery intent is invalid: %w", err)
	}
	if recovery.SchemaVersion != cacheMigrationRecoverySchema || recovery.TargetSchema <= 0 {
		return cacheMigrationRecovery{}, false, errors.New("cache migration recovery intent has an unsupported schema")
	}
	return recovery, true, nil
}

func writeCacheMigrationRecovery(cachePath string, recovery cacheMigrationRecovery) error {
	recovery.SchemaVersion = cacheMigrationRecoverySchema
	data, err := json.MarshalIndent(recovery, "", "  ")
	if err != nil {
		return err
	}
	path := cacheMigrationRecoveryPath(cachePath)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("cache migration recovery intent cannot be written: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("cache migration recovery intent cannot be committed: %w", err)
	}
	return nil
}

func clearCacheMigrationRecovery(cachePath string) error {
	err := os.Remove(cacheMigrationRecoveryPath(cachePath))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func writeCacheMigrationReceipt(cachePath string, receipt cacheMigrationReceipt) error {
	receipt.SchemaVersion = cacheMigrationReceiptSchema
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return err
	}
	path := cacheMigrationReceiptPath(cachePath)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("cache migration receipt cannot be written: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("cache migration receipt cannot be committed: %w", err)
	}
	return nil
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
	apiBaseURL := repositoryAPIBaseURL(opts.apiBaseURL, deps.Source)
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

func repositoryAPIBaseURL(explicit string, src config.Source) string {
	if value := strings.TrimSpace(explicit); value != "" {
		return value
	}
	return defaultAPIBaseURL(src)
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

	inspection, err := cache.InspectCacheMigration(ctx, cachePath)
	if err != nil {
		return writeError(stderr, opts.format, err)
	}
	var migrationService cacheMigrationService = deps.MigrationService
	if migrationService == nil {
		migrationService = &servicectl.Manager{Source: deps.Source, BinaryPath: os.Args[0], Version: buildinfo.Current().Version, Commit: buildinfo.Current().Commit, RuntimeDir: eff.Config.Service.RuntimeDir}
	}
	serviceStatus, err := migrationService.Status()
	if err != nil {
		return writeError(stderr, opts.format, err)
	}
	recovery, recoveryPending, err := readCacheMigrationRecovery(cachePath)
	if err != nil {
		return writeError(stderr, opts.format, err)
	}
	if recoveryPending && recovery.TargetSchema != inspection.ToVersion {
		return writeError(stderr, opts.format, service.ErrInvalidQuery{Field: "migration-recovery", Message: "pending cache migration recovery targets a different schema; restore the recorded backup or use a compatible gitcode-mcp binary"})
	}
	mr := migrateCacheResult{
		CachePath: cachePath, FromVersion: inspection.FromVersion, ToVersion: inspection.ToVersion,
		ServiceInstalled: serviceStatus.Installed, ServiceWasRunning: serviceStatus.Running,
		DaemonVersion: serviceStatus.BinaryVersion, DaemonCommit: serviceStatus.BinaryCommit,
		DaemonSchemaMin: serviceStatus.SchemaMin, DaemonSchemaMax: serviceStatus.SchemaMax,
	}
	if recoveryPending {
		mr.ServiceInstalled = recovery.ServiceInstalled
		mr.ServiceWasRunning = recovery.ServiceWasRunning
		mr.ServiceQuiesced = true
		mr.BackupPath = recovery.BackupPath
		mr.BackupVerified = recovery.BackupVerified
		mr.IdentityPreserved = recovery.IdentityPreserved
		mr.RecoveryState = recovery.Phase
	}
	needsMigration := inspection.Compatibility.Compatible && inspection.FromVersion > 1 && inspection.FromVersion < inspection.ToVersion
	if recoveryPending && !opts.confirm {
		return writeMigrateCacheRecoveryFailure(stdout, stderr, opts.format, mr, "cache_schema_recovery_confirmation_required")
	}
	if opts.confirm && needsMigration && !recoveryPending && (serviceStatus.Installed || serviceStatus.Running || serviceStatus.PIDAlive) {
		recovery = cacheMigrationRecovery{
			TargetSchema: inspection.ToVersion, Phase: "coordination_planned",
			ServiceInstalled: serviceStatus.Installed, ServiceWasRunning: serviceStatus.Running,
		}
		if err := writeCacheMigrationRecovery(cachePath, recovery); err != nil {
			return writeError(stderr, opts.format, err)
		}
		recoveryPending = true
	}
	if opts.confirm && (needsMigration || recoveryPending) && (serviceStatus.Running || serviceStatus.PIDAlive) {
		if _, err := migrationService.QuiesceForCacheMigration(ctx); err != nil {
			return writeError(stderr, opts.format, err)
		}
		mr.ServiceQuiesced = true
		mr.RecoveryState = "coordinator_quiesced"
		if recoveryPending {
			recovery.Phase = mr.RecoveryState
			if err := writeCacheMigrationRecovery(cachePath, recovery); err != nil {
				return writeMigrateCacheRecoveryFailure(stdout, stderr, opts.format, mr, "cache_schema_recovery_intent_failed")
			}
		}
	}
	result := inspection
	if opts.confirm && needsMigration {
		result, err = cache.MigrateCacheWithConfirm(ctx, cachePath, false, cache.Confirmation{Confirmed: true})
		if err != nil {
			if recoveryPending && recovery.ServiceInstalled && recovery.ServiceWasRunning {
				if _, restartErr := migrationService.Start(ctx); restartErr == nil {
					_ = clearCacheMigrationRecovery(cachePath)
				}
			} else if recoveryPending {
				_ = clearCacheMigrationRecovery(cachePath)
			}
			return writeError(stderr, opts.format, err)
		}
		mr.BackupVerified = result.BackupVerified
		mr.IdentityPreserved = result.IdentityPreserved
		mr.BackupPath = result.BackupPath
		if recoveryPending {
			recovery.Phase = "migration_committed"
			recovery.BackupPath = result.BackupPath
			recovery.BackupVerified = result.BackupVerified
			recovery.IdentityPreserved = result.IdentityPreserved
			if err := writeCacheMigrationRecovery(cachePath, recovery); err != nil {
				mr.RecoveryState = "migration_complete_recovery_intent_failed"
				return writeMigrateCacheRecoveryFailure(stdout, stderr, opts.format, mr, "cache_schema_recovery_intent_failed")
			}
		}
	}
	if opts.confirm && recoveryPending {
		var compatibleService servicectl.Status
		if recovery.ServiceInstalled {
			if _, err := migrationService.Install(true); err != nil {
				mr.RecoveryState = "migration_complete_service_install_failed"
				recovery.Phase = mr.RecoveryState
				_ = writeCacheMigrationRecovery(cachePath, recovery)
				return writeMigrateCacheRecoveryFailure(stdout, stderr, opts.format, mr, "cache_schema_service_install_failed")
			}
			recovery.Phase = "compatible_service_installed"
			if err := writeCacheMigrationRecovery(cachePath, recovery); err != nil {
				mr.RecoveryState = "migration_complete_recovery_intent_failed"
				return writeMigrateCacheRecoveryFailure(stdout, stderr, opts.format, mr, "cache_schema_recovery_intent_failed")
			}
			started, err := migrationService.Start(ctx)
			if err != nil {
				mr.RecoveryState = "migration_complete_service_restart_failed"
				recovery.Phase = mr.RecoveryState
				_ = writeCacheMigrationRecovery(cachePath, recovery)
				return writeMigrateCacheRecoveryFailure(stdout, stderr, opts.format, mr, "cache_schema_service_restart_failed")
			}
			if !started.Running || started.SchemaMin == 0 || started.SchemaMax == 0 || recovery.TargetSchema < started.SchemaMin || recovery.TargetSchema > started.SchemaMax {
				mr.RecoveryState = "migration_complete_service_health_failed"
				recovery.Phase = mr.RecoveryState
				_ = writeCacheMigrationRecovery(cachePath, recovery)
				return writeMigrateCacheRecoveryFailure(stdout, stderr, opts.format, mr, "cache_schema_service_health_failed")
			}
			compatibleService = started
			mr.ServiceRestarted = true
			mr.DaemonVersion = started.BinaryVersion
			mr.DaemonCommit = started.BinaryCommit
			mr.DaemonSchemaMin = started.SchemaMin
			mr.DaemonSchemaMax = started.SchemaMax
		}
		if !recovery.BackupVerified || !recovery.IdentityPreserved {
			mr.RecoveryState = "healthy_evidence_verification_failed"
			recovery.Phase = mr.RecoveryState
			_ = writeCacheMigrationRecovery(cachePath, recovery)
			return writeMigrateCacheRecoveryFailure(stdout, stderr, opts.format, mr, "cache_schema_recovery_evidence_failed")
		}
		identityStore, err := cache.NewSQLiteReadOnlyStore(ctx, cachePath)
		if err != nil {
			mr.RecoveryState = "healthy_identity_read_failed"
			recovery.Phase = mr.RecoveryState
			_ = writeCacheMigrationRecovery(cachePath, recovery)
			return writeMigrateCacheRecoveryFailure(stdout, stderr, opts.format, mr, "cache_schema_recovery_identity_failed")
		}
		identity, identityErr := identityStore.CacheIdentity(ctx)
		closeErr := identityStore.Close()
		if identityErr != nil || closeErr != nil || strings.TrimSpace(identity.UUID) == "" {
			mr.RecoveryState = "healthy_identity_read_failed"
			recovery.Phase = mr.RecoveryState
			_ = writeCacheMigrationRecovery(cachePath, recovery)
			return writeMigrateCacheRecoveryFailure(stdout, stderr, opts.format, mr, "cache_schema_recovery_identity_failed")
		}
		if err := writeCacheMigrationReceipt(cachePath, cacheMigrationReceipt{
			CacheUUID:    identity.UUID,
			TargetSchema: recovery.TargetSchema, Phase: "healthy",
			BackupVerified: recovery.BackupVerified, IdentityPreserved: recovery.IdentityPreserved,
			TargetBinaryVersion: compatibleService.BinaryVersion, TargetBinaryCommit: compatibleService.BinaryCommit,
			TargetSchemaMin: compatibleService.SchemaMin, TargetSchemaMax: compatibleService.SchemaMax,
			CompletedAt: time.Now().UTC(),
		}); err != nil {
			mr.RecoveryState = "healthy_receipt_failed"
			recovery.Phase = mr.RecoveryState
			_ = writeCacheMigrationRecovery(cachePath, recovery)
			return writeMigrateCacheRecoveryFailure(stdout, stderr, opts.format, mr, "cache_schema_recovery_receipt_failed")
		}
		if err := clearCacheMigrationRecovery(cachePath); err != nil {
			mr.RecoveryState = "healthy_recovery_intent_cleanup_failed"
			return writeMigrateCacheRecoveryFailure(stdout, stderr, opts.format, mr, "cache_schema_recovery_cleanup_failed")
		}
		mr.RecoveryState = "healthy"
	}

	mr.Applied = result.Applied
	mr.BackupPath = result.BackupPath

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

func writeMigrateCacheRecoveryFailure(stdout, stderr io.Writer, format string, result migrateCacheResult, failureClass string) int {
	result.Status = "recovery_required"
	result.Remediation = "re-run gitcode-mcp migrate-cache --confirm with the compatible binary; the durable recovery intent will resume install, restart, and health verification"
	if format == "json" {
		_ = renderJSON(stdout, result)
	} else {
		renderMigrateCacheText(stdout, result)
	}
	fmt.Fprintf(stderr, "cache migration recovery is incomplete\nfailure_class: %s\n", failureClass)
	return 1
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
	fmt.Fprintf(w, "backup_verified: %t\nidentity_preserved: %t\n", mr.BackupVerified, mr.IdentityPreserved)
	fmt.Fprintf(w, "service_installed: %t\nservice_was_running: %t\nservice_quiesced: %t\nservice_restarted: %t\n", mr.ServiceInstalled, mr.ServiceWasRunning, mr.ServiceQuiesced, mr.ServiceRestarted)
	if mr.DaemonVersion != "" {
		fmt.Fprintf(w, "daemon_version: %s\n", mr.DaemonVersion)
	}
	if mr.DaemonCommit != "" {
		fmt.Fprintf(w, "daemon_commit: %s\n", mr.DaemonCommit)
	}
	if mr.DaemonSchemaMin != 0 || mr.DaemonSchemaMax != 0 {
		fmt.Fprintf(w, "daemon_schema_range: %d..%d\n", mr.DaemonSchemaMin, mr.DaemonSchemaMax)
	}
	if mr.RecoveryState != "" {
		fmt.Fprintf(w, "recovery_state: %s\n", mr.RecoveryState)
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
	fmt.Fprintln(w, "  --title TITLE --body BODY --body-file PATH|- --label LABEL --labels A,B --tag TAG")
	fmt.Fprintln(w, "  --body-file is scoped to issue and issue-comment Markdown writes")
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
	case "feedback":
		fmt.Fprintln(w, "Usage: gitcode-mcp feedback prepare [--input PATH | structured flags] [--format FORMAT]")
		fmt.Fprintln(w, "       gitcode-mcp feedback submit [--input PATH | structured flags] --live --idempotency-key KEY [--format FORMAT]")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Prepare or submit a structured, redacted dogfood report to the configured feedback sink.")
		fmt.Fprintln(w, "Preparation is read-only. Submission is an audited write and cannot override the configured destination.")
		fmt.Fprintln(w, "Required draft fields: --title, --category, --surface, --reporter-type, --observed, --expected, --impact.")
		fmt.Fprintln(w, "Use repeatable --step and --evidence flags for bounded public-safe facts; never include prompts, transcripts, secrets, cookies, private content, or raw API bodies.")
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
		fmt.Fprintf(w, "Usage: gitcode-mcp %s --repo REPO QUERY [--mode hybrid|full_text] [--kind KIND] [--provenance PROVENANCE] [--limit N] [--offset N]\n\n", command)
		fmt.Fprintln(w, "Search cached sources with hybrid lexical plus semantic retrieval by default.")
		fmt.Fprintln(w, "Use --mode full_text for deterministic exact/token matching without an embedding-provider call.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO       repository id")
		fmt.Fprintln(w, "  --mode MODE       hybrid (default) or full_text")
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
		fmt.Fprintf(w, "Usage: gitcode-mcp %s [--offline|--fixture] --repo REPO [--issues] [--wiki] [--pulls] [--issue-comments|--pr-comments|--comments] [--daemon] [--detach] [--index] [--details] [--id ID] [--input REMOTE_ALIAS] [--idempotency-key KEY]\n\n", command)
		fmt.Fprintln(w, "Synchronize cached records. Uses live GitCode by default; use --offline/--fixture for deterministic fixture sync.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --live              compatibility alias for live provider selection")
		fmt.Fprintln(w, "  --offline           use offline/fixture provider")
		fmt.Fprintln(w, "  --fixture           use fixture provider")
		fmt.Fprintln(w, "  --repo REPO         repository id")
		fmt.Fprintln(w, "  --issues            sync issue records")
		fmt.Fprintln(w, "  --wiki              sync wiki records")
		fmt.Fprintln(w, "  --pulls             sync pull request records")
		fmt.Fprintln(w, "  --issue-comments    sync the durable issue comment queue with aggregate-first collection")
		fmt.Fprintln(w, "  --pr-comments       sync pull request comments; with --input pr:N, sync exactly one cached PR")
		fmt.Fprintln(w, "  --comments          compatibility selector; --input issue:N targets issue comments and pr:N targets PR comments")
		fmt.Fprintln(w, "  --daemon            start collection sync as a service-owned job and attach progress")
		fmt.Fprintln(w, "  --detach            start collection sync as a service-owned job and return the job id")
		fmt.Fprintln(w, "  --index             build index after sync")
		fmt.Fprintln(w, "  --max-pages N       collection-only page bound; omit to traverse until end/frontier")
		fmt.Fprintln(w, "  --max-records N     collection-only record bound; omit to traverse until end/frontier")
		fmt.Fprintln(w, "  --per-page N        collection-only records per page")
		fmt.Fprintln(w, "  --progress MODE     progress mode: auto, spinner, lines, jsonl, off")
		fmt.Fprintln(w, "  --quiet             suppress non-result progress output")
		fmt.Fprintln(w, "  --id ID             stable record id")
		fmt.Fprintln(w, "  --input ALIAS       exact remote alias; a matching --issues/--wiki/--pulls selector is allowed")
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
		fmt.Fprintf(w, "Usage: gitcode-mcp %s --repo REPO --title TITLE [--body BODY | --body-file PATH|-] [--labels A,B] [--milestone ID_OR_TITLE] [--idempotency-key KEY]\n\n", command)
		fmt.Fprintln(w, "Create a new issue. Executes live by default; use --dry-run for no-mutation validation.")
		fmt.Fprintln(w, "Use --body-file for multiline Markdown; CRLF/CR are normalized to LF and trailing newlines are preserved.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO         repository id (required)")
		fmt.Fprintln(w, "  --title TITLE       issue title (required)")
		fmt.Fprintln(w, "  --body BODY         issue body")
		fmt.Fprintln(w, "  --body-file PATH|-  UTF-8 issue body file, or stdin with -")
		fmt.Fprintln(w, "  --allow-literal-backslash-n  allow intentional inline literal \\n sequences")
		fmt.Fprintln(w, "  --labels A,B        comma-separated labels")
		fmt.Fprintln(w, "  --milestone VALUE   milestone remote id, stable MILESTONE-id, or exact title")
		fmt.Fprintln(w, "  --idempotency-key KEY  idempotency key")
		fmt.Fprintln(w, "  --dry-run           validate without mutation")
		fmt.Fprintln(w, "  --live              compatibility alias for live write")
		fmt.Fprintln(w, "  --cache-path PATH   cache database path")
		fmt.Fprintln(w, "  --format FORMAT     output format (text, json)")
	case "update-issue":
		fmt.Fprintf(w, "Usage: gitcode-mcp %s --repo REPO (--number N | --issue-id ISSUE_OR_ALIAS) [--title TITLE] [--body BODY | --body-file PATH|-] [--state open|closed] [--labels A,B] [--milestone ID_OR_TITLE | --clear-milestone] [--idempotency-key KEY]\n\n", command)
		fmt.Fprintln(w, "Update an existing issue. Executes live by default; use --dry-run for no-mutation validation.")
		fmt.Fprintln(w, "Use --body-file for multiline Markdown; CRLF/CR are normalized to LF and trailing newlines are preserved.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO         repository id (required)")
		fmt.Fprintln(w, "  --number N          repository-local issue number; not a provider id")
		fmt.Fprintln(w, "  --issue-id VALUE    stable source id or known cached issue alias")
		fmt.Fprintln(w, "  --title TITLE       updated title")
		fmt.Fprintln(w, "  --body BODY         updated body")
		fmt.Fprintln(w, "  --body-file PATH|-  UTF-8 updated body file, or stdin with -")
		fmt.Fprintln(w, "  --allow-literal-backslash-n  allow intentional inline literal \\n sequences")
		fmt.Fprintln(w, "  --state STATE       updated state: open or closed")
		fmt.Fprintln(w, "  --labels A,B        comma-separated labels")
		fmt.Fprintln(w, "  --milestone VALUE   milestone remote id, stable MILESTONE-id, or exact title")
		fmt.Fprintln(w, "  --clear-milestone   clear the current milestone; conflicts with --milestone")
		fmt.Fprintln(w, "  --idempotency-key KEY  idempotency key")
		fmt.Fprintln(w, "  --dry-run           validate without mutation")
		fmt.Fprintln(w, "  --live              compatibility alias for live write")
		fmt.Fprintln(w, "  --cache-path PATH   cache database path")
		fmt.Fprintln(w, "  --format FORMAT     output format (text, json)")
	case "milestones":
		fmt.Fprintln(w, "Usage: gitcode-mcp milestones --repo REPO [--state open|closed] [--per-page N]")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "List repository milestones and refresh the local cache.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO         repository id (required)")
		fmt.Fprintln(w, "  --state STATE       milestone state filter")
		fmt.Fprintln(w, "  --per-page N        records per page")
		fmt.Fprintln(w, "  --cache-path PATH   cache database path")
		fmt.Fprintln(w, "  --format FORMAT     output format (text, json)")
	case "list-push-mirrors", "push-mirrors":
		fmt.Fprintln(w, "Usage: gitcode-mcp list-push-mirrors --repo REPO")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "List repository push mirrors and refresh sanitized cached records.")
		fmt.Fprintln(w, "`push-mirrors` remains a backward-compatible alias.")
		fmt.Fprintln(w, "Credentials, query parameters, and fragments are removed from mirror destinations.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO         repository id (required)")
		fmt.Fprintln(w, "  --cache-path PATH   cache database path")
		fmt.Fprintln(w, "  --format FORMAT     output format (text, json)")
	case "trigger-push-mirror":
		fmt.Fprintln(w, "Usage: gitcode-mcp trigger-push-mirror --repo REPO [--mirror-id ID] --live --idempotency-key KEY")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Trigger a configured push mirror through an audited live write.")
		fmt.Fprintln(w, "The mirror id may be omitted only when exactly one mirror is configured.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO           repository id (required)")
		fmt.Fprintln(w, "  --mirror-id ID        configured mirror id")
		fmt.Fprintln(w, "  --live                required live write acknowledgement")
		fmt.Fprintln(w, "  --idempotency-key KEY idempotency key")
		fmt.Fprintln(w, "  --format FORMAT       output format (text, json)")
	case "wait-push-mirror":
		fmt.Fprintln(w, "Usage: gitcode-mcp wait-push-mirror --repo REPO [--mirror-id ID] [--after RFC3339] [--timeout-seconds N]")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Poll sanitized live status until the mirror finishes, fails, or times out.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO           repository id (required)")
		fmt.Fprintln(w, "  --mirror-id ID        configured mirror id")
		fmt.Fprintln(w, "  --after RFC3339       require terminal update at or after this timestamp")
		fmt.Fprintln(w, "  --timeout-seconds N   wait timeout, 1-600 (default 120)")
		fmt.Fprintln(w, "  --format FORMAT       output format (text, json)")
	case "create-milestone":
		fmt.Fprintln(w, "Usage: gitcode-mcp create-milestone --repo REPO --title TITLE --due-on YYYY-MM-DD [--description TEXT] [--state open|closed] [--idempotency-key KEY]")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Create a milestone. GitCode requires --due-on. Executes live by default; use --dry-run for no-mutation validation.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO         repository id (required)")
		fmt.Fprintln(w, "  --title TITLE       milestone title (required)")
		fmt.Fprintln(w, "  --due-on DATE       due date YYYY-MM-DD (required)")
		fmt.Fprintln(w, "  --description TEXT  milestone description")
		fmt.Fprintln(w, "  --state STATE       milestone state")
		fmt.Fprintln(w, "  --idempotency-key KEY  idempotency key")
		fmt.Fprintln(w, "  --dry-run           validate without mutation")
		fmt.Fprintln(w, "  --live              compatibility alias for live write")
		fmt.Fprintln(w, "  --cache-path PATH   cache database path")
		fmt.Fprintln(w, "  --format FORMAT     output format (text, json)")
	case "update-milestone":
		fmt.Fprintln(w, "Usage: gitcode-mcp update-milestone --repo REPO --milestone ID_OR_TITLE [--title TITLE] [--due-on YYYY-MM-DD] [--description TEXT] [--state open|closed] [--idempotency-key KEY]")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Update milestone metadata. Executes live by default; use --dry-run for no-mutation validation.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO         repository id (required)")
		fmt.Fprintln(w, "  --milestone VALUE   milestone id or exact title (required)")
		fmt.Fprintln(w, "  --title TITLE       updated title")
		fmt.Fprintln(w, "  --due-on DATE       updated due date YYYY-MM-DD")
		fmt.Fprintln(w, "  --description TEXT  updated description")
		fmt.Fprintln(w, "  --state STATE       updated state")
		fmt.Fprintln(w, "  --idempotency-key KEY  idempotency key")
		fmt.Fprintln(w, "  --dry-run           validate without mutation")
		fmt.Fprintln(w, "  --live              compatibility alias for live write")
		fmt.Fprintln(w, "  --cache-path PATH   cache database path")
		fmt.Fprintln(w, "  --format FORMAT     output format (text, json)")
	case "set-issue-milestone":
		fmt.Fprintln(w, "Usage: gitcode-mcp set-issue-milestone --repo REPO (--number N | --issue-id ISSUE_OR_ALIAS) --milestone ID_OR_TITLE [--idempotency-key KEY]")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Assign a milestone to an issue and verify by readback. Executes live by default; use --dry-run for no-mutation validation.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO         repository id (required)")
		fmt.Fprintln(w, "  --number N          repository-local issue number; not a provider id")
		fmt.Fprintln(w, "  --issue-id VALUE    stable source id or known cached issue alias")
		fmt.Fprintln(w, "  --milestone VALUE   milestone id or exact title (required)")
		fmt.Fprintln(w, "  --idempotency-key KEY  idempotency key")
		fmt.Fprintln(w, "  --dry-run           validate without mutation")
		fmt.Fprintln(w, "  --live              compatibility alias for live write")
		fmt.Fprintln(w, "  --cache-path PATH   cache database path")
		fmt.Fprintln(w, "  --format FORMAT     output format (text, json)")
	case "clear-issue-milestone":
		fmt.Fprintln(w, "Usage: gitcode-mcp clear-issue-milestone --repo REPO (--number N | --issue-id ISSUE_OR_ALIAS) [--idempotency-key KEY]")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Clear an issue milestone and verify by readback. Executes live by default; use --dry-run for no-mutation validation.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO         repository id (required)")
		fmt.Fprintln(w, "  --number N          repository-local issue number; not a provider id")
		fmt.Fprintln(w, "  --issue-id VALUE    stable source id or known cached issue alias")
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
	case "update-pr":
		fmt.Fprintln(w, "Usage: gitcode-mcp update-pr --repo REPO --number N [--title TITLE] [--body BODY] [--state STATE] [--idempotency-key KEY]")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Update an existing pull request / merge request. Executes live by default; use --dry-run for no-mutation validation.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO         repository id (required)")
		fmt.Fprintln(w, "  --number N          pull request number (required)")
		fmt.Fprintln(w, "  --title TITLE       updated pull request title")
		fmt.Fprintln(w, "  --body BODY         updated pull request body")
		fmt.Fprintln(w, "  --state STATE       updated pull request state")
		fmt.Fprintln(w, "  --idempotency-key KEY  idempotency key")
		fmt.Fprintln(w, "  --dry-run           validate without mutation")
		fmt.Fprintln(w, "  --live              compatibility alias for live write")
		fmt.Fprintln(w, "  --cache-path PATH   cache database path")
		fmt.Fprintln(w, "  --format FORMAT     output format (text, json)")
	case "merge-pr", "merge-mr":
		fmt.Fprintf(w, "Usage: gitcode-mcp %s --repo REPO --number N [--strategy merge|squash|rebase] [--sha HEAD_SHA] [--idempotency-key KEY]\n\n", command)
		fmt.Fprintln(w, "Merge a pull request / merge request and confirm the merged state by readback. Executes live by default; use --dry-run for validation.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO         repository id (required)")
		fmt.Fprintln(w, "  --number N          pull request number (required)")
		fmt.Fprintln(w, "  --strategy VALUE    merge, squash, or rebase (default merge)")
		fmt.Fprintln(w, "  --sha SHA           require this exact current head SHA before merge")
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
		fmt.Fprintf(w, "Usage: gitcode-mcp %s --repo REPO (--number N | --issue-id ISSUE_OR_ALIAS) (--body BODY | --body-file PATH|-) [--idempotency-key KEY]\n\n", command)
		fmt.Fprintln(w, "Add a comment to an issue. Executes live by default; use --dry-run for no-mutation validation.")
		fmt.Fprintln(w, "Use --body-file for multiline Markdown; CRLF/CR are normalized to LF and trailing newlines are preserved.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO         repository id (required)")
		fmt.Fprintln(w, "  --number N          repository-local issue number; not a provider id")
		fmt.Fprintln(w, "  --issue-id VALUE    stable source id or known cached issue alias")
		fmt.Fprintln(w, "  --body BODY         comment body (required)")
		fmt.Fprintln(w, "  --body-file PATH|-  UTF-8 comment body file, or stdin with -")
		fmt.Fprintln(w, "  --allow-literal-backslash-n  allow intentional inline literal \\n sequences")
		fmt.Fprintln(w, "  --idempotency-key KEY  idempotency key")
		fmt.Fprintln(w, "  --dry-run           validate without mutation")
		fmt.Fprintln(w, "  --live              compatibility alias for live write")
		fmt.Fprintln(w, "  --cache-path PATH   cache database path")
		fmt.Fprintln(w, "  --format FORMAT     output format (text, json)")
	case "add-pr-review-comment":
		fmt.Fprintf(w, "Usage: gitcode-mcp %s --repo REPO --number N --path PATH --line N --body BODY [--start-line N] [--end-line N] [--idempotency-key KEY]\n\n", command)
		fmt.Fprintln(w, "Create an inline pull request review comment. Executes live by default; use --dry-run for no-mutation validation.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO         repository id (required)")
		fmt.Fprintln(w, "  --number N          pull request number (required)")
		fmt.Fprintln(w, "  --path PATH         changed file path (required)")
		fmt.Fprintln(w, "  --line N            1-based current-side file line (required)")
		fmt.Fprintln(w, "  --position N        deprecated file-line alias; if supplied, must equal --line")
		fmt.Fprintln(w, "  --start-line N      optional range start at or before --line")
		fmt.Fprintln(w, "  --end-line N        optional range end; must equal --line")
		fmt.Fprintln(w, "  --body BODY         comment body (required)")
		fmt.Fprintln(w, "  --idempotency-key KEY  idempotency key")
		fmt.Fprintln(w, "  --dry-run           validate without mutation")
		fmt.Fprintln(w, "  --live              compatibility alias for live write")
		fmt.Fprintln(w, "  --cache-path PATH   cache database path")
		fmt.Fprintln(w, "  --format FORMAT     output format (text, json)")
	case "reply-pr-review-comment":
		fmt.Fprintln(w, "Usage: gitcode-mcp reply-pr-review-comment --repo REPO --number N --discussion-id ID --parent-comment-id ID --body BODY [--idempotency-key KEY]")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Reply inside an existing pull request review discussion. Executes live by default; use --dry-run for no-mutation validation.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO             repository id (required)")
		fmt.Fprintln(w, "  --number N              pull request number (required)")
		fmt.Fprintln(w, "  --discussion-id ID      review discussion id (required)")
		fmt.Fprintln(w, "  --parent-comment-id ID  parent/root review comment id (required)")
		fmt.Fprintln(w, "  --body BODY             reply body (required)")
		fmt.Fprintln(w, "  --idempotency-key KEY   idempotency key")
		fmt.Fprintln(w, "  --dry-run               validate without mutation")
		fmt.Fprintln(w, "  --live                  compatibility alias for live write")
		fmt.Fprintln(w, "  --cache-path PATH       cache database path")
		fmt.Fprintln(w, "  --format FORMAT         output format (text, json)")
	case "update-comment":
		fmt.Fprintf(w, "Usage: gitcode-mcp %s --repo REPO --comment-id ID (--body BODY | --body-file PATH|-) [--number N] [--idempotency-key KEY]\n\n", command)
		fmt.Fprintln(w, "Update an existing issue comment. Executes live by default; use --dry-run for no-mutation validation.")
		fmt.Fprintln(w, "Use --body-file for multiline Markdown; CRLF/CR are normalized to LF and trailing newlines are preserved.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO         repository id (required)")
		fmt.Fprintln(w, "  --comment-id ID     issue comment id (required)")
		fmt.Fprintln(w, "  --number N          issue number hint for cache parent resolution")
		fmt.Fprintln(w, "  --body BODY         updated comment body (required)")
		fmt.Fprintln(w, "  --body-file PATH|-  UTF-8 updated body file, or stdin with -")
		fmt.Fprintln(w, "  --allow-literal-backslash-n  allow intentional inline literal \\n sequences")
		fmt.Fprintln(w, "  --idempotency-key KEY  idempotency key")
		fmt.Fprintln(w, "  --dry-run           validate without mutation")
		fmt.Fprintln(w, "  --live              compatibility alias for live write")
		fmt.Fprintln(w, "  --cache-path PATH   cache database path")
		fmt.Fprintln(w, "  --format FORMAT     output format (text, json)")
	case "add-label":
		fmt.Fprintf(w, "Usage: gitcode-mcp %s --repo REPO (--number N | --issue-id ISSUE_OR_ALIAS) --label LABEL [--idempotency-key KEY]\n\n", command)
		fmt.Fprintln(w, "Add a label to an issue. Executes live by default; use --dry-run for no-mutation validation.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO         repository id (required)")
		fmt.Fprintln(w, "  --number N          repository-local issue number; not a provider id")
		fmt.Fprintln(w, "  --issue-id VALUE    stable source id or known cached issue alias")
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
	case "service":
		fmt.Fprintf(w, "Usage: gitcode-mcp %s SUBCOMMAND\n\n", command)
		fmt.Fprintln(w, "Manage the local gitcode-mcp service coordinator.")
		fmt.Fprintln(w, "Subcommands:")
		fmt.Fprintln(w, "  install     write the user service definition")
		fmt.Fprintln(w, "  repair      replace the definition, reload it, and verify readiness")
		fmt.Fprintln(w, "  uninstall   remove the user service definition")
		fmt.Fprintln(w, "  start       start the installed service and wait for bounded readiness")
		fmt.Fprintln(w, "  stop        report how to stop the installed service")
		fmt.Fprintln(w, "  status      inspect runtime state")
		fmt.Fprintln(w, "  doctor      inspect service health")
		fmt.Fprintln(w, "  maintenance inspect daemon-managed cache and RAG lifecycle state")
		fmt.Fprintln(w, "  reconcile   request an immediate maintenance reconciliation")
		fmt.Fprintln(w, "  run         run the coordinator foreground process")
		fmt.Fprintln(w, "  fake-job    start a fake long-running job for IPC/progress dogfood")
		fmt.Fprintln(w, "  jobs        list daemon jobs")
		fmt.Fprintln(w, "  job         inspect one daemon job")
		fmt.Fprintln(w, "  attach      attach to job progress by id")
		fmt.Fprintln(w, "  cancel      cancel a daemon job by id")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Run gitcode-mcp service SUBCOMMAND --help for details.")
	case "admin":
		fmt.Fprintf(w, "Usage: gitcode-mcp %s SUBCOMMAND\n\n", command)
		fmt.Fprintln(w, "Open and inspect the embedded local admin UI on the existing daemon.")
		fmt.Fprintln(w, "Subcommands:")
		fmt.Fprintln(w, "  open        create a one-time launch link and open it")
		fmt.Fprintln(w, "  status      inspect the sanitized listener state")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Run gitcode-mcp admin SUBCOMMAND --help for details.")
	case "maintenance":
		fmt.Fprintf(w, "Usage: gitcode-mcp %s plan --repo REPO [flags]\n", command)
		fmt.Fprintf(w, "       gitcode-mcp %s enable --repo REPO [flags]\n", command)
		fmt.Fprintf(w, "       gitcode-mcp %s enable --repo REPO --yes --idempotency-key KEY [flags]  # automation\n\n", command)
		fmt.Fprintln(w, "Plan and enable daemon-managed cache refresh, historical backfill, and RAG repair.")
		fmt.Fprintln(w, "Plan is read-only. Enable revalidates the rendered plan before any local effect.")
		fmt.Fprintln(w, "In a terminal, enable renders the plan, prompts for confirmation, and derives a replay-safe opaque key.")
		fmt.Fprintln(w, "Non-interactive callers must provide both --yes and a stable --idempotency-key.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO                 configured repository id")
		fmt.Fprintln(w, "  --cache-path PATH           selected cache (CLI only)")
		fmt.Fprintln(w, "  --profile PROFILE           RAG profile")
		fmt.Fprintln(w, "  --sync MODE                 off, head, or head-and-backfill")
		fmt.Fprintln(w, "  --collections LIST          issues,issue-comments,wiki,pulls,pr-comments")
		fmt.Fprintln(w, "  --rag MODE                  off or maintain")
		fmt.Fprintln(w, "  --yes                       confirm exactly the rendered plan")
		fmt.Fprintln(w, "  --idempotency-key KEY       audited apply identity")
		fmt.Fprintln(w, "  --no-service-install        prohibit service installation")
		fmt.Fprintln(w, "  --no-model-download         prohibit model download")
		fmt.Fprintln(w, "  --detach                    return after initial jobs are coalesced")
		fmt.Fprintln(w, "  --format FORMAT             text or json")
	case "rag":
		fmt.Fprintf(w, "Usage: gitcode-mcp %s SUBCOMMAND\n\n", command)
		fmt.Fprintln(w, "Set up, inspect, index, and search local RAG providers and models.")
		fmt.Fprintln(w, "Subcommands:")
		fmt.Fprintln(w, "  setup       check provider, model, and embedding readiness")
		fmt.Fprintln(w, "  enable      compatibility shortcut for maintenance enable")
		fmt.Fprintln(w, "  index       start a daemon-owned RAG index job")
		fmt.Fprintln(w, "  status      report provider, namespace, coverage, and job state")
		fmt.Fprintln(w, "  search      run semantic/hybrid retrieval over cached chunks")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Run gitcode-mcp rag SUBCOMMAND --help for details.")
	case "rag-status":
		fmt.Fprintf(w, "Usage: gitcode-mcp %s --repo REPO [--profile PROFILE] [--cache-path PATH] [--format FORMAT]\n\n", command)
		fmt.Fprintln(w, "Report provider readiness, namespace coverage, last index run, and active daemon job state.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO         configured repository id")
		fmt.Fprintln(w, "  --profile PROFILE   RAG profile name")
		fmt.Fprintln(w, "  --cache-path PATH   cache database path")
		fmt.Fprintln(w, "  --format FORMAT     output format (text, json)")
	case "rag-search":
		fmt.Fprintf(w, "Usage: gitcode-mcp %s --repo REPO QUERY [--profile PROFILE] [--top-k N] [--limit N] [--format FORMAT]\n\n", command)
		fmt.Fprintln(w, "Run semantic/hybrid retrieval over cached RAG chunks without changing full-text search behavior.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO         configured repository id")
		fmt.Fprintln(w, "  --profile PROFILE   RAG profile name")
		fmt.Fprintln(w, "  --top-k N           semantic candidate count")
		fmt.Fprintln(w, "  --limit N           maximum packed contexts")
		fmt.Fprintln(w, "  --source-id ID      source id filter")
		fmt.Fprintln(w, "  --record-id ID      record id filter")
		fmt.Fprintln(w, "  --snapshot-id ID    snapshot id filter")
		fmt.Fprintln(w, "  --policy POLICY     chunk policy namespace")
		fmt.Fprintln(w, "  --cache-path PATH   cache database path")
		fmt.Fprintln(w, "  --format FORMAT     output format (text, json)")
	case "repo-docs":
		fmt.Fprintf(w, "Usage: gitcode-mcp %s SUBCOMMAND --repo REPO [flags]\n\n", command)
		fmt.Fprintln(w, "Inspect, index, and search versioned repository documentation through an explicitly registered private Git authority.")
		fmt.Fprintln(w, "Subcommands:")
		fmt.Fprintln(w, "  register    explicitly register a private local Git authority")
		fmt.Fprintln(w, "  rebind      compare-and-swap the private Git authority at an exact generation")
		fmt.Fprintln(w, "  policy      resolve the repository-owned policy at one revision")
		fmt.Fprintln(w, "  plan        estimate eligible committed documents without embedding")
		fmt.Fprintln(w, "  status      inspect revision-set identity and coverage")
		fmt.Fprintln(w, "  index       submit daemon-owned indexing for one immutable revision set")
		fmt.Fprintln(w, "  search      search one revision with verified Git citations")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO          configured repository id")
		fmt.Fprintln(w, "  --repository-path PATH  local Git path (register only)")
		fmt.Fprintln(w, "  --registration-id ID   opaque daemon registration id")
		fmt.Fprintln(w, "  --source-registration-id ID  opaque private Git authority id")
		fmt.Fprintln(w, "  --source-registration-generation N  exact authority generation")
		fmt.Fprintln(w, "  --revision REV       local Git revision (default HEAD)")
		fmt.Fprintln(w, "  --include-worktree   explicitly include tracked dirty files")
		fmt.Fprintln(w, "  --mode MODE          hybrid or fulltext (search)")
		fmt.Fprintln(w, "  --profile PROFILE    embedding profile")
		fmt.Fprintln(w, "  --detach             return the daemon job id without attaching")
		fmt.Fprintln(w, "  --cache-path PATH    cache database path")
		fmt.Fprintln(w, "  --format FORMAT      text or json")
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
		fmt.Fprintln(w, "Quiesce the installed coordinator and migrate a supported older cache schema.")
		fmt.Fprintln(w, "The WAL is checkpointed and a verified backup is created at {cache-path}.backup-{timestamp} before one atomic migration transaction.")
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
		fmt.Fprintln(w, "Usage: gitcode-mcp bind --repo-owner OWNER --repo REPO [--api-base-url URL] [--scopes SCOPES] [--cache-path PATH]")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Deprecated compatibility alias for repo add; new automation should use repo add directly.")
		fmt.Fprintln(w, "Derives repository id as OWNER/REPO and binds it without syncing.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo-owner OWNER  repository owner (required)")
		fmt.Fprintln(w, "  --repo REPO         repository name (required)")
		fmt.Fprintln(w, "  --api-base-url URL  API base URL (defaults to effective config, then GitCode v5)")
		fmt.Fprintln(w, "  --scopes SCOPES     comma-separated scopes (defaults to issues,wiki,pulls,comments)")
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
	case "maintenance plan", "maintenance enable":
		printCommandHelp("maintenance", w)
	case "rag enable":
		fmt.Fprintln(w, "Usage: gitcode-mcp rag enable --repo REPO [flags]")
		fmt.Fprintln(w, "       gitcode-mcp rag enable --repo REPO --yes --idempotency-key KEY [flags]")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Interactive terminals render the plan, ask for confirmation, and derive a replay-safe opaque operation key from that exact plan.")
		fmt.Fprintln(w, "Automation must provide --yes plus a stable --idempotency-key and reuse the same key when retrying.")
		fmt.Fprintln(w, "Examples:")
		fmt.Fprintln(w, "  gitcode-mcp rag enable --repo owner/repo")
		fmt.Fprintln(w, "  gitcode-mcp rag enable --repo owner/repo --yes --idempotency-key owner-repo-rag-enable-1")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO                 configured repository id")
		fmt.Fprintln(w, "  --yes                       confirm the rendered plan non-interactively")
		fmt.Fprintln(w, "  --idempotency-key KEY       stable automation/retry identity")
		fmt.Fprintln(w, "  --profile PROFILE           RAG profile")
		fmt.Fprintln(w, "  --sync MODE                 off, head, or head-and-backfill")
		fmt.Fprintln(w, "  --format FORMAT             text or json")
	case "rag setup":
		fmt.Fprintln(w, "Usage: gitcode-mcp rag setup [--profile PROFILE] [--dry-run] [--yes] [--format FORMAT]")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Check the configured RAG provider, model availability, and embedding readiness.")
		fmt.Fprintln(w, "Model downloads require --yes; dry-run reports planned actions without starting or pulling.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --profile PROFILE   RAG profile name")
		fmt.Fprintln(w, "  --dry-run           report setup actions without mutation")
		fmt.Fprintln(w, "  --yes               allow non-interactive model pull")
		fmt.Fprintln(w, "  --format FORMAT     output format (text, json)")
	case "rag index":
		fmt.Fprintln(w, "Usage: gitcode-mcp rag index --repo REPO [--profile PROFILE] [--policy POLICY] [--batch-size N] [--progress MODE] [--detach] [--format FORMAT]")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Start a daemon-owned RAG index job. By default the CLI attaches until the job reaches a terminal state.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO         configured repository id")
		fmt.Fprintln(w, "  --profile PROFILE   RAG profile name")
		fmt.Fprintln(w, "  --policy POLICY     chunk policy namespace (defaults to heading)")
		fmt.Fprintln(w, "  --batch-size N      embedding batch size")
		fmt.Fprintln(w, "  --progress MODE     progress mode: auto, spinner, lines, jsonl, off")
		fmt.Fprintln(w, "  --detach            return the job id without attaching progress")
		fmt.Fprintln(w, "  --format FORMAT     output format (text, json)")
	case "rag status":
		fmt.Fprintln(w, "Usage: gitcode-mcp rag status --repo REPO [--profile PROFILE] [--cache-path PATH] [--format FORMAT]")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Report provider readiness, namespace coverage, last index run, and active daemon job state.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO         configured repository id")
		fmt.Fprintln(w, "  --profile PROFILE   RAG profile name")
		fmt.Fprintln(w, "  --cache-path PATH   cache database path")
		fmt.Fprintln(w, "  --format FORMAT     output format (text, json)")
	case "rag search":
		fmt.Fprintln(w, "Usage: gitcode-mcp rag search --repo REPO QUERY [--profile PROFILE] [--top-k N] [--limit N] [--format FORMAT]")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Run semantic/hybrid retrieval over cached RAG chunks without changing full-text search behavior.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO         configured repository id")
		fmt.Fprintln(w, "  --profile PROFILE   RAG profile name")
		fmt.Fprintln(w, "  --top-k N           semantic candidate count")
		fmt.Fprintln(w, "  --limit N           maximum packed contexts")
		fmt.Fprintln(w, "  --source-id ID      source id filter")
		fmt.Fprintln(w, "  --record-id ID      record id filter")
		fmt.Fprintln(w, "  --snapshot-id ID    snapshot id filter")
		fmt.Fprintln(w, "  --policy POLICY     chunk policy namespace")
		fmt.Fprintln(w, "  --cache-path PATH   cache database path")
		fmt.Fprintln(w, "  --format FORMAT     output format (text, json)")
	case "repo-docs register", "repo-docs rebind", "repo-docs policy", "repo-docs status", "repo-docs plan", "repo-docs index", "repo-docs search":
		printCommandHelp("repo-docs", w)
	case "service install":
		fmt.Fprintln(w, "Usage: gitcode-mcp service install [--overwrite] [--format FORMAT]")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Resolve the current executable to a validated absolute path and write the platform user-service definition.")
		fmt.Fprintln(w, "Use service repair when an already-loaded definition is broken; overwrite alone does not reload it.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --overwrite         replace an existing service definition")
		fmt.Fprintln(w, "  --format FORMAT     output format (text, json)")
	case "service repair":
		fmt.Fprintln(w, "Usage: gitcode-mcp service repair [--format FORMAT]")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Validate the current executable, unload any loaded definition, replace it, restart it, and wait for readiness.")
		fmt.Fprintln(w, "This is the supported one-command recovery for a broken or stale service definition.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --format FORMAT     output format (text, json)")
	case "service uninstall":
		fmt.Fprintln(w, "Usage: gitcode-mcp service uninstall [--format FORMAT]")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Remove the platform user-service definition.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --format FORMAT     output format (text, json)")
	case "service start":
		fmt.Fprintln(w, "Usage: gitcode-mcp service start [--format FORMAT]")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Start the platform user service and wait for PID plus control-socket readiness.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --format FORMAT     output format (text, json)")
	case "service stop":
		fmt.Fprintln(w, "Usage: gitcode-mcp service stop [--format FORMAT]")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Report current service state and the platform-manager stop boundary.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --format FORMAT     output format (text, json)")
	case "service status":
		fmt.Fprintln(w, "Usage: gitcode-mcp service status [--format FORMAT]")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Inspect install, PID, socket, runtime, and log paths.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --format FORMAT     output format (text, json)")
	case "service doctor":
		fmt.Fprintln(w, "Usage: gitcode-mcp service doctor [--format FORMAT]")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Inspect service health and validate the installed executable definition.")
		fmt.Fprintln(w, "A broken definition reports gitcode-mcp service repair as the supported recovery.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --format FORMAT     output format (text, json)")
	case "service maintenance":
		fmt.Fprintln(w, "Usage: gitcode-mcp service maintenance [--format FORMAT]")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Inspect sanitized daemon-managed cache, backfill, and RAG coverage state.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --format FORMAT     output format (text, json)")
	case "service reconcile":
		fmt.Fprintln(w, "Usage: gitcode-mcp service reconcile [--format FORMAT]")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Request an immediate maintenance pass; scheduled work remains coalesced by cache identity.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --format FORMAT     output format (text, json)")
	case "service run":
		fmt.Fprintln(w, "Usage: gitcode-mcp service run [--admin] [--admin-bind ADDRESS] [--admin-unsafe-allow-non-loopback]")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Run the service coordinator in the foreground using a user-global Unix socket.")
		fmt.Fprintln(w, "The admin listener defaults to 127.0.0.1 on a dynamic port and can also start lazily through admin open.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --admin             start the admin listener with the daemon")
		fmt.Fprintln(w, "  --admin-bind ADDR   listener address (default 127.0.0.1:0)")
		fmt.Fprintln(w, "  --admin-unsafe-allow-non-loopback  explicitly permit a non-loopback bind")
	case "admin open":
		fmt.Fprintln(w, "Usage: gitcode-mcp admin open [--no-browser] [--format FORMAT]")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Attach to the existing daemon, issue a one-time launch link, and open the local UI.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --no-browser        print the one-time URL without launching a browser")
		fmt.Fprintln(w, "  --format FORMAT     output format (text, json)")
	case "admin status":
		fmt.Fprintln(w, "Usage: gitcode-mcp admin status [--format FORMAT]")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Inspect listener state without exposing launch or session material.")
	case "service fake-job":
		fmt.Fprintln(w, "Usage: gitcode-mcp service fake-job [--steps N] [--interval-ms N] [--detach] [--format FORMAT]")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Start a fake long-running daemon job. By default the CLI attaches until the job reaches a terminal state.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --steps N          fake job step count")
		fmt.Fprintln(w, "  --interval-ms N    delay between fake job progress events")
		fmt.Fprintln(w, "  --detach           return the job id without attaching progress")
		fmt.Fprintln(w, "  --format FORMAT    output format (text, json)")
	case "service jobs":
		fmt.Fprintln(w, "Usage: gitcode-mcp service jobs [--format FORMAT]")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "List daemon jobs through local JSON-RPC IPC.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --format FORMAT    output format (text, json)")
	case "service job":
		fmt.Fprintln(w, "Usage: gitcode-mcp service job JOB_ID [--format FORMAT]")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Inspect one daemon job through local JSON-RPC IPC.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --format FORMAT    output format (text, json)")
	case "service attach":
		fmt.Fprintln(w, "Usage: gitcode-mcp service attach JOB_ID [--format FORMAT]")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Attach to daemon job progress until the job reaches a terminal state.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --format FORMAT    output format (text, json)")
	case "service cancel":
		fmt.Fprintln(w, "Usage: gitcode-mcp service cancel JOB_ID [--format FORMAT]")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Cancel a daemon job by id through local JSON-RPC IPC.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --format FORMAT    output format (text, json)")
	case "repo add":
		fmt.Fprintln(w, "Usage: gitcode-mcp repo add --repo REPO --owner OWNER --name NAME --scopes SCOPES [--api-base-url URL] [--alias ALIAS] [--display-name NAME]")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Bind a GitCode repository to the cache.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo REPO         repository id (required)")
		fmt.Fprintln(w, "  --owner OWNER       repository owner (required)")
		fmt.Fprintln(w, "  --name NAME         repository name (required)")
		fmt.Fprintln(w, "  --api-base-url URL  authoritative live API base URL (defaults to effective config, then GitCode v5)")
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
