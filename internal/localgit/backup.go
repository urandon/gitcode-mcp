package localgit

import (
	"bufio"
	"context"
	"encoding/binary"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const MaxBundleBytes = 64 << 20

// BackupResult is content/path free. The standard Git bundle itself is private.
type BackupResult struct {
	ProjectID string `json:"project_id"`
	Commit    string `json:"commit"`
	Commits   int    `json:"commits"`
	Status    string `json:"status"`
}

// ExportBundle pins the configured branch under the existing writer lane. No
// working tree, cache, remote transport, extra ref or backup protocol is used.
func (s *Store) ExportBundle(ctx context.Context, ref, projectID, path string) (result BackupResult, err error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	if !filepath.IsAbs(path) {
		return result, fail("invalid_argument", "bundle")
	}
	unlock, err := s.writerLock(ctx)
	if err != nil {
		return result, err
	}
	defer unlock()
	if err = s.ValidateLocalBranch(ctx, ref); err != nil {
		return result, err
	}
	snap, err := s.Snapshot(ctx, ref)
	if err != nil {
		return result, err
	}
	result, err = s.validateBackup(ctx, snap.commit, projectID)
	if err != nil {
		return result, err
	}
	// Bounded binary output remains request-local memory and is never a DTO/log.
	data, err := s.run(ctx, MaxBundleBytes, "bundle", "create", "-", ref, "--")
	if err != nil {
		return result, err
	}
	current, err := s.Snapshot(ctx, ref)
	if err != nil {
		return result, err
	}
	if current.commit != snap.commit {
		return result, fail("write_conflict", "head")
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		if os.IsExist(err) {
			return result, fail("write_conflict", "bundle_exists")
		}
		return result, fail("unavailable", "bundle")
	}
	defer func() {
		_ = f.Close()
		if err != nil {
			_ = os.Remove(path)
		}
	}()
	if _, e := f.Write(data); e != nil {
		return result, fail("unavailable", "bundle")
	}
	if f.Sync() != nil || f.Close() != nil {
		return result, fail("unavailable", "bundle")
	}
	if err = s.validateBundleHead(ctx, path, ref, snap.commit); err != nil {
		return result, err
	}
	result.Status = "exported"
	return result, nil
}

func (s *Store) validateBundleHead(ctx context.Context, path, ref, head string) error {
	out, err := s.run(ctx, 1024, "bundle", "list-heads", path)
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 1 || lines[0] != head+" "+ref {
		return fail("invalid_schema", "bundle_head")
	}
	return nil
}

// RestoreBundle only creates a new store. A verified store survives a later
// registration failure; invalid unpublished imports are removed by this owner.
func RestoreBundle(ctx context.Context, path, bundle, projectID, ref, head string) (_ *Store, result BackupResult, err error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	if !filepath.IsAbs(path) || !filepath.IsAbs(bundle) || !projectIDValid(projectID) || !oidValid(head) || !strings.HasPrefix(ref, "refs/heads/") {
		return nil, result, fail("invalid_argument", "restore")
	}
	if ctx.Err() != nil {
		return nil, result, ctx.Err()
	}
	// Copy a bounded regular file to a private snapshot before Git interprets it;
	// URL-like paths, FIFOs, symlinks and changing input cannot become a transport.
	info, err := os.Lstat(bundle)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 {
		return nil, result, fail("invalid_argument", "bundle")
	}
	if info.Size() > MaxBundleBytes {
		return nil, result, fail("limit_exceeded", "bundle_bytes")
	}
	input, err := os.Open(bundle)
	if err != nil {
		return nil, result, fail("unavailable", "bundle")
	}
	defer input.Close()
	opened, err := input.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, result, fail("unavailable", "bundle")
	}
	git, err := exec.LookPath("git")
	if err != nil {
		return nil, result, fail("unavailable", "git")
	}
	if os.MkdirAll(filepath.Dir(path), 0700) != nil {
		return nil, result, fail("unavailable", "store")
	}
	if os.Mkdir(path, 0700) != nil {
		return nil, result, fail("write_conflict", "store_exists")
	}
	verified := false
	defer func() {
		if err != nil && !verified {
			_ = os.RemoveAll(path)
		}
	}()
	limits, _ := normalizeLimits(Limits{})
	s := &Store{dir: path, git: git, limits: limits}
	if os.Mkdir(filepath.Join(path, "objects"), 0700) != nil {
		return nil, result, fail("unavailable", "store")
	}
	objectFormat := "sha1"
	if len(head) == 64 {
		objectFormat = "sha256"
	}
	if _, err = s.run(ctx, 1024, "init", "--bare", "--object-format="+objectFormat, "--template=", path); err != nil {
		return nil, result, err
	}
	if err = s.ValidateLocalBranch(ctx, ref); err != nil {
		return nil, result, err
	}
	private, err := os.CreateTemp(path, "restore-*.bundle")
	if err != nil {
		return nil, result, fail("unavailable", "bundle")
	}
	defer func() { _ = private.Close(); _ = os.Remove(private.Name()) }()
	n, e := io.Copy(private, io.LimitReader(input, MaxBundleBytes+1))
	if ctx.Err() != nil {
		return nil, result, ctx.Err()
	}
	if n > MaxBundleBytes {
		return nil, result, fail("limit_exceeded", "bundle_bytes")
	}
	if e != nil || n < 1 {
		return nil, result, fail("unavailable", "bundle")
	}
	if private.Sync() != nil || private.Close() != nil {
		return nil, result, fail("unavailable", "bundle")
	}
	if err = validateBundleEnvelope(private.Name()); err != nil {
		return nil, result, err
	}
	if err = s.validateBundleHead(ctx, private.Name(), ref, head); err != nil {
		return nil, result, err
	}
	// Empty target means incremental prerequisites cannot be satisfied. unbundle
	// imports objects only; no configured ref is published until validation passes.
	if _, err = s.run(ctx, 4096, "bundle", "verify", private.Name()); err != nil {
		return nil, result, err
	}
	if _, err = s.run(ctx, 1024, "-c", "core.bigFileThreshold=1048576", "-c", "core.deltaBaseCacheLimit=8m", "bundle", "unbundle", private.Name()); err != nil {
		return nil, result, err
	}
	if _, err = s.run(ctx, 4096, "fsck", "--strict", "--no-reflogs", "--no-dangling", head); err != nil {
		return nil, result, err
	}
	result, err = s.validateBackup(ctx, head, projectID)
	if err != nil {
		return nil, result, err
	}
	if _, err = s.run(ctx, 128, "symbolic-ref", "HEAD", ref); err != nil {
		return nil, result, err
	}
	verified = true
	if _, err = s.run(ctx, 128, "update-ref", "--no-deref", ref, head, strings.Repeat("0", len(head))); err != nil {
		return nil, result, fail("publication_ambiguous", "restore_ref")
	}
	result.Status = "restored"
	return s, result, nil
}

func projectIDValid(id string) bool { return projectID.MatchString(id) }

// Inspect only the documented ordinary Git envelope/pack header, not entity
// bytes or a new bundle protocol. Bound advertised pack fanout before index-pack.
func validateBundleEnvelope(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fail("unavailable", "bundle")
	}
	defer f.Close()
	r := bufio.NewReader(io.LimitReader(f, MaxMetadataBytes))
	line, err := r.ReadString('\n')
	if err != nil || line != "# v2 git bundle\n" && line != "# v3 git bundle\n" {
		return fail("invalid_schema", "bundle_header")
	}
	for {
		line, err = r.ReadString('\n')
		if err != nil {
			return fail("invalid_schema", "bundle_header")
		}
		if line == "\n" {
			break
		}
		if strings.HasPrefix(line, "-") || strings.HasPrefix(line, "@filter=") {
			return fail("invalid_schema", "bundle_prerequisite")
		}
	}
	var pack [12]byte
	if _, err = io.ReadFull(r, pack[:]); err != nil || string(pack[:4]) != "PACK" {
		return fail("invalid_schema", "bundle_pack")
	}
	version, count := binary.BigEndian.Uint32(pack[4:8]), binary.BigEndian.Uint32(pack[8:12])
	if version != 2 && version != 3 || count == 0 || count > 4*MaxEntries+MaxHistory {
		return fail("limit_exceeded", "bundle_objects")
	}
	return nil
}

// validateBackup proves the complete linear prefix fits the existing ceilings.
// It validates each distinct blob once, and keeps only bounded metadata/OIDs.
func (s *Store) validateBackup(ctx context.Context, head, id string) (BackupResult, error) {
	result := BackupResult{ProjectID: id, Commit: head}
	out, err := s.run(ctx, (s.limits.History+1)*140, "rev-list", "--parents", "--max-count="+strconv.Itoa(s.limits.History+1), head, "--")
	if err != nil {
		return result, err
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) > s.limits.History {
		return result, fail("history_unavailable", "backup_history")
	}
	for i, line := range lines {
		p := strings.Fields(line)
		if len(p) < 1 || len(p) > 2 || !oidValid(p[0]) || i == 0 && p[0] != head {
			return result, fail("invalid_schema", "backup_history")
		}
		if i == len(lines)-1 {
			if len(p) != 1 {
				return result, fail("history_unavailable", "backup_history")
			}
		} else if len(p) != 2 || strings.Fields(lines[i+1])[0] != p[1] {
			return result, fail("invalid_schema", "backup_history")
		}
	}
	validated := map[string]bool{}
	checkedEntities := map[string]bool{}
	receipts := map[string]string{}
	entities := map[string]string{}
	var previous Project
	bytesRead := int64(0)
	for i := len(lines) - 1; i >= 0; i-- {
		commit := strings.Fields(lines[i])[0]
		snap, err := s.Snapshot(ctx, commit)
		if err != nil {
			return result, err
		}
		if snap.project.ID != id {
			return result, fail("invalid_schema", "project_identity")
		}
		if previous.ID != "" && (snap.project.NextIssue < previous.NextIssue || snap.project.NextComment < previous.NextComment || snap.project.NextDocument < previous.NextDocument) {
			return result, fail("invalid_schema", "counter_history")
		}
		for key, oid := range receipts {
			if snap.entries[key].OID != oid {
				return result, fail("invalid_schema", "receipt_history")
			}
		}
		for key, path := range entities {
			if snap.entities[key] != path {
				return result, fail("invalid_schema", "entity_history")
			}
		}
		for path, entry := range snap.entries {
			_, _, isEntity := parseEntityPath(path)
			isReceipt := strings.HasPrefix(path, ".harness/receipts/") && strings.HasSuffix(path, ".json")
			if path != ".harness/project.yaml" && !isEntity && !isReceipt {
				return result, fail("invalid_schema", "backup_path")
			}
			if entry.Mode != "100644" || entry.Type != "blob" || entry.Size < 0 {
				return result, fail("invalid_schema", "file_mode")
			}
			max := s.limits.MetadataBytes
			if isEntity {
				max = s.limits.FileBytes
			}
			if entry.Size > int64(max) {
				return result, fail("limit_exceeded", "file_bytes")
			}
			if !validated[entry.OID] {
				bytesRead += entry.Size
				if bytesRead > int64(s.limits.ReadBytes) || len(validated) >= s.limits.Entries {
					return result, fail("history_unavailable", "backup_budget")
				}
				validated[entry.OID] = true
			}
			if isEntity && !checkedEntities[path+entry.OID] {
				if len(checkedEntities) >= s.limits.Entries {
					return result, fail("history_unavailable", "backup_budget")
				}
				r, _, _ := parseEntityPath(path)
				if _, err = snap.Get(ctx, r.Kind, r.ID); err != nil {
					return result, err
				}
				checkedEntities[path+entry.OID] = true
			}
			if isReceipt {
				key := strings.TrimSuffix(strings.TrimPrefix(path, ".harness/receipts/"), ".json")
				r, _, err := snap.Receipt(ctx, key)
				if err != nil {
					return result, err
				}
				if _, known := receipts[path]; !known {
					if _, err = snap.resultAt(ctx, commit, r); err != nil {
						return result, err
					}
					receipts[path] = entry.OID
				}
			}
		}
		entities = snap.entities
		previous = snap.project
	}
	result.Commits = len(lines)
	return result, nil
}
