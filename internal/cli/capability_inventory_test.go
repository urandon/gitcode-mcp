package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"

	"gitcode-mcp/internal/capability"
)

func TestEveryCLICommandAndDispatchAliasDeclaresSurfacePolicy(t *testing.T) {
	declared := map[string]bool{}
	for _, cap := range capability.Capabilities() {
		if !cap.CLI.Enabled {
			continue
		}
		for _, name := range append([]string{cap.CLIName}, cap.CLIAliases...) {
			declared[strings.Fields(name)[0]] = true
		}
	}
	actual := map[string]bool{"project": true} // Deliberate pre-bootstrap local profile router.
	for _, name := range commands {
		actual[name] = true
	}
	// Inspect actual query dispatch as well as discovery: adding a hidden alias
	// without updating `commands` must not bypass the contract gate.
	file, err := parser.ParseFile(token.NewFileSet(), "cli.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	foundDispatch := false
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "dispatch" {
			continue
		}
		foundDispatch = true
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			sw, ok := node.(*ast.SwitchStmt)
			if !ok {
				return true
			}
			id, ok := sw.Tag.(*ast.Ident)
			if !ok || id.Name != "command" {
				return true
			}
			for _, clause := range sw.Body.List {
				for _, expr := range clause.(*ast.CaseClause).List {
					if literal, ok := expr.(*ast.BasicLit); ok && literal.Kind == token.STRING {
						name, err := strconv.Unquote(literal.Value)
						if err != nil {
							t.Fatal(err)
						}
						actual[name] = true
					}
				}
			}
			return false
		})
	}
	if !foundDispatch {
		t.Fatal("query dispatch inventory target missing")
	}
	for name := range actual {
		if name == "tasks" || name == "tracks" {
			// Historical dispatch arms are unreachable: neither command is
			// accepted by root discovery. Do not promote them into new features.
			if isKnownCommand(name) {
				t.Fatalf("legacy %s became reachable without a declared capability", name)
			}
			continue
		}
		if !declared[name] {
			t.Errorf("command/alias %q lacks an enabled capability declaration", name)
		}
	}
	for name := range declared {
		if !actual[name] {
			t.Errorf("declared CLI root %q is not registered", name)
		}
	}
}

func TestGroupedCLIFormsDeclareCapabilityAndExceptions(t *testing.T) {
	caps := map[string]capability.Capability{}
	for _, cap := range capability.Capabilities() {
		caps[cap.ID] = cap
	}
	declared := map[string]bool{}
	for id, forms := range capability.CLIGroupedForms() {
		cap, ok := caps[id]
		if !ok || !cap.CLI.Enabled || len(forms) == 0 {
			t.Fatalf("invalid grouped capability %s", id)
		}
		for _, form := range forms {
			if declared[form] {
				t.Fatalf("duplicate grouped form %s", form)
			}
			declared[form] = true
		}
	}
	actual := map[string]bool{}
	// These are the real grouped dispatch/argument-validation switches. The
	// combined local router also includes help declarations for if-routed verbs.
	for _, target := range []struct{ file, function, root, tag string }{
		{"cli.go", "executeLocalCommand", "", "combined"},
		{"cli.go", "executeServiceCommand", "service", "sub"},
		{"cli.go", "executeMaintenanceCommand", "maintenance", "sub"},
		{"cli.go", "executeRAGCommand", "rag", "sub"},
		{"cli.go", "executeRepositoryDocsCommand", "repo-docs", "sub"},
		{"cli.go", "executeAdminCommand", "admin", "sub"},
		{"cli.go", "dispatch", "repo", "sub"},
		{"local_projects.go", "executeProjectCommand", "project", "verb"},
		{"observation.go", "executeObservationCommand", "service", "sub"},
	} {
		file, err := parser.ParseFile(token.NewFileSet(), target.file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name.Name != target.function {
				continue
			}
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				// Some grouped routers use if/validation predicates instead of
				// switch arms. Include literal equality/inequality on the actual
				// verb variable, not only their optional help declarations.
				if verb, ok := inventoryPredicateVerb(node, target.tag); ok {
					actual[strings.TrimSpace(target.root+" "+verb)] = true
					found = true
				}
				sw, ok := node.(*ast.SwitchStmt)
				if !ok {
					return true
				}
				if target.tag == "combined" {
					if _, ok := sw.Tag.(*ast.BinaryExpr); !ok {
						return true
					}
				} else if id, ok := sw.Tag.(*ast.Ident); !ok || id.Name != target.tag {
					return true
				}
				found = true
				for _, clause := range sw.Body.List {
					for _, expr := range clause.(*ast.CaseClause).List {
						literal, ok := expr.(*ast.BasicLit)
						if !ok || literal.Kind != token.STRING {
							continue
						}
						verb, err := strconv.Unquote(literal.Value)
						if err != nil {
							t.Fatal(err)
						}
						form := strings.TrimSpace(target.root + " " + verb)
						// dispatch has two nested sub switches (cache and repo).
						if target.function == "dispatch" && verb == "reset" {
							form = "cache reset"
						}
						actual[form] = true
					}
				}
				return false
			})
		}
		if !found {
			t.Fatalf("grouped dispatch target missing: %s", target.function)
		}
	}
	// Feedback preparation/submission use the typed write dispatcher rather than
	// a verb switch; both remain distinct declared read/write capabilities.
	actual["feedback prepare"], actual["feedback submit"] = true, true
	for form := range actual {
		if !declared[form] {
			t.Errorf("grouped form %q lacks capability declaration", form)
		}
	}
	for form := range declared {
		if !actual[form] {
			t.Errorf("declared grouped form %q lacks dispatch/help coverage", form)
		}
	}
}

func inventoryPredicateVerb(node ast.Node, tag string) (string, bool) {
	comparison, ok := node.(*ast.BinaryExpr)
	if !ok || (comparison.Op != token.EQL && comparison.Op != token.NEQ) {
		return "", false
	}
	for _, pair := range [][2]ast.Expr{{comparison.X, comparison.Y}, {comparison.Y, comparison.X}} {
		id, isVerb := pair[0].(*ast.Ident)
		literal, isLiteral := pair[1].(*ast.BasicLit)
		if isVerb && id.Name == tag && isLiteral && literal.Kind == token.STRING {
			verb, err := strconv.Unquote(literal.Value)
			return verb, err == nil
		}
	}
	return "", false
}

func TestGroupedInventoryFindsHiddenIfAliasesWithoutHelp(t *testing.T) {
	for _, tc := range []struct{ expression, want string }{
		{`sub == "new-hidden-alias"`, "new-hidden-alias"},
		{`"rebind" == sub`, "rebind"},
		{`sub != "plan"`, "plan"},
		{`other == "not-a-verb"`, ""},
		{`sub == dynamicValue`, ""},
	} {
		expr, err := parser.ParseExpr(tc.expression)
		if err != nil {
			t.Fatal(err)
		}
		got, found := inventoryPredicateVerb(expr, "sub")
		if got != tc.want || found != (tc.want != "") {
			t.Fatal("if-routed command escaped verb inventory")
		}
	}
}
