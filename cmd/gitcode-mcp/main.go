package main

import (
	"context"
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
	token           string
}

type startupOptions struct {
	mcp       bool
	help      bool
	version   bool
	overrides config.Overrides
}

type cliRouteFunc func(context.Context, []string, io.Writer, io.Writer, StartupDeps) int

type mcpRouteFunc func(context.Context, io.Reader, io.Writer, io.Writer, StartupDeps) int

var cliRoute cliRouteFunc = runCLICompatibility
var mcpRoute mcpRouteFunc = runMCPStdio

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
		return cli.ExecuteWithSource(rest, stdout, stderr, src)
	}

	cfg, err := config.Load(src, opts.overrides)
	if err != nil {
		fmt.Fprintln(stderr, config.RedactDiagnostic(err.Error(), src))
		return 1
	}
	deps := buildStartupDeps(cfg, config.Token(src))
	if opts.mcp {
		return mcpRoute(context.Background(), stdin, stdout, stderr, deps)
	}
	return cliRoute(context.Background(), rest, stdout, stderr, deps)
}

func buildStartupDeps(cfg config.Config, token string) StartupDeps {
	return StartupDeps{
		Config: cfg,
		Cache:  CacheStartup{CachePath: cfg.CachePath, LockPath: cfg.LockPath},
		GitCode: GitCodeStartup{
			BaseURL:         cfg.GitCodeBaseURL,
			DefaultTimeout:  cfg.DefaultTimeout,
			MaxResponseSize: cfg.MaxResponseSize,
			MaxRetries:      cfg.MaxRetries,
			token:           token,
		},
	}
}

func parseStartupArgs(args []string) (startupOptions, []string, error) {
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
		if strings.HasPrefix(arg, "-") {
			return opts, nil, fmt.Errorf("startup: unknown global flag %s", arg)
		}
		return opts, args[i:], nil
	}
	return opts, nil, nil
}

func startupValue(args []string, index int, flag string) (string, int, error) {
	if index+1 >= len(args) || args[index+1] == "" {
		return "", index, fmt.Errorf("startup: %s requires a value", flag)
	}
	return args[index+1], index + 1, nil
}

func runCLICompatibility(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer, deps StartupDeps) int {
	cliArgs := append([]string(nil), args...)
	if len(cliArgs) > 0 && deps.Config.CachePath != "" && !hasCLIFlag(cliArgs[1:], "--cache-path") {
		cliArgs = append(cliArgs, "--cache-path", deps.Config.CachePath)
	}
	if len(cliArgs) > 0 && deps.Config.Format != "" && !hasCLIFlag(cliArgs[1:], "--format") {
		cliArgs = append(cliArgs, "--format", deps.Config.Format)
	}
	_ = ctx
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
	server := mcp.New(stdin, stdout, stderr, service.New(store))
	if err := server.Serve(); err != nil {
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
	fmt.Fprintln(w, "  --mcp                 run stdio MCP server")
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
