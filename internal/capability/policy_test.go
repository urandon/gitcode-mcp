package capability

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestPolicyMatrix(t *testing.T) {
	parse := func(raw string) *Specification {
		t.Helper()
		s, ok, e := ParsePolicyDocument([]byte(raw))
		if e != nil || !ok {
			t.Fatalf("parse: %v", e)
		}
		return s
	}
	for _, tt := range []struct {
		name, rule string
		allowed    bool
	}{{"absent", "{}", true}, {"empty", "{allow: []}", false}, {"allow", "{allow: [get_source]}", true}, {"deny-wins", "{allow: [get_source], deny: [get_source]}", false}} {
		t.Run(tt.name, func(t *testing.T) {
			p, e := CompilePolicy(false, PolicyLayer{Specification: parse("capabilities: {schema: 1, ceiling: " + tt.rule + "}"), Source: "machine"})
			if e != nil {
				t.Fatal(e)
			}
			if (p.Check("get_source") == nil) != tt.allowed {
				t.Fatal(p.View())
			}
			if p.Check("unknown") == nil {
				t.Fatal("unmapped grant")
			}
		})
	}
	if p, e := CompilePolicy(false); e != nil || p != nil {
		t.Fatal("legacy changed")
	}
	s := parse("capabilities: {schema: 1, profile: research, profiles: {research: {allow: [get_source, add_issue_comment, repo_status]}}}")
	p, e := CompilePolicy(true, PolicyLayer{Specification: s, Source: "machine"})
	if e != nil {
		t.Fatal(e)
	}
	if p.Check("get_source") != nil || p.Check("add_issue_comment") == nil {
		t.Fatal("read ceiling lost")
	}
	if p.Require("ipc", "unknown") == nil || p.Require("mcp", "merge_pr") == nil {
		t.Fatal("route bypass")
	}
	v := p.View()
	v.Decisions[0].Allowed = !v.Decisions[0].Allowed
	v.Sources[0] = "altered"
	if p.View().Sources[0] != "machine" || p.View().Decisions[0] == v.Decisions[0] {
		t.Fatal("mutable view")
	}
	q, e := CompilePolicy(false, PolicyLayer{Specification: parse("capabilities: {schema: 1, ceiling: {deny: [merge_pr]}}"), Source: "machine"}, PolicyLayer{Specification: parse("capabilities: {schema: 1, ceiling: {allow: [merge_pr, get_source]}}"), Source: "deployment"})
	if e != nil {
		t.Fatal(e)
	}
	if q.Check("merge_pr") == nil || q.Check("get_source") != nil {
		t.Fatal("ceiling widened")
	}
}

func TestPolicyRejectsInvalidWithoutEcho(t *testing.T) {
	for _, raw := range []string{
		"capabilities: null", "capabilities: {}", "capabilities: {schema: 2}", "capabilities: {schema: 1, extra: private_secret}",
		"capabilities: {schema: 1, ceiling: {allow: null}}", "capabilities: {schema: 1, ceiling: {allow: get_source}}",
		"capabilities: {schema: 1, ceiling: {allow: [get_source, get_source]}}", "capabilities: {schema: 1, ceiling: {allow: [get_source], allow: []}}",
		"capabilities: {schema: 1, profile: unknown}", "capabilities: {schema: 1, profile: '../private_secret'}",
		"capabilities: {schema: 1}\n---\ncapabilities: {schema: 1}", "capabilities: {schema: 1}\ncapabilities: {schema: 1}",
	} {
		_, _, e := ParsePolicyDocument([]byte(raw))
		var typed PolicyError
		if !errors.As(e, &typed) || strings.Contains(e.Error(), "private_secret") {
			t.Fatalf("invalid result: %v", e)
		}
	}
	s, _, e := ParsePolicyDocument([]byte("capabilities: {schema: 1, profiles: {unused: {allow: [misspelled]}}}"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = CompilePolicy(false, PolicyLayer{Specification: s, Source: "machine"}); e == nil {
		t.Fatal("unknown inactive ID")
	}
}

func TestPolicyFutureCapabilitiesAndFingerprint(t *testing.T) {
	catalog := Capabilities()
	catalog = append(catalog, Capability{ID: "future", Safety: SafetyReadOnly})
	for _, tt := range []struct {
		rule   string
		future bool
	}{{"{allow: [get_source]}", false}, {"{deny: [merge_pr]}", true}} {
		s, _, e := ParsePolicyDocument([]byte("capabilities: {schema: 1, ceiling: " + tt.rule + "}"))
		if e != nil {
			t.Fatal(e)
		}
		p, e := compilePolicy(catalog, false, PolicyLayer{Specification: s, Source: "machine"})
		if e != nil {
			t.Fatal(e)
		}
		if (p.Check("future") == nil) != tt.future {
			t.Fatal("future behavior")
		}
		q, e := compilePolicy(catalog, false, PolicyLayer{Specification: s, Source: "machine"})
		if e != nil || p.Fingerprint() != q.Fingerprint() {
			t.Fatal("nondeterministic fingerprint")
		}
		encoded, _ := json.Marshal(p.View())
		if strings.Contains(string(encoded), ".yaml") {
			t.Fatal("path in view")
		}
	}
}

func TestPolicyRequirementCatalogIsClosed(t *testing.T) {
	known := map[string]bool{}
	for _, c := range Capabilities() {
		known[c.ID] = true
	}
	for surface, entries := range map[string]map[string][]string{"service": serviceRequirements, "ipc": ipcRequirements, "job": jobRequirements, "admin": adminRequirements, "dependency": capabilityDependencies} {
		for operation, ids := range entries {
			if len(ids) == 0 {
				t.Errorf("%s %s has no authority", surface, operation)
			}
			for _, id := range ids {
				if !known[id] {
					t.Errorf("%s %s maps to unknown %s", surface, operation, id)
				}
			}
		}
	}
	for route := range adminRoutes {
		if len(Requirements("admin_route", route)) == 0 {
			t.Errorf("unmapped admin route %s", route)
		}
	}
}

func TestPolicyParserBoundsAndDependencyClosure(t *testing.T) {
	for _, raw := range []string{strings.Repeat(" ", MaxPolicyDocumentBytes+1), "capabilities: {schema: 1, profile: " + strings.Repeat("x", 129) + "}", "capabilities: {schema: 1, ceiling: {allow: [" + strings.Repeat("get_source,", 513) + "]}}"} {
		if _, _, err := ParsePolicyDocument([]byte(raw)); err == nil {
			t.Fatal("policy bound not enforced")
		}
	}
	spec := &Specification{Ceiling: Rule{Allow: []string{"submit_feedback", "prepare_feedback", "repo_status"}}}
	for i := 0; i < 50; i++ {
		p, err := CompilePolicy(false, PolicyLayer{Source: "machine", Specification: spec})
		if err != nil {
			t.Fatal(err)
		}
		if p.Check("submit_feedback") == nil || p.Check("prepare_feedback") == nil {
			t.Fatal("transitive child denial ignored")
		}
	}
}

func TestPolicyConcreteGroupedAliasesKeepSameGrant(t *testing.T) {
	for _, tt := range []struct{ id, flat, group string }{{"rag_status", "rag-status", "rag status"}, {"rag_search", "rag-search", "rag search"}} {
		p, err := CompilePolicy(false, PolicyLayer{Source: "machine", Specification: &Specification{Ceiling: Rule{Allow: []string{tt.id}}}})
		if err != nil {
			t.Fatal(err)
		}
		if p.Require("cli", tt.flat) != nil || p.Require("cli", tt.group) != nil {
			t.Fatalf("standalone/grouped mapping diverged: %s", tt.id)
		}
		if p.Require("cli", "rag") == nil {
			t.Fatal("bare namespace became a grant")
		}
	}
	for _, cap := range Capabilities() {
		if !cap.CLI.Enabled {
			continue
		}
		for _, name := range append([]string{cap.CLIName}, cap.CLIAliases...) {
			if strings.Contains(name, " ") {
				continue
			}
			root := false
			for _, forms := range CLIGroupedForms() {
				for _, form := range forms {
					root = root || strings.Fields(form)[0] == name
				}
			}
			if !root && len(Requirements("cli", name)) == 0 {
				t.Errorf("concrete CLI spelling unmapped: %s", name)
			}
		}
	}
}

func TestPolicyAttachIncludesStatusDependency(t *testing.T) {
	for _, allow := range [][]string{{"service_job_attach"}, {"service_job_attach", "service_job_status"}} {
		p, err := CompilePolicy(false, PolicyLayer{Source: "machine", Specification: &Specification{Ceiling: Rule{Allow: allow}}})
		if err != nil {
			t.Fatal(err)
		}
		want := len(allow) == 2
		if (p.Require("cli", "service attach") == nil) != want || (p.Require("mcp", "service_job_attach") == nil) != want || (p.Require("ipc", "Jobs.Get") == nil) != want {
			t.Fatal("attach discovery/transport dependencies diverged")
		}
	}
}
