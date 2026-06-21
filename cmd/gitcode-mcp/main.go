package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/cli"
	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/gitcode"
	"gitcode-mcp/internal/mcp"
	"gitcode-mcp/internal/service"
)

const version = "0.1.0"

type StartupDeps struct {
	Config  config.Config
	Cache   CacheStartup
	GitCode GitCodeStartup
}

type CacheStartup struct {
	CachePath string
	LockPath  string
}

type GitCodeStartup struct {
	BaseURL         string
	DefaultTimeout  time.Duration
	MaxResponseSize int64
	MaxRetries      int
	Live            bool
	Token           string
	token           string
}

type startupOptions struct {
	mcp          bool
	help         bool
	version      bool
	mcpServe     bool
	mcpTransport string
	mcpBind      string
	live         bool
	overrides    config.Overrides
}

type cliRouteFunc func(context.Context, []string, io.Writer, io.Writer, StartupDeps) int

type mcpRouteFunc func(context.Context, io.Reader, io.Writer, io.Writer, StartupDeps) int

type mcpServeRouteFunc func(context.Context, io.Reader, io.Writer, io.Writer, StartupDeps, string, string) int

var cliRoute cliRouteFunc = runCLICompatibility
var mcpRoute mcpRouteFunc = runMCPStdio
var mcpServeRoute mcpServeRouteFunc = runMCPServe

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, config.OSSource{}))
}

func run(args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer, src config.Source) int {
	opts, rest, err := parseStartupArgs(args)
	if err != nil {
		fmt.Fprintln(stderr, config.RedactDiagnostic(err.Error(), src))
		return 2
	}
	if opts.help {
		if opts.mcpServe {
			printMCPServeHelp(stdout)
			return 0
		}
		if opts.mcp {
			printMCPHelp(stderr)
			return 0
		}
		printStartupHelp(stdout)
		return 0
	}
	if opts.version {
		fmt.Fprintf(stdout, "gitcode-mcp %s\n", version)
		return 0
	}
	if len(rest) > 0 && (rest[0] == "config" || rest[0] == "auth") {
		localArgs := append([]string(nil), rest...)
		if rest[0] == "auth" && opts.live && !hasCLIFlag(localArgs[1:], "--live") {
			localArgs = append(localArgs, "--live")
		}
		return cli.ExecuteWithSource(localArgs, stdout, stderr, src)
	}

	cfg, err := config.Load(src, opts.overrides)
	if err != nil {
		fmt.Fprintln(stderr, config.RedactDiagnostic(err.Error(), src))
		return 1
	}
	deps := buildStartupDeps(cfg, config.Token(src), opts.live)
	if opts.mcpServe {
		return mcpServeRoute(context.Background(), stdin, stdout, stderr, deps, opts.mcpTransport, opts.mcpBind)
	}
	if opts.mcp {
		return mcpRoute(context.Background(), stdin, stdout, stderr, deps)
	}
	return cliRoute(context.Background(), rest, stdout, stderr, deps)
}

func buildStartupDeps(cfg config.Config, token string, live bool) StartupDeps {
	return StartupDeps{
		Config: cfg,
		Cache:  CacheStartup{CachePath: cfg.CachePath, LockPath: cfg.LockPath},
		GitCode: GitCodeStartup{
			BaseURL:         cfg.GitCodeBaseURL,
			DefaultTimeout:  cfg.DefaultTimeout,
			MaxResponseSize: cfg.MaxResponseSize,
			MaxRetries:      cfg.MaxRetries,
			Live:            live,
			Token:           token,
			token:           token,
		},
	}
}

func parseStartupArgs(args []string) (startupOptions, []string, error) {
	if len(args) >= 2 && args[0] == "mcp" && args[1] == "serve" {
		return parseMCPServeArgs(args[2:])
	}
	var opts startupOptions
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			return opts, args[i+1:], nil
		}
		if arg == "-h" || arg == "--help" {
			opts.help = true
			continue
		}
		if arg == "--version" {
			opts.version = true
			continue
		}
		if arg == "--mcp" {
			opts.mcp = true
			continue
		}
		if strings.HasPrefix(arg, "--cache-path=") {
			opts.overrides.CachePath = strings.TrimPrefix(arg, "--cache-path=")
			continue
		}
		if arg == "--cache-path" {
			value, next, err := startupValue(args, i, arg)
			if err != nil {
				return opts, nil, err
			}
			opts.overrides.CachePath = value
			i = next
			continue
		}
		if strings.HasPrefix(arg, "--timeout=") {
			d, err := time.ParseDuration(strings.TrimPrefix(arg, "--timeout="))
			if err != nil {
				return opts, nil, fmt.Errorf("startup: invalid --timeout: %w", err)
			}
			opts.overrides.DefaultTimeout = d
			continue
		}
		if arg == "--timeout" {
			value, next, err := startupValue(args, i, arg)
			if err != nil {
				return opts, nil, err
			}
			d, err := time.ParseDuration(value)
			if err != nil {
				return opts, nil, fmt.Errorf("startup: invalid --timeout: %w", err)
			}
			opts.overrides.DefaultTimeout = d
			i = next
			continue
		}
		if strings.HasPrefix(arg, "--max-size=") {
			n, err := strconv.ParseInt(strings.TrimPrefix(arg, "--max-size="), 10, 64)
			if err != nil || n < 0 {
				return opts, nil, fmt.Errorf("startup: invalid --max-size")
			}
			opts.overrides.MaxResponseSize = n
			continue
		}
		if arg == "--max-size" {
			value, next, err := startupValue(args, i, arg)
			if err != nil {
				return opts, nil, err
			}
			n, err := strconv.ParseInt(value, 10, 64)
			if err != nil || n < 0 {
				return opts, nil, fmt.Errorf("startup: invalid --max-size")
			}
			opts.overrides.MaxResponseSize = n
			i = next
			continue
		}
		if strings.HasPrefix(arg, "--format=") {
			opts.overrides.Format = strings.TrimPrefix(arg, "--format=")
			continue
		}
		if arg == "--format" {
			value, next, err := startupValue(args, i, arg)
			if err != nil {
				return opts, nil, err
			}
			opts.overrides.Format = value
			i = next
			continue
		}
		if arg == "--live" {
			opts.live = true
			continue
		}
		if strings.HasPrefix(arg, "-") {
			return opts, nil, fmt.Errorf("startup: unknown global flag %s", arg)
		}
		if arg == "mcp" && i+1 < len(args) && args[i+1] == "serve" {
			serveOpts, rest, err := parseMCPServeArgs(args[i+2:])
			if err != nil {
				return opts, nil, err
			}
			serveOpts.live = opts.live || serveOpts.live
			serveOpts.overrides = mergeStartupOverrides(opts.overrides, serveOpts.overrides)
			return serveOpts, rest, nil
		}
		return opts, args[i:], nil
	}
	return opts, nil, nil
}

func mergeStartupOverrides(base config.Overrides, override config.Overrides) config.Overrides {
	if override.CachePath != "" {
		base.CachePath = override.CachePath
	}
	if override.LockPath != "" {
		base.LockPath = override.LockPath
	}
	if override.GitCodeBaseURL != "" {
		base.GitCodeBaseURL = override.GitCodeBaseURL
	}
	if override.DefaultTimeout != 0 {
		base.DefaultTimeout = override.DefaultTimeout
	}
	if override.MaxResponseSize != 0 {
		base.MaxResponseSize = override.MaxResponseSize
	}
	if override.MaxRetries != nil {
		base.MaxRetries = override.MaxRetries
	}
	if override.Format != "" {
		base.Format = override.Format
	}
	return base
}

func parseMCPServeArgs(args []string) (startupOptions, []string, error) {
	opts := startupOptions{mcpServe: true, mcpTransport: "stdio", mcpBind: "127.0.0.1:0"}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-h" || arg == "--help":
			opts.help = true
		case arg == "--live":
			opts.live = true
		case strings.HasPrefix(arg, "--transport="):
			opts.mcpTransport = strings.TrimPrefix(arg, "--transport=")
		case arg == "--transport":
			value, next, err := startupValue(args, i, arg)
			if err != nil {
				return opts, nil, err
			}
			opts.mcpTransport = value
			i = next
		case strings.HasPrefix(arg, "--bind="):
			opts.mcpBind = strings.TrimPrefix(arg, "--bind=")
		case arg == "--bind":
			value, next, err := startupValue(args, i, arg)
			if err != nil {
				return opts, nil, err
			}
			opts.mcpBind = value
			i = next
		case strings.HasPrefix(arg, "--cache-path="):
			opts.overrides.CachePath = strings.TrimPrefix(arg, "--cache-path=")
		case arg == "--cache-path":
			value, next, err := startupValue(args, i, arg)
			if err != nil {
				return opts, nil, err
			}
			opts.overrides.CachePath = value
			i = next
		default:
			return opts, nil, fmt.Errorf("startup: unknown mcp serve flag %s", arg)
		}
	}
	if opts.mcpTransport != "stdio" && opts.mcpTransport != "http-sse" {
		return opts, nil, fmt.Errorf("startup: invalid --transport %s", opts.mcpTransport)
	}
	return opts, nil, nil
}

func startupValue(args []string, index int, flag string) (string, int, error) {
	if index+1 >= len(args) || args[index+1] == "" {
		return "", index, fmt.Errorf("startup: %s requires a value", flag)
	}
	return args[index+1], index + 1, nil
}

func resolveLiveClient(deps StartupDeps) (gitcode.Client, error) {
	gc := deps.GitCode
	if !gc.Live {
		return nil, nil
	}
	token := strings.TrimSpace(gc.Token)
	if token == "" {
		return nil, fmt.Errorf("live provider requires GITCODE_TOKEN (set GITCODE_TOKEN or remove --live)")
	}
	provider, err := gitcode.NewLiveProvider(gitcode.ProviderConfig{
		Mode:            gitcode.ProviderModeLive,
		LiveAllowed:     true,
		BaseURL:         gc.BaseURL,
		Token:           token,
		Timeout:         gc.DefaultTimeout,
		MaxResponseSize: gc.MaxResponseSize,
		MaxRetries:      gc.MaxRetries,
	})
	if err != nil {
		return nil, err
	}
	client, ok := provider.(gitcode.Client)
	if !ok {
		return nil, fmt.Errorf("live provider does not implement GitCode client")
	}
	return client, nil
}

func resolveService(store cache.Store, deps StartupDeps) (*service.Service, error) {
	liveClient, err := resolveLiveClient(deps)
	if err != nil {
		return nil, err
	}
	if liveClient == nil {
		return service.New(store), nil
	}
	return service.NewWithClient(store, liveClient), nil
}

func runCLICompatibility(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer, deps StartupDeps) int {
	cliArgs := append([]string(nil), args...)
	if len(cliArgs) > 0 && deps.Config.CachePath != "" && !hasCLIFlag(cliArgs[1:], "--cache-path") {
		cliArgs = append(cliArgs, "--cache-path", deps.Config.CachePath)
	}
	if len(cliArgs) > 0 && deps.Config.Format != "" && !hasCLIFlag(cliArgs[1:], "--format") {
		cliArgs = append(cliArgs, "--format", deps.Config.Format)
	}
	liveClient, err := resolveLiveClient(deps)
	if err != nil {
		fmt.Fprintln(stderr, config.RedactDiagnostic(err.Error(), config.OSSource{}))
		return 1
	}
	_ = ctx
	if liveClient != nil {
		return cli.ExecuteWithClient(cliArgs, stdout, stderr, liveClient)
	}
	return cli.Execute(cliArgs, stdout, stderr)
}

func hasCLIFlag(args []string, name string) bool {
	for _, arg := range args {
		if arg == name || strings.HasPrefix(arg, name+"=") {
			return true
		}
	}
	return false
}

func runMCPServe(ctx context.Context, stdin io.Reader, stdout io.Writer, stderr io.Writer, deps StartupDeps, transport string, bind string) int {
	if transport == "stdio" {
		return runMCPStdio(ctx, stdin, stdout, stderr, deps)
	}
	return runMCPHTTPSSE(ctx, stderr, deps, bind)
}

func runMCPStdio(ctx context.Context, stdin io.Reader, stdout io.Writer, stderr io.Writer, deps StartupDeps) int {
	if err := ensureParentDir(deps.Config.CachePath); err != nil {
		fmt.Fprintln(stderr, config.RedactDiagnostic(err.Error(), config.OSSource{}))
		return 1
	}
	store, err := cache.NewSQLiteStore(ctx, deps.Config.CachePath)
	if err != nil {
		fmt.Fprintln(stderr, config.RedactDiagnostic(err.Error(), config.OSSource{}))
		return 1
	}
	defer store.Close()
	svc, err := resolveService(store, deps)
	if err != nil {
		fmt.Fprintln(stderr, config.RedactDiagnostic(err.Error(), config.OSSource{}))
		return 1
	}
	server := mcp.New(stdin, stdout, stderr, svc)
	if err := server.Serve(); err != nil {
		fmt.Fprintln(stderr, config.RedactDiagnostic(err.Error(), config.OSSource{}))
		return 1
	}
	return 0
}

func runMCPHTTPSSE(ctx context.Context, stderr io.Writer, deps StartupDeps, bind string) int {
	if err := ensureParentDir(deps.Config.CachePath); err != nil {
		fmt.Fprintln(stderr, config.RedactDiagnostic(err.Error(), config.OSSource{}))
		return 1
	}
	store, err := cache.NewSQLiteStore(ctx, deps.Config.CachePath)
	if err != nil {
		fmt.Fprintln(stderr, config.RedactDiagnostic(err.Error(), config.OSSource{}))
		return 1
	}
	defer store.Close()
	svc, err := resolveService(store, deps)
	if err != nil {
		fmt.Fprintln(stderr, config.RedactDiagnostic(err.Error(), config.OSSource{}))
		return 1
	}
	transport := mcp.NewHTTPSSETransport(mcp.NewRPCHandler(svc), mcp.ServerConfig{BindAddress: bind, ReadinessProbe: func(ctx context.Context) mcp.Readiness {
		repos, err := store.ListRepositories(ctx)
		if err != nil {
			var lockErr cache.ErrLockContention
			if errors.As(err, &lockErr) {
				return mcp.LockContentionReadiness(lockErr)
			}
			return mcp.Readiness{Ready: false, Code: "cache_unreadable", Message: err.Error()}
		}
		if len(repos) == 0 {
			return mcp.Readiness{Ready: false, Code: "repo_unavailable", Message: "no repositories configured"}
		}
		return mcp.Readiness{Ready: true}
	}})
	if err := transport.Serve(ctx); err != nil {
		fmt.Fprintln(stderr, config.RedactDiagnostic(err.Error(), config.OSSource{}))
		return 1
	}
	return 0
}

func ensureParentDir(path string) error {
	if path == "" || path == ":memory:" {
		return nil
	}
	dir := filepath.Dir(path)
	if dir == "." || dir == "" {
		return nil
	}
	return os.MkdirAll(dir, 0o700)
}

func printStartupHelp(w io.Writer) {
	fmt.Fprintln(w, "Usage: gitcode-mcp [global flags] <command> [args]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Global flags:")
	fmt.Fprintln(w, "  --live                enable live GitCode API provider (requires GITCODE_TOKEN)")
	fmt.Fprintln(w, "  --mcp                 run stdio MCP server")
	fmt.Fprintln(w, "  mcp serve             run MCP server with stdio or HTTP/SSE transport")
	fmt.Fprintln(w, "  --cache-path PATH     cache database path")
	fmt.Fprintln(w, "  --timeout DURATION    startup default timeout")
	fmt.Fprintln(w, "  --max-size BYTES      maximum GitCode response size")
	fmt.Fprintln(w, "  --format FORMAT       default output format")
	fmt.Fprintln(w, "  --version             print version")
	fmt.Fprintln(w, "  -h, --help            show help")
}

func printMCPHelp(w io.Writer) {
	fmt.Fprintln(w, "Usage: gitcode-mcp --mcp [global flags]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Starts stdio MCP mode. stdout is reserved for JSON-RPC frames.")
}

func printMCPServeHelp(w io.Writer) {
	fmt.Fprintln(w, "Usage: gitcode-mcp mcp serve --transport stdio|http-sse [--bind 127.0.0.1:PORT]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Starts MCP mode over stdio or HTTP/SSE.")
}
