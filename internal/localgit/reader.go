package localgit

import (
	"bytes"
	"context"
	"sort"
	"strconv"
	"strings"

	"gitcode-mcp/internal/repositorydocs"
)

type Snapshot struct {
	store    *Store
	project  Project
	commit   string
	entries  map[string]repositorydocs.TreeEntry
	entities map[string]string
}

func sortIDs(ids []string, kind Kind) {
	sort.Slice(ids, func(i, j int) bool { a, _ := idNumber(kind, ids[i]); b, _ := idNumber(kind, ids[j]); return a < b })
}

func (s *Snapshot) Project() Project { return s.project }
func (s *Snapshot) Commit() string   { return s.commit }

func (s *Store) Snapshot(ctx context.Context, ref string) (*Snapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	if ref == "" {
		ref = "HEAD"
	}
	if ref != "HEAD" && !oidValid(ref) {
		if !strings.HasPrefix(ref, "refs/heads/") {
			return nil, fail("invalid_argument", "revision")
		}
		if _, err := s.run(ctx, 128, "check-ref-format", ref); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, fail("invalid_argument", "revision")
		}
	}
	out, err := s.run(ctx, 128, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	if err != nil {
		return nil, err
	}
	commit := strings.TrimSpace(string(out))
	if !oidValid(commit) {
		return nil, fail("unavailable", "commit")
	}
	entries, err := s.tree(ctx, commit, "")
	if err != nil {
		return nil, err
	}
	snap := &Snapshot{store: s, commit: commit, entries: entries, entities: map[string]string{}}
	data, _, err := snap.file(ctx, ".harness/project.yaml", s.limits.MetadataBytes)
	if err != nil {
		return nil, err
	}
	if err = decodeYAML(data, &snap.project); err != nil {
		return nil, err
	}
	if err = validateProject(snap.project); err != nil {
		return nil, err
	}
	for path, entry := range entries {
		if r, _, ok := parseEntityPath(path); ok {
			if entry.Mode != "100644" || entry.Type != "blob" {
				return nil, fail("invalid_schema", "file_mode")
			}
			if _, exists := snap.entities[r.ID]; exists {
				return nil, fail("invalid_schema", "duplicate_id")
			}
			snap.entities[r.ID] = path
			n, _ := idNumber(r.Kind, r.ID)
			next := map[Kind]int64{Issue: snap.project.NextIssue, Comment: snap.project.NextComment, Wiki: snap.project.NextDocument}[r.Kind]
			if n >= next {
				return nil, fail("invalid_schema", "counters")
			}
		} else if strings.HasPrefix(path, "issues/") || strings.HasPrefix(path, "wiki/") {
			return nil, fail("invalid_schema", "entity_path")
		}
	}
	return snap, nil
}

func (s *Store) tree(ctx context.Context, commit, path string) (map[string]repositorydocs.TreeEntry, error) {
	args := []string{"ls-tree", "-r", "-l", "-z", "--full-tree", commit}
	if path != "" {
		args = append(args, "--", ":(top,literal)"+path)
	}
	out, err := s.run(ctx, s.limits.TreeBytes, args...)
	if err != nil {
		return nil, err
	}
	entries := map[string]repositorydocs.TreeEntry{}
	if len(out) > 0 && out[len(out)-1] != 0 {
		return nil, fail("invalid_schema", "git_tree")
	}
	for _, record := range bytes.Split(out, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		if len(entries) >= s.limits.Entries {
			return nil, fail("limit_exceeded", "entries")
		}
		header, p, ok := bytes.Cut(record, []byte{'\t'})
		fields := strings.Fields(string(header))
		if !ok || len(p) == 0 || len(fields) != 4 || !oidValid(fields[2]) {
			return nil, fail("invalid_schema", "git_tree")
		}
		size, err := strconv.ParseInt(fields[3], 10, 64)
		if err != nil {
			size = -1
		}
		e := repositorydocs.TreeEntry{Path: string(p), Mode: fields[0], Type: fields[1], OID: fields[2], Size: size}
		entries[e.Path] = e
	}
	return entries, nil
}

func (s *Snapshot) file(ctx context.Context, path string, maxBytes int) ([]byte, repositorydocs.TreeEntry, error) {
	e, ok := s.entries[path]
	if !ok {
		return nil, e, fail("not_found", "file")
	}
	if e.Type != "blob" || e.Mode != "100644" || e.Size < 0 {
		return nil, e, fail("invalid_schema", "file_mode")
	}
	if e.Size > int64(maxBytes) {
		return nil, e, fail("limit_exceeded", "file_bytes")
	}
	data, err := s.store.run(ctx, maxBytes, "cat-file", "blob", e.OID)
	if err != nil {
		return nil, e, err
	}
	if int64(len(data)) != e.Size || repositorydocs.ClassifyDocumentContent(e, data) != repositorydocs.DocumentContentRegular {
		return nil, e, fail("invalid_schema", "file_content")
	}
	return data, e, nil
}

func (s *Snapshot) Get(ctx context.Context, kind Kind, id string) (Entity, error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	if _, err := idNumber(kind, id); err != nil {
		return Entity{}, err
	}
	path, ok := s.entities[id]
	if !ok {
		return Entity{}, fail("not_found", "entity")
	}
	data, entry, err := s.file(ctx, path, s.store.limits.FileBytes)
	if err != nil {
		return Entity{}, err
	}
	e, err := decodeEntity(data, s.store.limits.MetadataBytes)
	if err != nil {
		return Entity{}, err
	}
	want, err := entityPath(e.Metadata.Kind, e.Metadata.ID, e.Metadata.ParentID)
	if err != nil || want != path || e.Metadata.Kind != kind || e.Metadata.ID != id {
		return Entity{}, fail("invalid_schema", "identity")
	}
	if e.Metadata.Kind == Comment {
		if _, ok := s.entities[e.Metadata.ParentID]; !ok {
			return Entity{}, fail("invalid_schema", "parent_id")
		}
	}
	e.Citation = Citation{s.project.ID, kind, id, s.commit, entry.OID, path}
	return e, nil
}

// List is a bounded, stable-ID ordered read, not a persisted index. Tombstones
// remain visible; transports can explicitly filter them without recycling IDs.
func (s *Snapshot) List(ctx context.Context, kind Kind, offset, limit int) ([]Entity, error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	if offset < 0 || offset > s.store.limits.Entries || limit < 1 || limit > 128 {
		return nil, fail("invalid_argument", "pagination")
	}
	if kind != Issue && kind != Comment && kind != Wiki {
		return nil, fail("invalid_argument", "kind")
	}
	ids := make([]string, 0)
	for id, path := range s.entities {
		r, _, _ := parseEntityPath(path)
		if r.Kind == kind {
			ids = append(ids, id)
		}
	}
	sortIDs(ids, kind)
	if offset >= len(ids) {
		return []Entity{}, nil
	}
	end := offset + limit
	if end > len(ids) {
		end = len(ids)
	}
	result := make([]Entity, 0, end-offset)
	size := int64(0)
	for _, id := range ids[offset:end] {
		size += s.entries[s.entities[id]].Size
		if size > int64(s.store.limits.ReadBytes) {
			return nil, fail("limit_exceeded", "read_bytes")
		}
		e, err := s.Get(ctx, kind, id)
		if err != nil {
			return nil, err
		}
		result = append(result, e)
	}
	return result, nil
}

func (s *Snapshot) Receipt(ctx context.Context, keyHash string) (Receipt, Citation, error) {
	if !hashValid(keyHash, 64) {
		return Receipt{}, Citation{}, fail("invalid_argument", "key_hash")
	}
	path := ".harness/receipts/" + keyHash + ".json"
	data, e, err := s.file(ctx, path, s.store.limits.MetadataBytes)
	if err != nil {
		return Receipt{}, Citation{}, err
	}
	r, err := decodeReceipt(data, s.project.ID, keyHash)
	if err != nil {
		return Receipt{}, Citation{}, err
	}
	return r, Citation{ProjectID: s.project.ID, Commit: s.commit, Blob: e.OID, Path: path}, nil
}

// History examines at most the caller's bounded first-parent commit prefix.
// Exhaustion returns unavailable evidence, never a complete/absent result.
func (s *Snapshot) History(ctx context.Context, kind Kind, id string, maxCommits int) ([]Citation, error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	if _, err := s.Get(ctx, kind, id); err != nil {
		return nil, err
	}
	if maxCommits < 1 || maxCommits > s.store.limits.History {
		return nil, fail("invalid_argument", "history_limit")
	}
	out, err := s.store.run(ctx, (maxCommits+1)*65, "rev-list", "--first-parent", "--max-count="+strconv.Itoa(maxCommits+1), s.commit, "--")
	if err != nil {
		return nil, err
	}
	commits := strings.Fields(string(out))
	if len(commits) > maxCommits {
		return nil, fail("history_unavailable", "history_limit")
	}
	path := s.entities[id]
	result := []Citation{}
	var pending *Citation
	for _, commit := range commits {
		if !oidValid(commit) {
			return nil, fail("invalid_schema", "history")
		}
		entries, err := s.store.tree(ctx, commit, path)
		if err != nil {
			return nil, err
		}
		entry, ok := entries[path]
		if pending != nil && (!ok || pending.Blob != entry.OID) {
			result = append(result, *pending)
			pending = nil
		}
		if ok {
			c := Citation{s.project.ID, kind, id, commit, entry.OID, path}
			pending = &c
		}
	}
	if pending != nil {
		result = append(result, *pending)
	}
	return result, nil
}
