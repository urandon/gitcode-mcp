package servicectl

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const syncCollectionRetrySchema = "gitcode-mcp.sync-collection-retries.v1"

type syncCollectionRetryCheckpointError struct{}

func (syncCollectionRetryCheckpointError) Error() string {
	return "service: collection retry checkpoint could not be persisted"
}

func (syncCollectionRetryCheckpointError) DiagnosticCode() string {
	return "retry_checkpoint_persist_failed"
}

type syncCollectionRetryRequest struct {
	RepoID          string         `json:"repo_id"`
	ProviderMode    string         `json:"provider_mode,omitempty"`
	CachePath       string         `json:"cache_path"`
	Issues          bool           `json:"issues,omitempty"`
	Wiki            bool           `json:"wiki,omitempty"`
	Pulls           bool           `json:"pulls,omitempty"`
	Comments        bool           `json:"comments,omitempty"`
	IssueComments   bool           `json:"issue_comments,omitempty"`
	PRComments      bool           `json:"pr_comments,omitempty"`
	IdempotencyKey  string         `json:"idempotency_key,omitempty"`
	MaxPages        int            `json:"max_pages,omitempty"`
	MaxRecords      int            `json:"max_records,omitempty"`
	PerPage         int            `json:"per_page,omitempty"`
	Page            int            `json:"page,omitempty"`
	CacheUUID       string         `json:"cache_uuid"`
	RegistrationID  string         `json:"registration_id"`
	Lane            string         `json:"lane,omitempty"`
	CollectionPages map[string]int `json:"collection_pages,omitempty"`
}

type syncCollectionRetryCheckpoint struct {
	JobID      string                     `json:"job_id"`
	Collection string                     `json:"collection"`
	RemoteType string                     `json:"remote_type"`
	Attempt    int                        `json:"attempt"`
	RetryAt    time.Time                  `json:"retry_at"`
	Request    syncCollectionRetryRequest `json:"request"`
}

type syncCollectionRetryFile struct {
	Schema      string                          `json:"schema"`
	Checkpoints []syncCollectionRetryCheckpoint `json:"checkpoints"`
}

type syncCollectionRetryJournal struct {
	path      string
	writeFile func(string, []byte, os.FileMode) error
}

func newSyncCollectionRetryJournal(runtimeDir string) *syncCollectionRetryJournal {
	return &syncCollectionRetryJournal{
		path: filepath.Join(runtimeDir, "sync-collection-retries.json"), writeFile: durableAtomicWriteFile,
	}
}

func (j *syncCollectionRetryJournal) List() ([]syncCollectionRetryCheckpoint, error) {
	file, err := j.load()
	if err != nil {
		return nil, err
	}
	return append([]syncCollectionRetryCheckpoint(nil), file.Checkpoints...), nil
}

func (j *syncCollectionRetryJournal) Upsert(checkpoint syncCollectionRetryCheckpoint) error {
	file, err := j.load()
	if err != nil {
		return err
	}
	replaced := false
	for i := range file.Checkpoints {
		if file.Checkpoints[i].JobID == checkpoint.JobID && file.Checkpoints[i].Collection == checkpoint.Collection {
			file.Checkpoints[i] = checkpoint
			replaced = true
			break
		}
	}
	if !replaced {
		file.Checkpoints = append(file.Checkpoints, checkpoint)
	}
	return j.save(file)
}

func (j *syncCollectionRetryJournal) Remove(jobID, collection string) error {
	file, err := j.load()
	if err != nil {
		return err
	}
	kept := file.Checkpoints[:0]
	for _, checkpoint := range file.Checkpoints {
		if checkpoint.JobID == jobID && (collection == "" || checkpoint.Collection == collection) {
			continue
		}
		kept = append(kept, checkpoint)
	}
	if len(kept) == len(file.Checkpoints) {
		return nil
	}
	file.Checkpoints = kept
	return j.save(file)
}

func (j *syncCollectionRetryJournal) load() (syncCollectionRetryFile, error) {
	file := syncCollectionRetryFile{Schema: syncCollectionRetrySchema}
	data, err := os.ReadFile(j.path)
	if errors.Is(err, os.ErrNotExist) {
		return file, nil
	}
	if err != nil {
		return file, syncCollectionRetryCheckpointError{}
	}
	if err := json.Unmarshal(data, &file); err != nil || file.Schema != syncCollectionRetrySchema {
		return syncCollectionRetryFile{}, syncCollectionRetryCheckpointError{}
	}
	return file, nil
}

func (j *syncCollectionRetryJournal) save(file syncCollectionRetryFile) error {
	file.Schema = syncCollectionRetrySchema
	sort.Slice(file.Checkpoints, func(i, k int) bool {
		left, right := file.Checkpoints[i], file.Checkpoints[k]
		if left.JobID == right.JobID {
			return left.Collection < right.Collection
		}
		return left.JobID < right.JobID
	})
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return syncCollectionRetryCheckpointError{}
	}
	if err := j.writeFile(j.path, append(data, '\n'), 0o600); err != nil {
		return syncCollectionRetryCheckpointError{}
	}
	return nil
}

func syncRetryRequestFromStart(req StartSyncJobRequest) syncCollectionRetryRequest {
	return syncCollectionRetryRequest{
		RepoID: req.RepoID, ProviderMode: req.ProviderMode, CachePath: req.CachePath,
		Issues: req.Issues, Wiki: req.Wiki, Pulls: req.Pulls, Comments: req.Comments,
		IssueComments: req.IssueComments, PRComments: req.PRComments, IdempotencyKey: req.IdempotencyKey,
		MaxPages: req.MaxPages, MaxRecords: req.MaxRecords, PerPage: req.PerPage, Page: req.Page,
		CacheUUID: req.CacheUUID, RegistrationID: req.RegistrationID, Lane: req.Lane,
		CollectionPages: appendSyncCollectionPages(req.collectionPages),
	}
}

func (req syncCollectionRetryRequest) startRequest() StartSyncJobRequest {
	return StartSyncJobRequest{
		RepoID: req.RepoID, ProviderMode: req.ProviderMode, CachePath: req.CachePath,
		Issues: req.Issues, Wiki: req.Wiki, Pulls: req.Pulls, Comments: req.Comments,
		IssueComments: req.IssueComments, PRComments: req.PRComments, IdempotencyKey: req.IdempotencyKey,
		MaxPages: req.MaxPages, MaxRecords: req.MaxRecords, PerPage: req.PerPage, Page: req.Page,
		CacheUUID: req.CacheUUID, RegistrationID: req.RegistrationID, Lane: req.Lane,
		collectionPages: appendSyncCollectionPages(req.CollectionPages),
	}
}

func syncRetryRuntimeDir(manager Manager, snapshotPath string) string {
	runtimeDir := strings.TrimSpace(manager.RuntimeDir)
	if runtimeDir == "" && strings.TrimSpace(snapshotPath) != "" {
		runtimeDir = filepath.Dir(snapshotPath)
	}
	return runtimeDir
}
