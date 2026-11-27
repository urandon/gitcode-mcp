package service

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/capability"
	"gitcode-mcp/internal/config"
)

func TestCapabilityPolicyDeniesBeforeServiceEffects(t *testing.T) {
	spec, _, err := capability.ParsePolicyDocument([]byte("capabilities: {schema: 1, ceiling: {allow: []}}"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := capability.CompilePolicy(false, capability.PolicyLayer{Specification: spec, Source: "machine"})
	if err != nil {
		t.Fatal(err)
	}
	// Nil store/client/project bindings are intentional: a denied call must not
	// touch them, parse its untrusted intent or claim an idempotency key.
	s := &Service{policy: p}
	ctx := context.Background()
	if _, err := s.GetSource(ctx, GetSourceRequest{}); err == nil {
		t.Fatal("read executed")
	}
	if _, err := s.MergePR(ctx, WriteCommandRequest{}); err == nil {
		t.Fatal("write executed")
	}
	if _, err := s.BulkSyncAll(ctx, BulkSyncRequest{}); err == nil {
		t.Fatal("sync executed")
	}
	l := &LocalProjects{searchConfig: config.Config{CapabilityPolicy: p}}
	if _, err := l.Mutate(ctx, LocalProjectMutation{}); err == nil {
		t.Fatal("git mutation executed")
	}
	if err := s.BindCapabilityPolicy(nil); err == nil {
		t.Fatal("rebound managed service")
	}
}

func TestCapabilityPolicyAllowsExistingCachedReadContract(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "example/repo", Owner: "example", Name: "repo", APIBaseURL: "https://example.invalid/api/v5", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	if _, err := New(store).SyncToCache(ctx, SyncRequest{RepoID: "example/repo", RemoteAlias: "issue:42", IdempotencyKey: "seed-policy-read"}); err != nil {
		t.Fatal(err)
	}
	p, err := capability.CompilePolicy(false, capability.PolicyLayer{Source: "machine", Specification: &capability.Specification{Ceiling: capability.Rule{Allow: []string{"list_sources", "search_sources", "repo_status"}}}})
	if err != nil {
		t.Fatal(err)
	}
	svc := New(store)
	if err := svc.BindCapabilityPolicy(p); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ListSources(ctx, ListSourcesRequest{RepoID: "example/repo", Limit: 20}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SearchSources(ctx, SearchSourcesRequest{RepoID: "example/repo", Status: "open", Limit: 20}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RepositoryStatus(ctx, RepositoryStatusRequest{RepoID: "example/repo"}); err != nil {
		t.Fatal(err)
	}
}

// The service facade is an enforcement boundary, not just a transport filter.
// A new exported context entry must carry a first-statement guard; unknown
// operation mappings then fail closed rather than inherit namespace authority.
func TestCapabilityPolicyServiceEntryInventory(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || !fn.Name.IsExported() || fn.Body == nil || len(fn.Type.Params.List) == 0 {
				continue
			}
			recv, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
			if !ok {
				continue
			}
			name, ok := recv.X.(*ast.Ident)
			if !ok || name.Name != "Service" && name.Name != "LocalProjects" {
				continue
			}
			typ, ok := fn.Type.Params.List[0].Type.(*ast.SelectorExpr)
			if !ok || typ.Sel.Name != "Context" {
				continue
			}
			guard, ok := fn.Body.List[0].(*ast.IfStmt)
			if !ok || guard.Init == nil {
				t.Fatalf("unguarded entry %s", fn.Name.Name)
			}
			assign, ok := guard.Init.(*ast.AssignStmt)
			if !ok || len(assign.Rhs) != 1 {
				t.Fatal("invalid guard", fn.Name.Name)
			}
			call, ok := assign.Rhs[0].(*ast.CallExpr)
			if !ok {
				t.Fatal("invalid guard", fn.Name.Name)
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "authorizePolicy" {
				t.Fatal("invalid guard", fn.Name.Name)
			}
			if name.Name == "Service" && len(capability.Requirements("service", fn.Name.Name)) == 0 {
				t.Fatalf("unmapped service %s", fn.Name.Name)
			}
		}
	}
}
