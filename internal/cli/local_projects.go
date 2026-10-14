package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/localgit"
	"gitcode-mcp/internal/service"
	"gitcode-mcp/internal/servicectl"
)

func executeProjectCommand(ctx context.Context, args []string, stdout, stderr io.Writer, deps localCommandDeps) int {
	writeError := writeLocalProjectError
	invalid := func() error { return &localgit.Error{Code: "invalid_argument", Field: "command"} }
	for _, arg := range args {
		if !utf8.ValidString(arg) {
			return writeError(stderr, "json", invalid())
		}
	}
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(stdout, "project init|register|status|get|list|history|mutate --project SELECTOR [--format json]\nSetup: --project-id ID --storage-ref REF --confirm; global YAML only. Mutate: --operation OP --write-mode local --idempotency-key KEY --expected-head OID or --expected-revision OID. No implicit daemon start.")
		return 0
	}
	verb := args[0]
	fs := flag.NewFlagSet("project", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var q service.LocalProjectQuery
	var mut localgit.Mutation
	var setup service.LocalProjectSetupRequest
	var format, kind, labels, links, bodyFile string
	fs.StringVar(&q.Project, "project", "", "opaque project selector")
	fs.StringVar(&format, "format", "json", "json or text")
	fs.StringVar(&kind, "kind", "", "issue, comment or wiki")
	fs.StringVar(&q.ID, "id", "", "stable entity id")
	fs.IntVar(&q.Offset, "offset", 0, "list offset")
	fs.IntVar(&q.Limit, "limit", 20, "list bound")
	fs.IntVar(&q.MaxCommits, "max-commits", 64, "history bound")
	fs.StringVar(&setup.ProjectID, "project-id", "", "stable project identity")
	fs.StringVar(&setup.StorageRef, "storage-ref", "", "opaque managed storage reference")
	fs.StringVar(&setup.Ref, "ref", "refs/heads/main", "local branch")
	fs.BoolVar(&setup.ReadOnly, "read-only", false, "read-only registration")
	fs.BoolVar(&setup.Confirm, "confirm", false, "confirm setup and strict-local configuration")
	fs.StringVar(&mut.Operation, "operation", "", "fixed typed mutation")
	fs.StringVar(&mut.ParentID, "parent-id", "", "parent issue for new comment")
	fs.StringVar(&mut.Title, "title", "", "entity title")
	fs.StringVar(&mut.Body, "body", "", "UTF-8 body")
	fs.StringVar(&bodyFile, "body-file", "", "CLI-only body file, or - for stdin")
	fs.StringVar(&mut.State, "state", "", "entity state")
	fs.StringVar(&labels, "labels", "", "comma-separated issue labels")
	fs.StringVar(&mut.Milestone, "milestone", "", "issue milestone metadata")
	fs.StringVar(&links, "links", "", "JSON typed kind/id array")
	fs.StringVar(&mut.ExpectedHead, "expected-head", "", "full commit precondition")
	fs.StringVar(&mut.ExpectedRevision, "expected-revision", "", "entity blob precondition")
	fs.StringVar(&mut.IdempotencyKey, "idempotency-key", "", "caller retry key")
	fs.StringVar(&mut.WriteMode, "write-mode", "", "requires local")
	parseErr := fs.Parse(args[1:])
	help := errors.Is(parseErr, flag.ErrHelp)
	if parseErr != nil && !help || fs.NArg() != 0 || (format != "json" && format != "text") {
		return writeError(stderr, "json", invalid())
	}
	allowed := map[string]bool{"project": true, "format": true}
	var fields string
	switch verb {
	case "init", "register":
		fields = "project-id storage-ref ref read-only confirm"
	case "status":
	case "get":
		fields = "kind id"
	case "list":
		fields = "kind offset limit"
	case "history":
		fields = "kind id max-commits"
	case "mutate":
		fields = "operation id parent-id title body body-file state labels milestone links expected-head expected-revision idempotency-key write-mode"
	default:
		return writeError(stderr, format, invalid())
	}
	for _, key := range strings.Fields(fields) {
		allowed[key] = true
	}
	if help {
		fmt.Fprintf(stdout, "Usage: gitcode-mcp project %s [flags]\n", verb)
		fs.VisitAll(func(f *flag.Flag) {
			if allowed[f.Name] {
				fmt.Fprintf(stdout, "  --%s  %s\n", f.Name, f.Usage)
			}
		})
		return 0
	}
	bad := false
	fs.Visit(func(f *flag.Flag) {
		if !allowed[f.Name] {
			bad = true
		}
	})
	seen := map[string]bool{}
	for i := 1; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		key, _, inline := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		f := fs.Lookup(key)
		if f == nil {
			bad = true
			break
		}
		if seen[key] {
			bad = true
		}
		seen[key] = true
		if !inline {
			b, isBool := f.Value.(interface{ IsBoolFlag() bool })
			if !isBool || !b.IsBoolFlag() {
				i++
			}
		}
	}
	if bad || (q.Project == "" && verb != "status") {
		return writeError(stderr, format, invalid())
	}
	if verb == "init" || verb == "register" {
		setup.Project = q.Project
		setup.Initialize = verb == "init"
		result, err := service.SetupLocalProject(ctx, deps.Source, setup)
		if err != nil {
			return writeError(stderr, format, err)
		}
		if encodeLocalProject(stdout, format, result) != nil {
			return writeError(stderr, format, &localgit.Error{Code: "unavailable", Field: "output"})
		}
		return 0
	}
	eff, err := config.LoadEffective(deps.Source, config.Overrides{})
	if err != nil {
		return writeError(stderr, format, config.LocalProjectConfigError{})
	}
	if eff.Config.Service.Mode != config.ServiceModeLocal {
		return writeError(stderr, format, &localgit.Error{Code: "invalid_configuration", Field: "local_mode_required"})
	}
	client, err := (servicectl.Manager{Source: deps.Source, RuntimeDir: eff.Config.Service.RuntimeDir}).Client()
	if err != nil {
		return writeError(stderr, format, &localgit.Error{Code: "unavailable", Field: "service"})
	}
	q.Kind = localgit.Kind(kind)
	var params, result any
	method := "LocalProjects."
	switch verb {
	case "status":
		if q.Project != "" {
			return writeError(stderr, format, invalid())
		}
		method += "Status"
		params = struct{}{}
		result = &service.LocalProjectsStatus{}
	case "get":
		method += "Get"
		params = q
		result = &localgit.Entity{}
	case "list":
		method += "List"
		params = q
		result = &[]localgit.Entity{}
	case "history":
		method += "History"
		params = q
		result = &[]localgit.Citation{}
	case "mutate":
		if bodyFile != "" {
			if seen["body"] {
				return writeError(stderr, format, invalid())
			}
			reader := deps.Stdin
			if bodyFile != "-" {
				f, e := os.Open(bodyFile)
				if e != nil {
					return writeError(stderr, format, invalid())
				}
				defer f.Close()
				reader = f
			}
			if reader == nil {
				return writeError(stderr, format, invalid())
			}
			body, e := io.ReadAll(io.LimitReader(reader, localgit.MaxFileBytes+1))
			if e != nil || len(body) > localgit.MaxFileBytes || !utf8.Valid(body) {
				return writeError(stderr, format, invalid())
			}
			mut.Body = string(body)
		}
		if labels != "" {
			mut.Labels = strings.Split(labels, ",")
		}
		if links != "" && servicectl.DecodeLocalProjectJSON([]byte(links), &mut.Links) != nil {
			return writeError(stderr, format, invalid())
		}
		mut.ID = q.ID
		method += "Mutate"
		params = service.LocalProjectMutation{Project: q.Project, Mutation: mut}
		result = &localgit.MutationResult{}
	}
	if err = client.Call(ctx, method, params, result); err != nil {
		return writeError(stderr, format, err)
	}
	if encodeLocalProject(stdout, format, result) != nil {
		code := "unavailable"
		if verb == "mutate" {
			code = "publication_ambiguous"
		}
		return writeError(stderr, format, &localgit.Error{Code: code, Field: "output"})
	}
	return 0
}

func encodeLocalProject(w io.Writer, format string, v any) error {
	enc := json.NewEncoder(w)
	if format == "text" {
		enc.SetIndent("", "  ")
	}
	return enc.Encode(v)
}

func writeLocalProjectError(w io.Writer, format string, err error) int {
	code := "local_git_unavailable"
	var coded interface{ DiagnosticCode() string }
	if errors.As(err, &coded) {
		code = coded.DiagnosticCode()
	} else if errors.Is(err, context.Canceled) {
		code = "operation_cancelled"
	} else if errors.Is(err, context.DeadlineExceeded) {
		code = "operation_timeout"
	}
	message := "local project operation could not be completed; inspect project status/configuration, explicitly start the daemon if needed; preserve the same mutation key after ambiguity"
	var local *localgit.Error
	if errors.As(err, &local) && local.Field == "global_yaml_required" {
		message = "project setup requires trusted global YAML; select it with GITCODE_MCP_CONFIG, then repeat the confirmed setup"
	}
	if format == "json" {
		_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "message": message})
	} else {
		fmt.Fprintln(w, code+": "+message)
	}
	return 1
}
