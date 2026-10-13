package localauthority

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gitcode-mcp/internal/authority"
)

func TestIndexAdmissionBindsOriginalKeyAndSequenceWithoutEcho(t *testing.T) {
	c, _, _ := deferredIndexCoordinator(t)
	req := IndexJobRequest{ProjectID: testProject, Sequence: 0, IdempotencyKey: "synthetic-private-index-caller-key", WriteMode: "local"}
	a, err := c.AdmitIndex(context.Background(), req, testActor)
	if err != nil || VerifyIndexJobAdmission(req, a) != nil {
		t.Fatal("protected index admission failed")
	}
	body, _ := json.Marshal(a)
	if strings.Contains(string(body), req.IdempotencyKey) || strings.Contains(string(body), testActor.ID) {
		t.Fatal("protected admission reflected caller or actor")
	}
	for _, field := range []string{"key", "sequence", "mode", "project"} {
		bad := req
		switch field {
		case "key":
			bad.IdempotencyKey = "synthetic-other-caller-key"
		case "sequence":
			bad.Sequence++
		case "mode":
			bad.WriteMode = "live"
		case "project":
			bad.ProjectID = "55555555-5555-4555-8555-555555555555"
		}
		if VerifyIndexJobAdmission(bad, a) == nil {
			t.Fatal("admission accepted changed caller intent")
		}
	}
	bad := a
	bad.RequestDigest = strings.Repeat("a", 64)
	if VerifyIndexJobAdmission(req, bad) == nil {
		t.Fatal("admission accepted unrelated request digest")
	}
}

func TestIndexJobsPagesNeverProvisionAndPinExactLedger(t *testing.T) {
	c, _, dir := deferredIndexCoordinator(t)
	ctx := context.Background()
	query := IndexJobsRequest{ProjectID: testProject, Limit: 1}
	missing, err := c.IndexJobs(ctx, query)
	if err != nil || VerifyIndexJobsPage(query, missing) != nil || missing.State != "missing" {
		t.Fatal("missing index ledger observation failed")
	}
	if _, err := os.Lstat(filepath.Join(dir, indexJobsDirectory)); !os.IsNotExist(err) {
		t.Fatal("index page read created metadata")
	}
	var ids []authority.UUID
	for i := 0; i < 3; i++ {
		_, r := admittedIndexRecord(t, c, 0, fmt.Sprintf("synthetic-page-index-%d", i))
		ids = append(ids, r.Selection.BuildID)
		if _, err := c.CancelIndex(ctx, IndexJobAction{ProjectID: testProject, JobID: r.Selection.BuildID, ExpectedGeneration: 1, WriteMode: "local"}, testActor); err != nil {
			t.Fatal("page fixture cancellation failed")
		}
	}
	first, err := c.IndexJobs(ctx, query)
	if err != nil || first.NextCursor == "" || VerifyIndexJobsPage(query, first) != nil || first.Retained != 3 || len(first.Jobs) != 1 {
		t.Fatal("bounded first index page failed")
	}
	query.Cursor = first.NextCursor
	second, err := c.IndexJobs(ctx, query)
	if err != nil || VerifyIndexJobsPage(query, second) != nil || second.Offset != 1 || second.Jobs[0].JobID <= first.Jobs[0].JobID {
		t.Fatal("exact index continuation failed")
	}
	for _, field := range []string{"generation", "digest", "offset", "project", "last", "empty", "null", "total"} {
		bad := second
		bad.Jobs = append([]IndexJobView{}, second.Jobs...)
		switch field {
		case "generation":
			bad.Generation++
		case "digest":
			bad.SnapshotDigest = strings.Repeat("b", 64)
		case "offset":
			bad.Offset++
		case "project":
			bad.ProjectID = "55555555-5555-4555-8555-555555555555"
		case "last":
			bad.Jobs[0].JobID = first.Jobs[0].JobID
		case "empty":
			bad.Jobs = []IndexJobView{}
		case "null":
			bad.Jobs = nil
		case "total":
			bad.Retained++
		}
		if VerifyIndexJobsPage(query, bad) == nil {
			t.Fatal("protected continuation accepted mixed ledger")
		}
	}
	query.Cursor = second.NextCursor
	last, err := c.IndexJobs(ctx, query)
	if err != nil || VerifyIndexJobsPage(query, last) != nil || last.NextCursor != "" || last.Offset != 2 || last.Jobs[0].JobID <= second.Jobs[0].JobID {
		t.Fatal("last index page lost exact accounting")
	}
	query.Cursor = first.NextCursor
	if _, err := c.RetryIndex(ctx, IndexJobAction{ProjectID: testProject, JobID: ids[0], ExpectedGeneration: 2, WriteMode: "local"}, testActor); err != nil {
		t.Fatal("changed-ledger fixture retry failed")
	}
	if _, err := c.IndexJobs(ctx, query); !authorityConflict(err) {
		t.Fatal("changed ledger accepted prior continuation")
	}
	for _, malformed := range []string{"synthetic-invalid-cursor", base64.RawURLEncoding.EncodeToString([]byte(`{"project_id":null}`))} {
		query.Cursor = malformed
		if _, err := c.IndexJobs(ctx, query); err == nil {
			t.Fatal("malformed continuation reached index page")
		}
	}
}

func TestIndexActiveRetryRefusesWithoutMetadataChange(t *testing.T) {
	c, _, _ := deferredIndexCoordinator(t)
	_, r := admittedIndexRecord(t, c, 0, "synthetic-active-retry")
	q := c.projects[testProject].indexJobs
	before, _ := json.Marshal(q.state)
	if _, err := c.RetryIndex(context.Background(), IndexJobAction{ProjectID: testProject, JobID: r.Selection.BuildID, ExpectedGeneration: 1, WriteMode: "local"}, testActor); !authorityConflict(err) {
		t.Fatal("active retry falsely acknowledged unperformed action")
	}
	after, _ := json.Marshal(q.state)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("rejected active retry changed ledger")
	}
}
