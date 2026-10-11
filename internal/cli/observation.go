package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/observability"
	"gitcode-mcp/internal/service"
	"gitcode-mcp/internal/servicectl"
)

func IsObservationCommand(args []string) bool {
	if len(args) < 2 || args[0] != "service" {
		return false
	}
	return args[1] == "observe" || args[1] == "metrics" || args[1] == "events"
}

func executeObservationCommand(ctx context.Context, args []string, stdout, stderr io.Writer, deps localCommandDeps) int {
	sub := args[0]
	format := "text"
	help := false
	events := observability.EventsRequest{Limit: 50}
	metrics := observability.MetricsRequest{HistoryMinutes: 60}
	flags := flag.NewFlagSet("service "+sub, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&format, "format", "text", "text, markdown, or json")
	flags.BoolVar(&help, "help", false, "show read-only command help")
	flags.BoolVar(&help, "h", false, "show read-only command help")
	if sub == "metrics" {
		flags.StringVar(&metrics.Name, "name", "", "fixed metric name")
		flags.IntVar(&metrics.HistoryMinutes, "history-minutes", 60, "sampled history minutes (1..60)")
	}
	if sub == "events" {
		for _, f := range []struct {
			name  string
			value *string
		}{
			{"stream", &events.Stream}, {"severity", &events.Severity}, {"component", &events.Component}, {"phase", &events.Phase}, {"collection", &events.Collection}, {"outcome", &events.Outcome}, {"failure-class", &events.FailureClass},
			{"job-ref", &events.JobRef}, {"registration-ref", &events.RegistrationRef}, {"cache-ref", &events.CacheRef}, {"repo-ref", &events.RepoRef}, {"correlation-ref", &events.CorrelationRef}, {"stage-ref", &events.StageRef}, {"source-ref", &events.SourceRef}, {"previous-registration-ref", &events.PreviousRegistrationRef},
			{"since", &events.Since}, {"until", &events.Until}, {"cursor", &events.Cursor},
		} {
			flags.StringVar(f.value, f.name, "", "bounded event selector")
		}
		flags.Var(observationCodeFlag{&events.Code}, "code", "fixed event code")
		flags.IntVar(&events.Limit, "limit", 50, "result count (1..200)")
	}
	invalid := service.ErrInvalidQuery{Field: "observation", Message: "unknown, repeated, or invalid observation flag or positional argument"}
	// Standard flag parsing accepts repeats; this read boundary deliberately
	// refuses them before config or IPC access, including repeated booleans.
	seen := map[string]bool{}
	for i := 1; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") || a == "--" {
			return writeError(stderr, format, invalid)
		}
		name := strings.TrimLeft(strings.SplitN(a, "=", 2)[0], "-")
		f := flags.Lookup(name)
		if f == nil || seen[name] {
			return writeError(stderr, format, invalid)
		}
		seen[name] = true
		if name != "h" && name != "help" && !strings.Contains(a, "=") {
			i++
			if i >= len(args) {
				return writeError(stderr, format, invalid)
			}
		}
	}
	if err := flags.Parse(args[1:]); err != nil || len(flags.Args()) > 0 {
		return writeError(stderr, format, invalid)
	}
	if format != "text" && format != "markdown" && format != "json" {
		return writeError(stderr, "text", invalid)
	}
	if help {
		fmt.Fprintf(stdout, "Usage: gitcode-mcp service %s [--format text|markdown|json]\n", sub)
		flags.SetOutput(stdout)
		flags.PrintDefaults()
		fmt.Fprintln(stdout, "Read-only: one 2s budget; no cache/provider bootstrap, credential lookup, or daemon startup.")
		return 0
	}
	var params any
	var err error
	if sub == "events" {
		events, err = events.Validate()
		params = events
	}
	if sub == "metrics" {
		metrics, err = metrics.Validate()
		params = metrics
	}
	if err != nil {
		return writeError(stderr, format, err)
	}
	if deps.Source == nil {
		deps.Source = config.OSSource{}
	}
	effective, err := config.LoadEffective(deps.Source, config.Overrides{})
	if err != nil {
		return writeError(stderr, format, service.ErrInvalidQuery{Field: "config", Message: "effective configuration is invalid; inspect config diagnostics"})
	}
	manager := servicectl.Manager{Source: deps.Source, RuntimeDir: effective.Config.Service.RuntimeDir}
	client, err := manager.Client()
	if err != nil {
		return writeError(stderr, format, servicectl.RPCDomainError{Code: "service_unavailable", Message: "local daemon is unavailable; explicitly start it before retrying"})
	}
	switch sub {
	case "observe":
		var result observability.Summary
		err = client.Call(ctx, "Observability.Summary", nil, &result)
		if err == nil {
			return render(stdout, format, result, func(w io.Writer, s observability.Summary) {
				fmt.Fprintf(w, "protocol: %s\nboot: %s\nexecution: %s (%s)\nobservation: %s (%s)\n", s.Protocol, s.Collector.BootID, s.ExecutionHealth.State, s.ExecutionHealth.Coverage, s.ObservationHealth.State, s.ObservationHealth.Reason)
				for _, p := range s.Resources {
					fmt.Fprintf(w, "%s: %s\n", p.Name, observationMetricValue(p))
				}
			})
		}
	case "metrics":
		var result observability.MetricsSnapshot
		err = client.Call(ctx, "Observability.Metrics", params, &result)
		if err == nil {
			return render(stdout, format, result, func(w io.Writer, s observability.MetricsSnapshot) {
				fmt.Fprintf(w, "boot: %s\nstate: %s\nsampled_at: %s\n", s.BootID, s.State, s.SampledAt)
				for _, p := range s.Series {
					fmt.Fprintf(w, "%s: %s (history: %d minutes)\n", p.Name, observationMetricValue(p.MetricPoint), len(p.History))
				}
			})
		}
	case "events":
		var result observability.Page
		err = client.Call(ctx, "Observability.Events", params, &result)
		if err == nil {
			return render(stdout, format, result, func(w io.Writer, p observability.Page) {
				fmt.Fprintf(w, "coverage: %s\nhas_more: %t\ncursor: %s\n", p.Coverage.State, p.HasMore, p.Cursor)
				for _, e := range p.Events {
					fmt.Fprintf(w, "%d %s %s %s %s\n", e.Sequence, e.Code, e.Phase, e.Outcome, e.Message)
				}
			})
		}
	}
	return writeError(stderr, format, err)
}

type observationCodeFlag struct{ value *observability.Code }

func (f observationCodeFlag) String() string { return string(*f.value) }
func (f observationCodeFlag) Set(value string) error {
	*f.value = observability.Code(value)
	return nil
}
func observationMetricValue(p observability.MetricPoint) string {
	if p.Value == nil {
		return "unavailable (" + p.Reason + ")"
	}
	return fmt.Sprintf("%g", *p.Value)
}
