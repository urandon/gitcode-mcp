package observability

// MetricDefinition is a closed product contract, not a registration API.
// Dimension values are finite enums; entity correlation belongs in events.
type MetricDefinition struct {
	Name         string              `json:"name"`
	Type         string              `json:"type"`
	Unit         string              `json:"unit"`
	Origin       string              `json:"origin"`
	Reset        string              `json:"reset"`
	Window       string              `json:"window"`
	Availability string              `json:"availability"`
	Dimensions   map[string][]string `json:"dimensions"`
	Bounds       []float64           `json:"bounds,omitempty"`
}

var durationBounds = []float64{.01, .05, .1, .5, 1, 5, 15, 60, 300, 1800}
var workKinds = []string{"sync", "rag", "repository_docs", "other"}
var outcomes = []string{"succeeded", "failed", "cancelled", "interrupted", "superseded"}
var collections = []string{"issues", "issue_comments", "wiki", "pulls", "pr_comments", "other"}
var providerOutcomes = []string{"succeeded", "timeout", "cancelled", "rate_limited", "forbidden", "invalid", "failed"}
var adminRoutes = []string{"session", "read", "control", "search", "events", "assets", "unknown"}

func metricDefinitions() []MetricDefinition {
	g := func(name, unit, origin string) MetricDefinition {
		return MetricDefinition{Name: name, Type: "gauge", Unit: unit, Origin: origin, Reset: "sample", Window: "15s", Availability: "null_until_successful_probe", Dimensions: map[string][]string{}}
	}
	c := func(name, unit, origin string, dimensions map[string][]string) MetricDefinition {
		return MetricDefinition{Name: name, Type: "counter", Unit: unit, Origin: origin, Reset: "boot", Window: "boot", Availability: "null_until_origin_attached", Dimensions: dimensions}
	}
	h := func(name, origin string, dimensions map[string][]string) MetricDefinition {
		d := c(name, "seconds", origin, dimensions)
		d.Type = "histogram"
		d.Bounds = durationBounds
		return d
	}
	return []MetricDefinition{
		g("daemon_uptime", "seconds", "runtime"), g("runtime_heap", "bytes", "runtime"), g("runtime_goroutines", "count", "runtime"), g("runtime_gc_pause", "seconds", "runtime"),
		g("jobs_queued", "count", "jobs"), g("jobs_active", "count", "jobs"), g("writer_waiters", "count", "writer"), g("retry_scheduled", "count", "maintenance"), g("sse_connections", "count", "admin"),
		c("jobs_admitted", "jobs", "jobs", map[string][]string{"kind": workKinds}),
		c("jobs_terminal", "jobs", "jobs", map[string][]string{"kind": workKinds, "outcome": outcomes}),
		c("retry_admissions", "attempts", "maintenance", map[string][]string{"collection": collections}),
		c("provider_outcomes", "attempts", "provider", map[string][]string{"outcome": providerOutcomes}),
		c("writer_contention", "attempts", "writer", map[string][]string{"kind": workKinds}),
		c("admin_requests", "requests", "admin", map[string][]string{"route": adminRoutes, "outcome": []string{"succeeded", "rejected", "failed", "cancelled"}}),
		c("event_drops", "events", "collector", map[string][]string{}),
		c("sync_committed", "records", "sync", map[string][]string{"collection": collections}),
		c("rag_embedded", "chunks", "rag", map[string][]string{}),
		h("sync_duration", "sync", map[string][]string{"outcome": outcomes}), h("rag_duration", "rag", map[string][]string{"outcome": outcomes}),
		h("provider_duration", "provider", map[string][]string{"outcome": providerOutcomes}), h("admin_duration", "admin", map[string][]string{"route": adminRoutes}),
	}
}

// Catalog returns an isolated copy: callers cannot expand cardinality or edit
// histogram boundaries of the live registry through a returned DTO.
func MetricCatalog() []MetricDefinition {
	defs := metricDefinitions()
	for i := range defs {
		d := &defs[i]
		dimensions := make(map[string][]string, len(d.Dimensions))
		for key, values := range d.Dimensions {
			dimensions[key] = append([]string(nil), values...)
		}
		d.Dimensions = dimensions
		d.Bounds = append([]float64(nil), d.Bounds...)
	}
	return defs
}
