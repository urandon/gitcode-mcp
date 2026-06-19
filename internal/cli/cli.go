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
	"gitcode-mcp/internal/service"
)

const version = "0.1.0"

var commands = []string{
	"search_sources", "search",
	"list_sources", "list",
	"get_source", "get",
	"source_backlinks", "backlinks",
	"get_snippet", "snippet",
	"sync_status", "sync-status",
	"recent",
	"link-check",
	"stale-index",
	"export",
	"diff",
}

type queryService interface {
	SearchSources(context.Context, service.SearchSourcesRequest) ([]service.SearchSourceResult, error)
	ListSources(context.Context, service.ListSourcesRequest) ([]service.SourceSummary, error)
	GetSource(context.Context, service.GetSourceRequest) (service.SourceRecord, error)
	GetBacklinks(context.Context, service.GetBacklinksRequest) ([]service.BacklinkResult, error)
	GetSnippet(context.Context, service.SnippetRequest) (service.SnippetResult, error)
	GetSyncStatus(context.Context, service.SyncStatusRequest) (service.SyncStatusResult, error)
	RecentChanges(context.Context, service.RecentChangesRequest) ([]service.RecentChangeResult, error)
	LinkCheck(context.Context, service.LinkCheckRequest) (service.LinkCheckResult, error)
	StaleIndex(context.Context, service.StaleIndexRequest) (service.StaleIndexResult, error)
}

type serviceFactory func(context.Context, string) (queryService, func() error, error)

type options struct {
	format    string
	kind      string
	status    string
	limit     int
	offset    int
	lineStart int
	lineEnd   int
	cachePath string
	strict    bool
	base      string
	head      string
}

// Execute runs the gitcode-mcp CLI.
func Execute(args []string, stdout io.Writer, stderr io.Writer) int {
	return executeWithFactory(args, stdout, stderr, defaultServiceFactory)
}

func executeWithFactory(args []string, stdout io.Writer, stderr io.Writer, factory serviceFactory) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		printHelp(stdout)
		return 0
	}
	if args[0] == "--version" || args[0] == "version" {
		fmt.Fprintf(stdout, "gitcode-mcp %s\n", version)
		return 0
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
	flags.StringVar(&opts.format, "format", "text", "text or json")
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
	if err := flags.Parse(reorderFlags(args)); err != nil {
		return opts, nil, service.ErrInvalidQuery{Field: "flags", Message: err.Error()}
	}
	opts.format = strings.ToLower(opts.format)
	if opts.format != "text" && opts.format != "json" {
		return opts, nil, service.ErrInvalidQuery{Field: "format", Message: "format must be text or json"}
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
			if strings.Contains(arg, "=") || arg == "--strict" {
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

func dispatch(ctx context.Context, svc queryService, command string, args []string, opts options, stdout io.Writer, stderr io.Writer) int {
	switch command {
	case "search_sources", "search":
		if len(args) == 0 {
			return writeError(stderr, opts.format, service.ErrInvalidQuery{Field: "query", Message: "query is required"})
		}
		results, err := svc.SearchSources(ctx, service.SearchSourcesRequest{Query: strings.Join(args, " "), Kind: opts.kind, Limit: opts.limit, Offset: opts.offset})
		if err != nil {
			return writeError(stderr, opts.format, err)
		}
		return render(stdout, opts.format, results, renderSearchText)
	case "list_sources", "list":
		results, err := svc.ListSources(ctx, service.ListSourcesRequest{Kind: opts.kind, Status: opts.status, Limit: opts.limit, Offset: opts.offset})
		if err != nil {
			return writeError(stderr, opts.format, err)
		}
		return render(stdout, opts.format, results, renderListText)
	case "get_source", "get":
		id, ok := firstArg(args)
		if !ok {
			return writeError(stderr, opts.format, service.ErrInvalidQuery{Field: "id", Message: "id is required"})
		}
		result, err := svc.GetSource(ctx, service.GetSourceRequest{ID: id})
		if err != nil {
			return writeError(stderr, opts.format, err)
		}
		return render(stdout, opts.format, result, renderGetText)
	case "source_backlinks", "backlinks":
		id, ok := firstArg(args)
		if !ok {
			return writeError(stderr, opts.format, service.ErrInvalidQuery{Field: "id", Message: "id is required"})
		}
		results, err := svc.GetBacklinks(ctx, service.GetBacklinksRequest{ID: id})
		if err != nil {
			return writeError(stderr, opts.format, err)
		}
		return render(stdout, opts.format, results, renderBacklinksText)
	case "get_snippet", "snippet":
		id, ok := firstArg(args)
		if !ok {
			return writeError(stderr, opts.format, service.ErrInvalidQuery{Field: "id", Message: "id is required"})
		}
		result, err := svc.GetSnippet(ctx, service.SnippetRequest{ID: id, LineStart: opts.lineStart, LineEnd: opts.lineEnd})
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
	case "sync_status", "sync-status":
		id, ok := firstArg(args)
		if !ok {
			return writeError(stderr, opts.format, service.ErrInvalidQuery{Field: "id", Message: "id is required"})
		}
		result, err := svc.GetSyncStatus(ctx, service.SyncStatusRequest{ID: id})
		if err != nil {
			return writeError(stderr, opts.format, err)
		}
		return render(stdout, opts.format, result, renderSyncStatusText)
	case "recent":
		results, err := svc.RecentChanges(ctx, service.RecentChangesRequest{Kind: opts.kind, Status: opts.status, Limit: opts.limit, Offset: opts.offset})
		if err != nil {
			return writeError(stderr, opts.format, err)
		}
		return render(stdout, opts.format, results, renderRecentText)
	case "link-check":
		result, err := svc.LinkCheck(ctx, service.LinkCheckRequest{Strict: opts.strict})
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
		result, err := svc.StaleIndex(ctx, service.StaleIndexRequest{Strict: opts.strict})
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
	default:
		return writeError(stderr, opts.format, service.ErrInvalidQuery{Field: "command", Message: command + " is not a query command"})
	}
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

func renderSearchText(w io.Writer, results []service.SearchSourceResult) {
	for _, result := range results {
		line := 0
		if result.LineStart != nil {
			line = *result.LineStart
		}
		fmt.Fprintf(w, "%s:%d:%s\n", result.Path, line, result.Snippet)
	}
}

func renderListText(w io.Writer, results []service.SourceSummary) {
	for _, result := range results {
		fmt.Fprintf(w, "%s %s %s %s %s\n", result.ID, result.Kind, result.Status, result.Path, result.Title)
	}
}

func renderGetText(w io.Writer, result service.SourceRecord) {
	fmt.Fprintf(w, "id: %s\npath: %s\ntitle: %s\nstatus: %s\n\n%s\n", result.ID, result.Path, result.Title, result.Status, result.Body)
}

func renderBacklinksText(w io.Writer, results []service.BacklinkResult) {
	for _, result := range results {
		fmt.Fprintf(w, "%s %s %s %s\n", result.ID, result.Path, result.Title, result.TargetID)
	}
}

func renderSyncStatusText(w io.Writer, result service.SyncStatusResult) {
	fmt.Fprintf(w, "%s %s %s %s %s\n", result.SourceID, result.Status, result.RemoteType, result.RemoteID, result.LastFetchedAt.Format(time.RFC3339))
}

func renderRecentText(w io.Writer, results []service.RecentChangeResult) {
	for _, result := range results {
		fmt.Fprintf(w, "%s %s %s %s\n", result.UpdatedAt.UTC().Format(time.RFC3339), result.ID, result.Path, result.Title)
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

func writeError(stderr io.Writer, format string, err error) int {
	code := exitCode(err)
	if format == "json" {
		_ = json.NewEncoder(stderr).Encode(map[string]any{"error": err.Error(), "exit_code": code})
		return code
	}
	fmt.Fprintln(stderr, err.Error())
	return code
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
	var invalid service.ErrInvalidQuery
	if errors.As(err, &invalid) {
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
	fmt.Fprintln(w, "  find -> list_sources")
	fmt.Fprintln(w, "  rg -n -> search_sources")
	fmt.Fprintln(w, "  rg --files -> list_sources")
	fmt.Fprintln(w, "  sed -n -> get_snippet")
	fmt.Fprintln(w, "  handoff/review inspection -> recent")
	fmt.Fprintln(w, "  broken pointer search -> link-check")
	fmt.Fprintln(w, "  stale derived data search -> stale-index")
	fmt.Fprintln(w, "  minimum replacement sequence: ingest -> search_sources -> list_sources -> get_source -> source_backlinks -> sync_status")
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
