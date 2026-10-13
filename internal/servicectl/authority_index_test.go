package servicectl

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"gitcode-mcp/internal/authority"
	"gitcode-mcp/internal/localauthority"
)

func indexIPCRequests() map[string]any {
	return map[string]any{
		"AuthorityV1.EnqueueIndex": localauthority.IndexJobRequest{ProjectID: authorityExampleProject, Sequence: 7, IdempotencyKey: "synthetic-private-index-key", WriteMode: "local"},
		"AuthorityV1.IndexJob":     localauthority.IndexJobLookup{ProjectID: authorityExampleProject, JobID: recoveryExampleJob},
		"AuthorityV1.IndexJobs":    localauthority.IndexJobsRequest{ProjectID: authorityExampleProject, Limit: 1},
		"AuthorityV1.CancelIndex":  localauthority.IndexJobAction{ProjectID: authorityExampleProject, JobID: recoveryExampleJob, ExpectedGeneration: 1, WriteMode: "local"},
		"AuthorityV1.RetryIndex":   localauthority.IndexJobAction{ProjectID: authorityExampleProject, JobID: recoveryExampleJob, ExpectedGeneration: 2, WriteMode: "local"},
	}
}

func indexExampleView() localauthority.IndexJobView {
	return localauthority.IndexJobView{Schema: 1, ProjectID: authorityExampleProject, JobID: recoveryExampleJob, Type: "content_catalog", State: "queued", Generation: 1, Sequence: 7, CatalogGeneration: 1}
}

func indexExampleAdmission() localauthority.IndexJobAdmission {
	r := indexIPCRequests()["AuthorityV1.EnqueueIndex"].(localauthority.IndexJobRequest)
	body, _ := json.Marshal(r)
	digest := sha256.Sum256(append([]byte("authority.v1/content_catalog/admission\x00"), body...))
	return localauthority.IndexJobAdmission{Schema: 1, ProjectID: r.ProjectID, RequestDigest: hex.EncodeToString(digest[:]), Job: indexExampleView()}
}

func indexExampleResult(method string) any {
	v := indexExampleView()
	switch method {
	case "AuthorityV1.EnqueueIndex":
		return indexExampleAdmission()
	case "AuthorityV1.IndexJobs":
		return localauthority.IndexJobsPage{Schema: 1, ProjectID: v.ProjectID, State: "ready", Generation: 2, SnapshotDigest: strings.Repeat("a", 64), Retained: 1, Jobs: []localauthority.IndexJobView{v}}
	case "AuthorityV1.CancelIndex":
		v.State, v.Generation, v.Code = "cancelled", 2, "cancelled"
	case "AuthorityV1.RetryIndex":
		v.Generation = 3
	}
	return v
}

func TestAuthorityIndexParamsFailBeforeTransportAndRemoteDispatch(t *testing.T) {
	for method, req := range indexIPCRequests() {
		t.Run(method, func(t *testing.T) {
			body, _ := json.Marshal(req)
			if _, err := authorityParams(method, body); err != nil {
				t.Fatal("valid index request refused")
			}
			for _, field := range []string{"path", "repo_id", "actor", "provider", "markdown", "profile", "credential", "destination", "namespace"} {
				var shape map[string]any
				_ = json.Unmarshal(body, &shape)
				shape[field] = "synthetic-private-selector"
				c := RPCClient{Network: "mem", Address: "missing-index-fixture"}
				requireAuthorityCode(t, c.Call(context.Background(), method, shape, nil), authority.InvalidContract)
				if c.nextID.Load() != 0 {
					t.Fatal("invalid index request entered transport")
				}
			}
			duplicate := append([]byte(`{"project_id":"12345678-1234-4234-8234-123456789abc",`), body[1:]...)
			_, err := authorityParams(method, duplicate)
			requireAuthorityCode(t, err, authority.InvalidContract)
			_, err = (RPCServer{}).dispatch(context.Background(), method, body)
			requireAuthorityCode(t, err, authority.NoAuthority)
			c := RPCClient{Network: "tcp", Address: "synthetic-private.invalid:1"}
			requireAuthorityCode(t, c.Call(context.Background(), method, req, nil), authority.PolicyDenied)
			if c.nextID.Load() != 0 {
				t.Fatal("index control negotiated over TCP")
			}
			var shape map[string]any
			_ = json.Unmarshal(body, &shape)
			if _, write := shape["write_mode"]; write {
				for _, mode := range []string{"", "live", "dry-run"} {
					shape["write_mode"] = mode
					bad, _ := json.Marshal(shape)
					_, err := authorityParams(method, bad)
					requireAuthorityCode(t, err, authority.InvalidContract)
				}
			}
		})
	}
}

func TestAuthorityIndexProtectedResultBinding(t *testing.T) {
	for method, req := range indexIPCRequests() {
		body, _ := json.Marshal(indexExampleResult(method))
		if _, err := authorityRuntimeResult(method, req, body); err != nil {
			t.Fatal("valid protected index response refused")
		}
		for _, bad := range [][]byte{[]byte(`{}`), []byte(`null`), []byte(`{"path":"synthetic-private-selector"}`), append(body, body...)} {
			_, err := authorityRuntimeResult(method, req, bad)
			requireAuthorityCode(t, err, authority.InvalidContract)
		}
		var shape map[string]any
		_ = json.Unmarshal(body, &shape)
		shape["project_id"] = "55555555-5555-4555-8555-555555555555"
		bad, _ := json.Marshal(shape)
		_, err := authorityRuntimeResult(method, req, bad)
		requireAuthorityCode(t, err, authority.InvalidContract)
	}
	a := indexExampleAdmission()
	for _, field := range []string{"key", "sequence"} {
		req := indexIPCRequests()["AuthorityV1.EnqueueIndex"].(localauthority.IndexJobRequest)
		if field == "key" {
			req.IdempotencyKey = "synthetic-other-key"
		} else {
			req.Sequence++
		}
		body, _ := json.Marshal(a)
		_, err := authorityRuntimeResult("AuthorityV1.EnqueueIndex", req, body)
		requireAuthorityCode(t, err, authority.InvalidContract)
	}
	for _, method := range []string{"AuthorityV1.IndexJob", "AuthorityV1.CancelIndex", "AuthorityV1.RetryIndex"} {
		v := indexExampleResult(method).(localauthority.IndexJobView)
		v.JobID = "55555555-5555-4555-8555-555555555555"
		body, _ := json.Marshal(v)
		_, err := authorityRuntimeResult(method, indexIPCRequests()[method], body)
		requireAuthorityCode(t, err, authority.InvalidContract)
	}
	for _, method := range []string{"AuthorityV1.CancelIndex", "AuthorityV1.RetryIndex"} {
		v := indexExampleView()
		req := indexIPCRequests()[method].(localauthority.IndexJobAction)
		v.Generation = req.ExpectedGeneration
		body, _ := json.Marshal(v)
		_, err := authorityRuntimeResult(method, req, body)
		requireAuthorityCode(t, err, authority.InvalidContract)
		v.State, v.Attempts, v.Published = "succeeded", 1, true
		v.Generation = 4
		req.ExpectedGeneration = 4
		body, _ = json.Marshal(v)
		if _, err := authorityRuntimeResult(method, req, body); err != nil {
			t.Fatal("already published terminal index action refused")
		}
	}
}
