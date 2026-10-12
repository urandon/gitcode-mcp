package authority

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const exampleID UUID = "12345678-1234-4234-8234-123456789abc"
const exampleEntity UUID = "23456789-2345-4345-8345-23456789abcd"

const exampleIntent = `project:
  schema: 1
  slug: research-example
  authority: local_authoritative
  entities: [issue, comment, label, milestone, document, link]
  network: {data_egress: deny, control_transport: ipc_only}
  search: {default: hybrid, fallback: full_text}
`
const exampleRegistration = `storage_ref: workstation-store
writer: coordinator
backup: {retain: 7, max_bytes: 1073741824}
indexing: {interval_seconds: 900, max_chunks: 128, embedding_profile: disabled}
`

func requireError(t *testing.T, err error, code Code) {
	t.Helper()
	var e Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatal("unexpected authority error classification")
	}
}

func TestIntentAndInitializationContract(t *testing.T) {
	m, err := ParseIntent([]byte(exampleIntent))
	if err != nil {
		t.Fatal("synthetic manifest rejected")
	}
	r, err := ParseRegistration([]byte(exampleRegistration), m)
	if err != nil {
		t.Fatal("synthetic registration rejected")
	}
	requireError(t, func() error { _, e := PlanInitialization("admin", m, r); return e }(), PolicyDenied)
	p, err := PlanInitialization("cli", m, r)
	if err != nil || !p.ProjectID.Valid() || p.NetworkState != "zero_network" || p.Status != "planned_not_enrolled" || len(p.IntentDigest) != 64 {
		t.Fatal("initialization plan not explicit")
	}
	q, err := PlanInitialization("cli", p.Manifest, r)
	if err != nil || q.ProjectID != p.ProjectID || q.IntentDigest != p.IntentDigest {
		t.Fatal("fixed identity plan changed")
	}
	if m.ID != "" {
		t.Fatal("planning modified caller intent")
	}
}

func TestStrictLocalManifestRefusesMixedUnknownDuplicateAndAliases(t *testing.T) {
	for _, suffix := range []string{
		"cache_path: synthetic-private-path\n", "gitcode_base_url: https://example.invalid\n", "credential: {store: env}\n", "sync: true\n", "project: {schema: 1}\n", "---\nproject: {}\n",
	} {
		if _, err := ParseIntent([]byte(exampleIntent + suffix)); err == nil || strings.Contains(err.Error(), "synthetic-private-path") {
			t.Fatal("mixed or duplicate intent accepted or echoed")
		}
	}
	for _, change := range []struct{ old, new string }{
		{"schema: 1", "schema: 2"}, {"slug: research-example", "slug: owner/repo"}, {"control_transport: ipc_only", "control_transport: tcp"}, {"entities: [issue, comment, label, milestone, document, link]", "entities: [issue, issue, label, milestone, document, link]"}, {"authority: local_authoritative", "authority: remote_authoritative"}, {"search: {default: hybrid, fallback: full_text}", "search: &private {default: hybrid, fallback: full_text}"},
	} {
		if _, err := ParseIntent([]byte(strings.Replace(exampleIntent, change.old, change.new, 1))); err == nil {
			t.Fatal("invalid policy/schema/identity accepted")
		}
	}
	m, _ := ParseIntent([]byte(exampleIntent))
	for _, suffix := range []string{"cache_mode: global\n", "endpoint: https://example.invalid\n", "storage_ref: second-store\n"} {
		if _, err := ParseRegistration([]byte(exampleRegistration+suffix), m); err == nil {
			t.Fatal("registration accepted remote/cache/duplicate selector")
		}
	}
	for _, input := range []string{strings.Replace(exampleRegistration, "retain: 7", "retain: '7'", 1), strings.Replace(exampleRegistration, "storage_ref: workstation-store", "storage_ref: 123", 1)} {
		if _, err := ParseRegistration([]byte(input), m); err == nil {
			t.Fatal("local registration coerced a scalar type")
		}
	}
	_, err := ParseRegistration([]byte(strings.Replace(exampleRegistration, "disabled", "configured-profile", 1)), m)
	requireError(t, err, PolicyDenied)
}

func TestPolicyPreflightFailClosed(t *testing.T) {
	p := NetworkPolicy{"deny", "ipc_only"}
	good := RuntimeProfile{Protocol: Protocol, SchemaMin: 1, SchemaMax: 1, Strict: true, PrivateIPC: true}
	if err := Preflight(p, good); err != nil {
		t.Fatal("pure strict admission contract rejected")
	}
	for i := 0; i < 8; i++ {
		r := good
		switch i {
		case 0:
			r.Strict = false
		case 1:
			r.TCP = true
		case 2:
			r.UDP = true
		case 3:
			r.DNS = true
		case 4:
			r.Admin = true
		case 5:
			r.RemoteProjects = true
		case 6:
			r.LoopbackProjects = true
		case 7:
			r.Protocol = "old"
		}
		if err := Preflight(p, r); err == nil {
			t.Fatal("incompatible runtime admitted")
		}
	}
	if (NetworkPolicy{"deny", "loopback_admin"}).Label() != "loopback_control_only" || (NetworkPolicy{"loopback_embeddings", "ipc_only"}).Label() != "loopback_provider_network" {
		t.Fatal("loopback mislabeled zero-network")
	}
	n := ContractsOnly()
	if n.Available || len(n.Methods) != 0 || n.Reason != Unavailable {
		t.Fatal("unimplemented workflows advertised")
	}
}

func TestStrictJSONBoundaryAndCitation(t *testing.T) {
	for _, input := range []string{`null`, `{"project_id":"synthetic-secret","project_id":"other"}`, `{"unknown":"synthetic-secret"}`, `{"Project_ID":"synthetic-secret"}`, `{"project_id":null}`, `{} {}`, strings.Repeat(" ", MaxContractBytes+1)} {
		var r ReadRequest
		err := DecodeJSON([]byte(input), &r)
		requireError(t, err, InvalidContract)
		if strings.Contains(err.Error(), "synthetic-secret") {
			t.Fatal("private body echoed")
		}
	}
	c := Citation{exampleID, exampleEntity, Document, 1}
	if c.Validate() != nil {
		t.Fatal("immutable citation rejected")
	}
	c.Revision = 0
	requireError(t, c.Validate(), InvalidContract)
	for _, id := range []UUID{"owner/repo", "DOC-1", "00000000-0000-0000-0000-000000000000", "12345678-1234-4234-8234-123456789ABc"} {
		if id.Valid() {
			t.Fatal("display alias accepted as UUID")
		}
	}
}

func TestEntityMutationContracts(t *testing.T) {
	c := Citation{exampleID, exampleEntity, Issue, 1}
	for _, test := range []struct {
		kind    string
		payload any
	}{
		{"issue", IssuePayload{"Example", "Evidence", "open"}}, {"comment", CommentPayload{c, "Evidence"}}, {"label", LabelPayload{"Example", "#abcdef", false}}, {"milestone", MilestonePayload{"Example", "Evidence", "2026-12-31", false}}, {"document", DocumentPayload{"Example", "# Evidence"}}, {"link", LinkPayload{c, c, "relates_to"}},
	} {
		body, _ := json.Marshal(test.payload)
		r := MutationRequest{WriteIntent{ProjectID: exampleID, WriteMode: "local", IdempotencyKey: "synthetic-create"}, test.kind + ".create", body}
		if r.Validate() != nil {
			t.Fatal("initial entity create contract rejected")
		}
		r.Intent.WriteMode = "live"
		requireError(t, r.Validate(), InvalidContract)
		r.Intent.WriteMode = "local"
		r.Operation = test.kind + ".update"
		requireError(t, r.Validate(), InvalidContract)
		r.Intent.EntityID = exampleEntity
		r.Intent.ExpectedRevision = 1
		if r.Validate() != nil {
			t.Fatal("CAS update contract rejected")
		}
	}
}
