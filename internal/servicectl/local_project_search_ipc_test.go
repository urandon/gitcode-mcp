package servicectl

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"gitcode-mcp/internal/localgit"
	"gitcode-mcp/internal/repositorydocs"
	"gitcode-mcp/internal/service"
)

func TestLocalProjectSearchWireRejectsMissingAndFalseReadiness(t *testing.T) {
	r := service.LocalProjectSearchResult{Project: "lab", ProjectID: "lab", Commit: strings.Repeat("a", 40), RequestedMode: "fulltext", EffectiveMode: "fulltext", IndexState: "not_requested", Coverage: repositorydocs.SearchCoverage{State: "lexical-only", EligibleFiles: 1, EligibleChunks: 1}, Hits: []service.LocalProjectSearchHit{{Citation: localgit.Citation{ProjectID: "lab", Kind: localgit.Issue, ID: "ISSUE-1", Commit: strings.Repeat("a", 40), Blob: strings.Repeat("b", 40), Path: "issues/ISSUE-1/issue.md"}, Snippet: "Fixture", LineStart: 1, LineEnd: 1, Digest: strings.Repeat("c", 64), Score: 0.1}}}
	raw, _ := json.Marshal(r)
	if _, valid := validateLocalSearchResult("LocalProjects.Search", raw); !valid {
		t.Fatal("valid lexical response rejected")
	}
	for _, mutate := range []func(map[string]any){
		func(x map[string]any) { delete(x, "commit") },
		func(x map[string]any) { x["cache_path"] = "private-coordinate" },
		func(x map[string]any) { x["hits"] = nil },
		func(x map[string]any) { x["fallback"] = "private-coordinate" },
		func(x map[string]any) {
			x["requested_mode"] = "hybrid"
			x["effective_mode"] = "hybrid"
			x["index_state"] = "ready"
		},
		func(x map[string]any) { delete(x["coverage"].(map[string]any), "eligible_files") },
		func(x map[string]any) { delete(x["hits"].([]any)[0].(map[string]any), "score") },
		func(x map[string]any) {
			x["hits"].([]any)[0].(map[string]any)["citation"].(map[string]any)["project_id"] = "other"
		},
		func(x map[string]any) { x["hits"].([]any)[0].(map[string]any)["digest"] = "invalid" },
	} {
		var fields map[string]any
		json.Unmarshal(raw, &fields)
		mutate(fields)
		bad, _ := json.Marshal(fields)
		if _, valid := validateLocalSearchResult("LocalProjects.Search", bad); valid {
			t.Fatal("malformed response or false semantic readiness accepted")
		}
	}
	index := service.LocalProjectIndexResult{Project: "lab", ProjectID: "lab", Commit: r.Commit, RevisionSetID: "repo-doc-set-" + strings.Repeat("d", 64), State: "ready", EligibleChunks: 1, EmbeddedChunks: 1}
	encoded, _ := json.Marshal(index)
	if _, valid := validateLocalSearchResult("LocalProjects.Index", encoded); !valid {
		t.Fatal("ready index response rejected")
	}
	index.FailedChunks = 1
	encoded, _ = json.Marshal(index)
	if _, valid := validateLocalSearchResult("LocalProjects.Index", encoded); valid {
		t.Fatal("contradictory ready counts accepted")
	}
}

func TestLocalProjectSearchIPCRequestShapeRejectsNullBeforeStore(t *testing.T) {
	server := RPCServer{LocalProjects: &service.LocalProjects{}}
	for _, q := range []struct{ method, raw string }{
		{"Search", `{"project":"lab"}`},
		{"Search", `{"project":"lab","query":null}`},
		{"Search", `{"project":"lab","query":"x","mode":null}`},
		{"Search", `{"project":"lab","query":"` + string([]byte{0xff}) + `"}`},
		{"Index", `{"project":"lab","write_mode":"local","max_chunks":null}`},
		{"Index", `{"project":"lab","write_mode":"local","cache_path":"private-coordinate"}`},
	} {
		_, err := server.dispatchLocalProject(context.Background(), "LocalProjects."+q.method, []byte(q.raw))
		coded, ok := err.(interface{ DiagnosticCode() string })
		if !ok || coded.DiagnosticCode() != "local_git_invalid_argument" {
			t.Fatal("invalid search/index request reached store resolution")
		}
	}
}
