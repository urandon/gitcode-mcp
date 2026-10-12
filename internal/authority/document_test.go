package authority

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func documentRequestFixture(operation string) DocumentMutationRequest {
	r := DocumentMutationRequest{
		Schema:    DocumentContractSchema,
		Intent:    DocumentWriteIntent{ProjectID: exampleID, WriteMode: "local", IdempotencyKey: "synthetic-document", ExpectedRefOID: EmptyKnowledgeRef},
		Operation: operation, Payload: json.RawMessage(`{"title":"Example","markdown":"# Evidence\r\nexact bytes"}`),
	}
	if operation != "document.create" {
		r.Intent.EntityID, r.Intent.ExpectedRevision = exampleEntity, 1
		r.Intent.ExpectedRefOID = strings.Repeat("a", 64)
	}
	if operation == "document.tombstone" {
		r.Payload = json.RawMessage(`{}`)
	}
	return r
}

func TestDocumentRequestRoundTripAndLegacyIsolation(t *testing.T) {
	for _, operation := range []string{"document.create", "document.update", "document.tombstone"} {
		r := documentRequestFixture(operation)
		if r.Validate() != nil {
			t.Fatal("synthetic document request rejected")
		}
		body, err := json.Marshal(r)
		if err != nil {
			t.Fatal("request encoding failed")
		}
		var decoded DocumentMutationRequest
		if DecodeJSON(body, &decoded) != nil || !reflect.DeepEqual(r, decoded) {
			t.Fatal("strict document round trip changed request")
		}
		var direct DocumentMutationRequest
		if json.Unmarshal(body, &direct) != nil || !reflect.DeepEqual(r, direct) {
			t.Fatal("direct decoder bypassed document contract")
		}
		// Removing the new top-level schema must not admit expected_ref_oid into
		// an old generic envelope; an older dispatch cannot infer document CAS.
		legacy := strings.Replace(string(body), `"schema":1,`, "", 1)
		var generic MutationRequest
		requireError(t, DecodeJSON([]byte(legacy), &generic), InvalidContract)
	}
	if Schema != 1 || ContractsOnly().Available || len(ContractsOnly().Methods) != 0 {
		t.Fatal("document DTO activated or changed legacy transport")
	}
}

func TestDocumentRequestStrictBoundary(t *testing.T) {
	create, _ := json.Marshal(documentRequestFixture("document.create"))
	update, _ := json.Marshal(documentRequestFixture("document.update"))
	var future DocumentMutationRequest
	requireError(t, DecodeJSON([]byte(strings.Replace(string(create), `"schema":1`, `"schema":2`, 1)), &future), Unsupported)
	for _, test := range []struct{ name, body string }{
		{"schema missing", strings.Replace(string(create), `"schema":1,`, "", 1)},
		{"schema null", strings.Replace(string(create), `"schema":1`, `"schema":null`, 1)},
		{"schema future", strings.Replace(string(create), `"schema":1`, `"schema":2`, 1)},
		{"schema duplicate", strings.Replace(string(create), `"schema":1`, `"schema":1,"schema":1`, 1)},
		{"ref missing", strings.Replace(string(create), `,"expected_ref_oid":"`+EmptyKnowledgeRef+`"`, "", 1)},
		{"ref null", strings.Replace(string(create), `"expected_ref_oid":"`+EmptyKnowledgeRef+`"`, `"expected_ref_oid":null`, 1)},
		{"ref abbreviated", strings.Replace(string(create), EmptyKnowledgeRef, "0000000", 1)},
		{"ref uppercase", strings.Replace(string(update), strings.Repeat("a", 64), strings.Repeat("A", 64), 1)},
		{"unborn update", strings.Replace(string(update), strings.Repeat("a", 64), EmptyKnowledgeRef, 1)},
		{"create entity placeholder", strings.Replace(string(create), `"write_mode":"local"`, `"entity_id":"","write_mode":"local"`, 1)},
		{"create revision placeholder", strings.Replace(string(create), `"write_mode":"local"`, `"expected_revision":0,"write_mode":"local"`, 1)},
		{"create entity null", strings.Replace(string(create), `"write_mode":"local"`, `"entity_id":null,"write_mode":"local"`, 1)},
		{"update revision null", strings.Replace(string(update), `"expected_revision":1`, `"expected_revision":null`, 1)},
		{"update revision missing", strings.Replace(string(update), `,"expected_revision":1`, "", 1)},
		{"revision overflow", strings.Replace(string(update), `"expected_revision":1`, `"expected_revision":9223372036854775808`, 1)},
		{"unknown intent", strings.Replace(string(create), `"write_mode":"local"`, `"private":"synthetic-secret","write_mode":"local"`, 1)},
		{"unknown payload", strings.Replace(string(create), `"title":"Example"`, `"private":"synthetic-secret","title":"Example"`, 1)},
		{"duplicate payload", strings.Replace(string(create), `"title":"Example"`, `"title":"Example","title":"Other"`, 1)},
		{"payload null", strings.Replace(string(create), string(documentRequestFixture("document.create").Payload), "null", 1)},
		{"unsupported operation", strings.Replace(string(create), "document.create", "issue.create", 1)},
		{"remote write", strings.Replace(string(create), `"local"`, `"live"`, 1)},
		{"suffix", string(create) + `{}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			prior := documentRequestFixture("document.update")
			decoded := prior
			err := DecodeJSON([]byte(test.body), &decoded)
			if err == nil || strings.Contains(err.Error(), "synthetic-secret") || !reflect.DeepEqual(decoded, prior) {
				t.Fatal("malformed request accepted, echoed or changed receiver")
			}
			if json.Unmarshal([]byte(test.body), &decoded) == nil {
				t.Fatal("direct decoder admitted malformed request")
			}
		})
	}
	tombstone := documentRequestFixture("document.tombstone")
	tombstone.Payload = json.RawMessage(`{"title":"Example"}`)
	requireError(t, tombstone.Validate(), InvalidContract)
	createRequest := documentRequestFixture("document.create")
	createRequest.Intent.ExpectedRefOID = strings.Repeat("b", 64)
	if createRequest.Validate() != nil {
		t.Fatal("create against an existing exact ref rejected")
	}
}

func documentResultFixture(tombstone bool) DocumentMutationResult {
	citation := Citation{ProjectID: exampleID, EntityID: exampleEntity, Kind: Document, Revision: 1}
	if tombstone {
		citation.Revision = 2
	}
	r := DocumentMutationResult{Schema: DocumentContractSchema, Durability: OrderedOSBarriersV1,
		Receipt: MutationReceipt{ProjectID: exampleID, OperationID: "34567890-3456-4456-8456-34567890abcd", Citation: citation, ProjectSequence: 1,
			IdempotencyKeyHash: strings.Repeat("a", 64), IntentHash: strings.Repeat("b", 64), Evidence: "local_commit_confirmed", ProjectionState: "pending"},
		Result: DocumentOutcome{State: "live", Locator: &DocumentLocator{Citation: citation, CommitOID: strings.Repeat("c", 64), BlobOID: strings.Repeat("d", 64), SHA256: strings.Repeat("e", 64), ByteLength: 10}},
	}
	if tombstone {
		r.Result = DocumentOutcome{State: "tombstone", Tombstone: &DocumentTombstoneEvidence{Citation: citation, OperationID: r.Receipt.OperationID, CommitOID: strings.Repeat("c", 64), ParentOID: strings.Repeat("f", 64)}}
	}
	return r
}

func TestDocumentResultStrictUnionAndReceiptBinding(t *testing.T) {
	for _, tombstone := range []bool{false, true} {
		r := documentResultFixture(tombstone)
		body, _ := json.Marshal(r)
		var future DocumentMutationResult
		requireError(t, DecodeJSON([]byte(strings.Replace(string(body), `"schema":1`, `"schema":2`, 1)), &future), Unsupported)
		var decoded DocumentMutationResult
		if r.Validate() != nil || DecodeJSON(body, &decoded) != nil || !reflect.DeepEqual(decoded, r) {
			t.Fatal("valid document result rejected or changed")
		}
		for _, alter := range []func(string) string{
			func(s string) string { return strings.Replace(s, `"schema":1,`, "", 1) },
			func(s string) string { return strings.Replace(s, `"schema":1`, `"schema":2`, 1) },
			func(s string) string { return strings.Replace(s, OrderedOSBarriersV1, "best_effort", 1) },
			func(s string) string {
				return strings.Replace(s, `"receipt":`, `"private":"synthetic-secret","receipt":`, 1)
			},
			func(s string) string { return strings.Replace(s, strings.Repeat("c", 64), EmptyKnowledgeRef, 1) },
			func(s string) string { return strings.Replace(s, strings.Repeat("c", 64), strings.Repeat("c", 40), 1) },
			func(s string) string { return strings.Replace(s, `"state":`, `"state":"invalid","state":`, 1) },
			func(s string) string {
				arm := "tombstone"
				if tombstone {
					arm = "locator"
				}
				return strings.Replace(s, `"state":`, `"`+arm+`":null,"state":`, 1)
			},
			func(s string) string {
				arm := "locator"
				if tombstone {
					arm = "tombstone"
				}
				var fields map[string]json.RawMessage
				_ = json.Unmarshal([]byte(s), &fields)
				fields["result"] = json.RawMessage(`{"state":"` + r.Result.State + `","` + arm + `":null}`)
				out, _ := json.Marshal(fields)
				return string(out)
			},
		} {
			err := DecodeJSON([]byte(alter(string(body))), &decoded)
			if err == nil || strings.Contains(err.Error(), "synthetic-secret") || !reflect.DeepEqual(decoded, r) {
				t.Fatal("malformed result accepted, echoed or changed receiver")
			}
			if json.Unmarshal([]byte(alter(string(body))), &decoded) == nil {
				t.Fatal("direct decoder bypassed result union")
			}
		}
		r.Receipt.Citation.Revision++
		requireError(t, r.Validate(), InvalidContract)
	}
	live := documentResultFixture(false)
	live.Result.Locator.ByteLength = 0
	requireError(t, live.Validate(), InvalidContract)
	live = documentResultFixture(false)
	live.Result.Locator.BlobOID = EmptyKnowledgeRef
	requireError(t, live.Validate(), InvalidContract)
	tombstone := documentResultFixture(true)
	tombstone.Result.Tombstone.OperationID = exampleEntity
	requireError(t, tombstone.Validate(), InvalidContract)
	tombstone = documentResultFixture(true)
	tombstone.Result.Tombstone.ParentOID = tombstone.Result.Tombstone.CommitOID
	requireError(t, tombstone.Validate(), InvalidContract)
	tombstone = documentResultFixture(true)
	tombstone.Result.Tombstone.Citation.Revision = 1
	tombstone.Receipt.Citation = tombstone.Result.Tombstone.Citation
	requireError(t, tombstone.Validate(), InvalidContract)
}
