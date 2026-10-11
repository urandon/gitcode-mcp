package servicectl

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestRepositoryDocsCallbacksRejectMismatchedAuthority(t *testing.T) {
	for _, owner := range []string{"finish", "failure"} {
		for _, mismatch := range []string{"registration_generation", "registration_id", "cache_uuid", "repo_id", "physical_cache", "source_generation", "source_path", "source_profile", "selected_source", "state_source", "other_source_target", "missing_source", "missing_registration"} {
			t.Run(owner+"/"+mismatch, func(t *testing.T) {
				m, _, repo, reg, id := inspectionBoundaryFixture(t)
				targetID := id
				if mismatch == "other_source_target" {
					otherRepo := createRepositoryDocsRegistrationRepo(t, repo+"-other", "fixture-other-source")
					other, ok, err := m.RegisterRepositoryDocsSource(context.Background(), m.entries[reg].CacheUUID, m.entries[reg].RepoID, otherRepo, "")
					if err != nil || !ok || other.RepositoryDocs == nil {
						t.Fatal("second source fixture unavailable")
					}
					targetID = other.RepositoryDocs.SourceRegistrationID
				}
				snapshot := cloneMaintenanceEntryPrivate(m.entries[reg])
				state := m.sources[reg][id].State
				snapshot.RepositoryDocs = &state
				snapshot.repositorySourceID = id
				snapshot.repositoryPath = m.sources[reg][id].RepositoryPath
				snapshot.repositoryProfile = m.sources[reg][id].Profile
				snapshot.RepositoryDocs.State = "ready"
				snapshot.RepositoryDocs.RevisionSetID = "fixture-stale-set"
				switch mismatch {
				case "registration_generation":
					snapshot.Generation--
				case "registration_id":
					snapshot.RegistrationID = "fixture-other-registration"
				case "cache_uuid":
					snapshot.CacheUUID = "fixture-other-cache"
				case "repo_id":
					snapshot.RepoID = "fixture/other-repo"
				case "physical_cache":
					snapshot.PathFingerprint = "fixture-other-physical-cache"
				case "source_generation":
					snapshot.RepositoryDocs.SourceRegistrationGeneration--
				case "source_path":
					snapshot.repositoryPath += "-obsolete"
				case "source_profile":
					snapshot.repositoryProfile = "fixture-other-profile"
				case "selected_source":
					snapshot.repositorySourceID = "fixture-other-source"
				case "state_source":
					snapshot.RepositoryDocs.SourceRegistrationID = "fixture-other-source"
				case "missing_source":
					delete(m.sources[reg], id)
				case "missing_registration":
					delete(m.entries, reg)
				}
				before := cloneMaintenanceEntryPrivate(m.entries[reg])
				beforeSources := cloneRepositoryDocsSources(m.sources[reg])
				beforeList, err := m.List(context.Background())
				if err != nil {
					t.Fatal("public source fixture unavailable")
				}
				writes := 0
				m.writeFile = func(string, []byte, os.FileMode) error {
					writes++
					return errors.New("fixture unexpected authority write")
				}
				m.jobs.SetObservation(maintenanceEventCollector(t))
				ctx, flush := beginMaintenanceObservation(context.Background(), m.jobs)
				defer flush()
				if owner == "finish" {
					// A different failure target is not part of finish's contract;
					// model the corresponding mismatched selected-source callback.
					if mismatch == "other_source_target" {
						snapshot.repositorySourceID = targetID
					}
					m.finishReconcileEntry(ctx, reg, snapshot, 10, 9, "ready", "fixture-namespace", nil, Job{}, Job{}, Job{}, time.Now().UTC())
				} else {
					m.updateRepositoryDocsFailure(ctx, reg, targetID, snapshot, "git_store_unavailable", "fixture failure")
				}
				afterList, err := m.List(context.Background())
				if err != nil || writes != 0 || !reflect.DeepEqual(beforeList, afterList) || !reflect.DeepEqual(before, cloneMaintenanceEntryPrivate(m.entries[reg])) || !reflect.DeepEqual(beforeSources, m.sources[reg]) {
					t.Fatal("mismatched callback changed retained source or scheduling authority")
				}
				buffer := ctx.Value(maintenanceObservationKey{}).(*maintenanceObservationBuffer)
				if buffer.count != 0 || buffer.lost != 0 {
					t.Fatal("unchanged rejected callback manufactured observation evidence or loss")
				}
			})
		}
	}
}

func TestRepositoryDocsCompletionQualifiesActiveJobIntent(t *testing.T) {
	for _, mismatch := range []string{"current", "type", "status", "cache", "repository", "registration", "source", "generation"} {
		t.Run(mismatch, func(t *testing.T) {
			m, _, _, reg, id := inspectionBoundaryFixture(t)
			snapshot := cloneMaintenanceEntryPrivate(m.entries[reg])
			before := m.sources[reg][id].State
			job := Job{ID: "fixture-active-job", Type: RepositoryDocsIndexJobType, Status: JobStatusQueued,
				CacheUUID: snapshot.CacheUUID, RepoID: snapshot.RepoID, RegistrationID: reg,
				SourceRegistrationID: id, SourceRegistrationGeneration: before.SourceRegistrationGeneration}
			switch mismatch {
			case "type":
				job.Type = RAGIndexJobType
			case "status":
				job.Status = JobStatusSuperseded
			case "cache":
				job.CacheUUID = "fixture-other-cache"
			case "repository":
				job.RepoID = "fixture/other-repo"
			case "registration":
				job.RegistrationID = "fixture-other-registration"
			case "source":
				job.SourceRegistrationID = "fixture-other-source"
			case "generation":
				job.SourceRegistrationGeneration--
			}
			// A registered but not-yet-indexed source normally makes the parent
			// registration indexing even without an active job. Compare that
			// existing derivation, not a fabricated ready parent status.
			baseline := m.finishReconcileEntry(context.Background(), reg, snapshot, 0, 0, "", "", nil, Job{}, Job{}, Job{}, time.Now().UTC())
			got := m.finishReconcileEntry(context.Background(), reg, snapshot, 0, 0, "", "", nil, Job{}, Job{}, job, time.Now().UTC())
			if mismatch == "current" {
				if m.sources[reg][id].State.State != "indexing" || got.State != "indexing" || len(got.ActiveJobs) != 1 || got.ActiveJobs[0] != job.ID {
					t.Fatal("current active source job did not apply")
				}
			} else if m.sources[reg][id].State != before || len(got.ActiveJobs) != 0 || got.State != baseline.State {
				t.Fatal("unrelated or terminal job copy acquired current source authority")
			}
		})
	}
}
