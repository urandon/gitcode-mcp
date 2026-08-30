package repositorydocs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var ErrVectorCheckpointNotFound = errors.New("repository docs: vector checkpoint not found")

// VectorCheckpoint is a durable, vector-only handoff between an embedding
// provider response and the primary cache writer. It deliberately contains no
// repository document bytes or local filesystem paths.
type VectorCheckpoint struct {
	SchemaVersion string    `json:"schema_version"`
	RepoID        string    `json:"repo_id"`
	NamespaceID   string    `json:"namespace_id"`
	ChunkID       string    `json:"chunk_id"`
	Vector        []byte    `json:"vector"`
	Dimensions    int       `json:"dimensions"`
	DType         string    `json:"dtype"`
	CreatedAt     time.Time `json:"created_at"`
}

type VectorCheckpointStore interface {
	Load(context.Context, string, string, string) (VectorCheckpoint, error)
	Save(context.Context, VectorCheckpoint) error
	Delete(context.Context, string, string, string) error
}

type VectorCheckpointPersistenceError struct {
	operation string
	cause     error
}

func (e VectorCheckpointPersistenceError) Error() string {
	return "repository docs: vector checkpoint " + e.operation + " failed"
}

func (e VectorCheckpointPersistenceError) DiagnosticCode() string {
	return "repository_docs_vector_checkpoint_failed"
}

type FileVectorCheckpointStore struct {
	root string
}

type VectorCheckpointRetentionPolicy struct {
	MaxAge   time.Duration
	MaxBytes int64
	// ActiveIdentities, when non-nil, is the complete set of checkpoint
	// identities still reachable from durable admissions. Everything else is
	// an orphan and can be removed immediately.
	ActiveIdentities map[string]struct{}
}

type VectorCheckpointPruneResult struct {
	FilesDeleted  int
	BytesDeleted  int64
	BytesRetained int64
}

func NewFileVectorCheckpointStore(root string) (*FileVectorCheckpointStore, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, VectorCheckpointPersistenceError{operation: "configure", cause: errors.New("empty checkpoint root")}
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, VectorCheckpointPersistenceError{operation: "configure", cause: err}
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return nil, VectorCheckpointPersistenceError{operation: "configure", cause: err}
	}
	return &FileVectorCheckpointStore{root: root}, nil
}

func (s *FileVectorCheckpointStore) Load(ctx context.Context, repoID, namespaceID, chunkID string) (VectorCheckpoint, error) {
	if err := ctx.Err(); err != nil {
		return VectorCheckpoint{}, err
	}
	path, err := s.checkpointPath(repoID, namespaceID, chunkID)
	if err != nil {
		return VectorCheckpoint{}, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return VectorCheckpoint{}, ErrVectorCheckpointNotFound
	}
	if err != nil {
		return VectorCheckpoint{}, VectorCheckpointPersistenceError{operation: "read", cause: err}
	}
	var checkpoint VectorCheckpoint
	if err := json.Unmarshal(data, &checkpoint); err != nil {
		return VectorCheckpoint{}, s.removeCorrupt(path, err)
	}
	if checkpoint.SchemaVersion != "1" || checkpoint.RepoID != repoID || checkpoint.NamespaceID != namespaceID || checkpoint.ChunkID != chunkID || len(checkpoint.Vector) == 0 || checkpoint.Dimensions <= 0 || checkpoint.DType != "float32" || len(checkpoint.Vector) != checkpoint.Dimensions*4 {
		return VectorCheckpoint{}, s.removeCorrupt(path, errors.New("checkpoint identity or vector contract is inconsistent"))
	}
	return checkpoint, nil
}

func (s *FileVectorCheckpointStore) removeCorrupt(path string, cause error) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return VectorCheckpointPersistenceError{operation: "remove-corrupt", cause: errors.Join(cause, err)}
	}
	if err := syncDirectory(s.root); err != nil {
		return VectorCheckpointPersistenceError{operation: "remove-corrupt", cause: errors.Join(cause, err)}
	}
	// Corrupt derived state is recoverable: make the exact provider request
	// eligible again instead of poisoning every retry for this identity.
	return ErrVectorCheckpointNotFound
}

func (s *FileVectorCheckpointStore) Save(ctx context.Context, checkpoint VectorCheckpoint) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if checkpoint.RepoID == "" || checkpoint.NamespaceID == "" || checkpoint.ChunkID == "" || len(checkpoint.Vector) == 0 || checkpoint.Dimensions <= 0 || checkpoint.DType == "" {
		return VectorCheckpointPersistenceError{operation: "validate", cause: errors.New("checkpoint identity is incomplete")}
	}
	checkpoint.SchemaVersion = "1"
	if checkpoint.CreatedAt.IsZero() {
		checkpoint.CreatedAt = time.Now().UTC()
	}
	data, err := json.Marshal(checkpoint)
	if err != nil {
		return VectorCheckpointPersistenceError{operation: "encode", cause: err}
	}
	path, err := s.checkpointPath(checkpoint.RepoID, checkpoint.NamespaceID, checkpoint.ChunkID)
	if err != nil {
		return err
	}
	if err := durableAtomicVectorCheckpoint(path, append(data, '\n')); err != nil {
		return VectorCheckpointPersistenceError{operation: "write", cause: err}
	}
	return nil
}

func (s *FileVectorCheckpointStore) Delete(ctx context.Context, repoID, namespaceID, chunkID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := s.checkpointPath(repoID, namespaceID, chunkID)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return VectorCheckpointPersistenceError{operation: "delete", cause: err}
	}
	if err := syncDirectory(s.root); err != nil {
		return VectorCheckpointPersistenceError{operation: "delete", cause: err}
	}
	return nil
}

func (s *FileVectorCheckpointStore) checkpointPath(repoID, namespaceID, chunkID string) (string, error) {
	if s == nil || strings.TrimSpace(s.root) == "" || strings.TrimSpace(repoID) == "" || strings.TrimSpace(namespaceID) == "" || strings.TrimSpace(chunkID) == "" {
		return "", VectorCheckpointPersistenceError{operation: "identify", cause: errors.New("checkpoint identity is incomplete")}
	}
	return filepath.Join(s.root, VectorCheckpointIdentity(repoID, namespaceID, chunkID)+".json"), nil
}

func VectorCheckpointIdentity(repoID, namespaceID, chunkID string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{repoID, namespaceID, chunkID}, "\x00")))
	return hex.EncodeToString(sum[:])
}

func (s *FileVectorCheckpointStore) Prune(ctx context.Context, policy VectorCheckpointRetentionPolicy) (VectorCheckpointPruneResult, error) {
	if err := ctx.Err(); err != nil {
		return VectorCheckpointPruneResult{}, err
	}
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return VectorCheckpointPruneResult{}, VectorCheckpointPersistenceError{operation: "prune-list", cause: err}
	}
	type candidate struct {
		path string
		name string
		size int64
		when time.Time
	}
	now := time.Now().UTC()
	var retained []candidate
	result := VectorCheckpointPruneResult{}
	remove := func(item candidate) error {
		if err := os.Remove(item.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		result.FilesDeleted++
		result.BytesDeleted += item.size
		return nil
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil || !info.Mode().IsRegular() {
			continue
		}
		item := candidate{path: filepath.Join(s.root, entry.Name()), name: strings.TrimSuffix(entry.Name(), ".json"), size: info.Size(), when: info.ModTime()}
		_, active := policy.ActiveIdentities[item.name]
		orphan := policy.ActiveIdentities != nil && !active
		expired := policy.MaxAge > 0 && now.Sub(item.when) > policy.MaxAge
		if orphan || expired {
			if err := remove(item); err != nil {
				return result, VectorCheckpointPersistenceError{operation: "prune-delete", cause: err}
			}
			continue
		}
		retained = append(retained, item)
		result.BytesRetained += item.size
	}
	if policy.MaxBytes > 0 && result.BytesRetained > policy.MaxBytes {
		sort.Slice(retained, func(i, j int) bool {
			if retained[i].when.Equal(retained[j].when) {
				return retained[i].name < retained[j].name
			}
			return retained[i].when.Before(retained[j].when)
		})
		for _, item := range retained {
			if result.BytesRetained <= policy.MaxBytes {
				break
			}
			if err := remove(item); err != nil {
				return result, VectorCheckpointPersistenceError{operation: "prune-delete", cause: err}
			}
			result.BytesRetained -= item.size
		}
	}
	if result.FilesDeleted > 0 {
		if err := syncDirectory(s.root); err != nil {
			return result, VectorCheckpointPersistenceError{operation: "prune-sync", cause: err}
		}
	}
	return result, nil
}

func durableAtomicVectorCheckpoint(path string, data []byte) (err error) {
	dir := filepath.Dir(path)
	file, err := os.CreateTemp(dir, ".vector-checkpoint-*")
	if err != nil {
		return err
	}
	tmp := file.Name()
	defer func() { _ = os.Remove(tmp) }()
	if err = file.Chmod(0o600); err == nil {
		_, err = file.Write(data)
	}
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(tmp, path); err != nil {
		return err
	}
	if err = syncDirectory(dir); err != nil {
		return err
	}
	return nil
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("sync checkpoint directory: %w", err)
	}
	return nil
}
