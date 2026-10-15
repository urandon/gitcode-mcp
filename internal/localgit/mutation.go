package localgit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"gitcode-mcp/internal/repositorydocs"
	"gopkg.in/yaml.v3"
)

// Mutation replaces an entity's editable fields, never its identity/parent.
// Guards are concurrency preconditions, not part of the logical intent digest.
type Mutation struct {
	Operation        string      `json:"operation"`
	ID               string      `json:"id,omitempty"`
	ParentID         string      `json:"parent_id,omitempty"`
	Title            string      `json:"title,omitempty"`
	Body             string      `json:"body,omitempty"`
	State            string      `json:"state,omitempty"`
	Labels           []string    `json:"labels,omitempty"`
	Milestone        string      `json:"milestone,omitempty"`
	Links            []Reference `json:"links,omitempty"`
	ExpectedHead     string      `json:"expected_head,omitempty"`
	ExpectedRevision string      `json:"expected_revision,omitempty"`
	IdempotencyKey   string      `json:"idempotency_key"`
	WriteMode        string      `json:"write_mode"`
}

type MutationResult struct {
	Entity   Entity `json:"entity"`
	Replayed bool   `json:"replayed"`
}
type mutationHooks struct{ beforePublish, afterPublish func() error }

func operationKind(op string) Kind {
	return map[string]Kind{"create_issue": Issue, "update_issue": Issue, "delete_issue": Issue, "add_comment": Comment, "update_comment": Comment, "delete_comment": Comment, "create_wiki": Wiki, "update_wiki": Wiki, "delete_wiki": Wiki}[op]
}
func creating(op string) bool   { return strings.HasPrefix(op, "create_") || op == "add_comment" }
func deleting(op string) bool   { return strings.HasPrefix(op, "delete_") }
func digest(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }

func (s *Store) Mutate(ctx context.Context, ref string, req Mutation) (MutationResult, error) {
	return s.MutateProject(ctx, ref, "", req)
}

// MutateProject binds a service registration inside the same writer lane and
// pinned snapshot as receipt lookup/publication, not in a racy preflight read.
func (s *Store) MutateProject(ctx context.Context, ref, projectID string, req Mutation) (MutationResult, error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	var zero MutationResult
	kind, err := s.validateMutationInput(ref, req)
	if err != nil {
		return zero, err
	}
	unlock, err := s.writerLock(ctx)
	if err != nil {
		return zero, err
	}
	defer unlock()
	snap, err := s.Snapshot(ctx, ref)
	if err != nil {
		return zero, err
	}
	if projectID != "" && snap.project.ID != projectID {
		return zero, fail("invalid_configuration", "project")
	}
	if err := s.ValidateLocalBranch(ctx, ref); err != nil {
		return zero, err
	}
	intent := req
	intent.ExpectedHead = ""
	intent.ExpectedRevision = ""
	intent.IdempotencyKey = ""
	intent.WriteMode = ""
	raw, _ := json.Marshal(struct {
		Project  string   `json:"project"`
		Ref      string   `json:"ref"`
		Mutation Mutation `json:"mutation"`
	}{snap.project.ID, ref, intent})
	keyHash, intentHash := digest([]byte(req.IdempotencyKey)), digest(raw)
	prior, _, err := snap.Receipt(ctx, keyHash)
	if err == nil {
		if prior.IntentHash != intentHash {
			return zero, fail("idempotency_conflict", "intent")
		}
		e, err := snap.receiptResult(ctx, keyHash, prior)
		return MutationResult{e, true}, err
	}
	var absent *Error
	if !errors.As(err, &absent) || absent.Code != "not_found" {
		return zero, err
	}
	p, e, err := snap.prepareMutationEntity(ctx, req, kind)
	if err != nil {
		return zero, err
	}
	entityBytes, err := s.renderMutationEntity(e)
	if err != nil {
		return zero, err
	}
	path, _ := entityPath(kind, e.Metadata.ID, e.Metadata.ParentID)
	blob, err := s.writeBlob(ctx, entityBytes)
	if err != nil {
		return zero, err
	}
	projectBytes, err := yaml.Marshal(p)
	if err != nil || len(projectBytes) > s.limits.MetadataBytes {
		return zero, fail("limit_exceeded", "project")
	}
	projectBlob, err := s.writeBlob(ctx, projectBytes)
	if err != nil {
		return zero, err
	}
	receipt := Receipt{Schema: SchemaVersion, ProjectID: p.ID, KeyHash: keyHash, IntentHash: intentHash, Operation: req.Operation, Result: Reference{kind, e.Metadata.ID}, ResultBlob: blob}
	receiptBytes, _ := json.Marshal(receipt)
	if len(receiptBytes) > s.limits.MetadataBytes {
		return zero, fail("limit_exceeded", "receipt")
	}
	receiptBlob, err := s.writeBlob(ctx, receiptBytes)
	if err != nil {
		return zero, err
	}
	changes := map[string]repositorydocs.TreeEntry{}
	for path, oid := range map[string]string{path: blob, ".harness/project.yaml": projectBlob, ".harness/receipts/" + keyHash + ".json": receiptBlob} {
		changes[path] = repositorydocs.TreeEntry{Mode: "100644", Type: "blob", OID: oid}
	}
	base, err := s.run(ctx, 128, "rev-parse", snap.commit+"^{tree}")
	if err != nil {
		return zero, err
	}
	tree, err := s.patchTree(ctx, strings.TrimSpace(string(base)), changes)
	if err != nil {
		return zero, err
	}
	msg := []byte("localgit: " + req.Operation + " " + e.Metadata.ID + "\n")
	commitBytes, err := s.runInput(ctx, 128, msg, "commit-tree", tree, "-p", snap.commit)
	if err != nil {
		return zero, err
	}
	commit := strings.TrimSpace(string(commitBytes))
	if !oidValid(commit) {
		return zero, fail("unavailable", "commit")
	}
	// Validate the exact candidate before publication, including aggregate tree
	// capacity/counters. Unreferenced objects are normal Git garbage on rejection.
	candidate, err := s.Snapshot(ctx, commit)
	if err != nil {
		return zero, err
	}
	if _, err = candidate.Get(ctx, kind, e.Metadata.ID); err != nil {
		return zero, err
	}
	if s.hooks != nil && s.hooks.beforePublish != nil {
		if err := s.hooks.beforePublish(); err != nil {
			return zero, fail("unavailable", "before_publication")
		}
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	_, publishErr := s.run(ctx, 128, "update-ref", "--no-deref", ref, commit, snap.commit)
	if s.hooks != nil && s.hooks.afterPublish != nil && publishErr == nil {
		if err := s.hooks.afterPublish(); err != nil {
			return zero, fail("publication_ambiguous", "same_key_recovery")
		}
	}
	current, err := s.Snapshot(ctx, ref)
	if err != nil {
		return zero, fail("publication_ambiguous", "same_key_recovery")
	}
	confirmed, _, err := current.Receipt(ctx, keyHash)
	if err == nil && confirmed == receipt {
		entity, err := current.receiptResult(ctx, keyHash, confirmed)
		if err != nil {
			return zero, fail("publication_ambiguous", "same_key_recovery")
		}
		return MutationResult{Entity: entity}, nil
	}
	if publishErr != nil && current.commit != snap.commit && current.commit != commit {
		return zero, fail("write_conflict", "ref")
	}
	if publishErr != nil && current.commit == snap.commit {
		return zero, fail("publication_rejected", "ref")
	}
	return zero, fail("publication_ambiguous", "same_key_recovery")
}

// Configured authority is a real local branch, never a symbolic redirect.
func (s *Store) ValidateLocalBranch(ctx context.Context, ref string) error {
	if !strings.HasPrefix(ref, "refs/heads/") {
		return fail("invalid_argument", "revision")
	}
	if _, err := s.run(ctx, 128, "check-ref-format", ref); err != nil {
		return fail("invalid_argument", "revision")
	}
	sym, err := s.run(ctx, s.limits.TreeBytes, "for-each-ref", "--format=%(refname) %(symref)", ref)
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(sym), "\n") {
		name, target, _ := strings.Cut(line, " ")
		if name == ref && target != "" {
			return fail("unsupported_store", "symbolic_ref")
		}
	}
	return nil
}

func (s *Store) writeBlob(ctx context.Context, data []byte) (string, error) {
	out, err := s.runInput(ctx, 128, data, "hash-object", "--no-filters", "-w", "--stdin")
	if err != nil {
		return "", err
	}
	oid := strings.TrimSpace(string(out))
	if !oidValid(oid) {
		return "", fail("unavailable", "blob")
	}
	return oid, nil
}

// Only the changed fixed paths are rebuilt. Unchanged trees/blobs remain Git
// objects; this never writes a working tree or an index file.
func (s *Store) patchTree(ctx context.Context, tree string, changes map[string]repositorydocs.TreeEntry) (string, error) {
	entries := map[string]repositorydocs.TreeEntry{}
	if tree != "" {
		var err error
		entries, err = s.readTree(ctx, tree, "", false)
		if err != nil {
			return "", err
		}
	}
	groups := map[string]map[string]repositorydocs.TreeEntry{}
	for path, e := range changes {
		name, rest, nested := strings.Cut(path, "/")
		if !nested {
			e.Path = name
			entries[name] = e
			continue
		}
		if groups[name] == nil {
			groups[name] = map[string]repositorydocs.TreeEntry{}
		}
		groups[name][rest] = e
	}
	for name, group := range groups {
		old := entries[name]
		if old.OID != "" && (old.Type != "tree" || old.Mode != "040000") {
			return "", fail("invalid_schema", "directory")
		}
		oid, err := s.patchTree(ctx, old.OID, group)
		if err != nil {
			return "", err
		}
		entries[name] = repositorydocs.TreeEntry{Path: name, Mode: "040000", Type: "tree", OID: oid}
	}
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	var input strings.Builder
	for _, name := range names {
		e := entries[name]
		fmt.Fprintf(&input, "%s %s %s\t%s\x00", e.Mode, e.Type, e.OID, name)
	}
	out, err := s.runInput(ctx, 128, []byte(input.String()), "mktree", "-z")
	if err != nil {
		return "", err
	}
	oid := strings.TrimSpace(string(out))
	if !oidValid(oid) {
		return "", fail("unavailable", "tree")
	}
	return oid, nil
}

func (s *Snapshot) receiptResult(ctx context.Context, key string, receipt Receipt) (Entity, error) {
	path := ".harness/receipts/" + key + ".json"
	entry := s.entries[path]
	out, err := s.store.run(ctx, (s.store.limits.History+1)*65, "rev-list", "--first-parent", "--max-count="+strconv.Itoa(s.store.limits.History+1), s.commit, "--")
	if err != nil {
		return Entity{}, err
	}
	commits := strings.Fields(string(out))
	previous := ""
	for i, commit := range commits {
		if i >= s.store.limits.History {
			return Entity{}, fail("history_unavailable", "receipt_origin")
		}
		if !oidValid(commit) {
			return Entity{}, fail("invalid_schema", "receipt_history")
		}
		entries, err := s.store.tree(ctx, commit, path)
		if err != nil {
			return Entity{}, err
		}
		e, exists := entries[path]
		if !exists && previous != "" {
			return s.resultAt(ctx, previous, receipt)
		}
		if !exists || e.OID != entry.OID || e.Type != "blob" || e.Mode != "100644" {
			return Entity{}, fail("invalid_schema", "receipt_history")
		}
		previous = commit
	}
	if previous != "" {
		return s.resultAt(ctx, previous, receipt)
	}
	return Entity{}, fail("history_unavailable", "receipt_origin")
}

func (s *Snapshot) resultAt(ctx context.Context, commit string, receipt Receipt) (Entity, error) {
	origin, err := s.store.Snapshot(ctx, commit)
	if err != nil {
		return Entity{}, err
	}
	if origin.project.ID != receipt.ProjectID {
		return Entity{}, fail("invalid_schema", "receipt_project")
	}
	e, err := origin.Get(ctx, receipt.Result.Kind, receipt.Result.ID)
	if err != nil {
		return Entity{}, err
	}
	// A receipt must describe the operation's actual result, not merely point at
	// a well-formed blob. Share this check between replay and bundle validation.
	if e.Citation.Blob != receipt.ResultBlob || operationKind(receipt.Operation) != e.Metadata.Kind ||
		deleting(receipt.Operation) != (e.Metadata.State == "deleted") ||
		deleting(receipt.Operation) && e.Body != "" {
		return Entity{}, fail("invalid_schema", "receipt_result")
	}
	// Qualify the operation against its original parent, never today's state.
	out, err := s.store.run(ctx, 140, "rev-list", "--parents", "--max-count=1", commit, "--")
	if err != nil {
		return Entity{}, err
	}
	parents := strings.Fields(string(out))
	if len(parents) != 2 || parents[0] != commit || !oidValid(parents[1]) {
		return Entity{}, fail("invalid_schema", "receipt_origin")
	}
	parent, err := s.store.Snapshot(ctx, parents[1])
	if err != nil {
		return Entity{}, err
	}
	if parent.project.ID != receipt.ProjectID {
		return Entity{}, fail("invalid_schema", "receipt_origin")
	}
	if creating(receipt.Operation) {
		// Creation consumes a fresh ID, not a pre-existing entity.
		n, err := idNumber(e.Metadata.Kind, e.Metadata.ID)
		before := map[Kind]int64{Issue: parent.project.NextIssue, Comment: parent.project.NextComment, Wiki: parent.project.NextDocument}[e.Metadata.Kind]
		after := map[Kind]int64{Issue: origin.project.NextIssue, Comment: origin.project.NextComment, Wiki: origin.project.NextDocument}[e.Metadata.Kind]
		if err != nil || parent.entities[e.Metadata.ID] != "" || n != before || after != before+1 {
			return Entity{}, fail("invalid_schema", "receipt_origin")
		}
		if e.Metadata.Kind == Comment {
			issue, err := parent.Get(ctx, Issue, e.Metadata.ParentID)
			if err != nil {
				return Entity{}, err
			}
			if issue.Metadata.State == "deleted" {
				return Entity{}, fail("invalid_schema", "receipt_origin")
			}
		}
	} else {
		previous, err := parent.Get(ctx, e.Metadata.Kind, e.Metadata.ID)
		if err != nil {
			return Entity{}, err
		}
		if previous.Metadata.State == "deleted" {
			return Entity{}, fail("invalid_schema", "receipt_origin")
		}
	}
	return e, nil
}

func (s *Store) validateMutationInput(ref string, req Mutation) (Kind, error) {
	kind := operationKind(req.Operation)
	if kind == "" || req.WriteMode != "local" || !strings.HasPrefix(ref, "refs/heads/") {
		return "", fail("invalid_argument", "local_intent")
	}
	if !validText(req.IdempotencyKey, 256) {
		return "", fail("invalid_argument", "idempotency_key")
	}
	if len(req.Body) > s.limits.FileBytes || !utf8.ValidString(req.Body) || strings.ContainsRune(req.Body, 0) {
		return "", fail("invalid_argument", "body")
	}
	if req.ExpectedHead != "" && !oidValid(req.ExpectedHead) || req.ExpectedRevision != "" && !oidValid(req.ExpectedRevision) {
		return "", fail("invalid_argument", "revision")
	}
	if creating(req.Operation) {
		if req.ID != "" || req.ExpectedHead == "" || req.ExpectedRevision != "" {
			return "", fail("invalid_argument", "create_guard")
		}
		if kind == Comment {
			if _, err := idNumber(Issue, req.ParentID); err != nil {
				return "", err
			}
		} else if req.ParentID != "" {
			return "", fail("invalid_argument", "parent_id")
		}
	} else {
		if _, err := idNumber(kind, req.ID); err != nil {
			return "", err
		}
		if req.ParentID != "" || req.ExpectedHead == "" && req.ExpectedRevision == "" {
			return "", fail("invalid_argument", "update_guard")
		}
	}
	if deleting(req.Operation) && (req.Title != "" || req.Body != "" || req.State != "" || len(req.Labels) > 0 || req.Milestone != "" || len(req.Links) > 0) {
		return "", fail("invalid_argument", "delete_payload")
	}
	// Bound every caller-controlled metadata field before hashing or acquiring
	// the writer lane. Generated identity and an update's immutable parent are
	// placeholders here; the actual entity is validated again before publication.
	if !deleting(req.Operation) {
		id := req.ID
		if creating(req.Operation) {
			id = map[Kind]string{Issue: "ISSUE-1", Comment: "COMMENT-1", Wiki: "DOC-1"}[kind]
		}
		parent, state := req.ParentID, req.State
		if kind == Comment && parent == "" {
			parent = "ISSUE-1"
		}
		if creating(req.Operation) && state == "" {
			state = "active"
			if kind == Issue {
				state = "open"
			}
		}
		if state == "deleted" {
			return "", fail("invalid_argument", "state")
		}
		if err := validateMetadata(Metadata{Schema: SchemaVersion, Kind: kind, ID: id, ParentID: parent, Title: req.Title, State: state, Labels: req.Labels, Milestone: req.Milestone, Links: req.Links}); err != nil {
			return "", err
		}
	}
	return kind, nil
}

func (snap *Snapshot) prepareMutationEntity(ctx context.Context, req Mutation, kind Kind) (Project, Entity, error) {
	var err error
	if req.ExpectedHead != "" && req.ExpectedHead != snap.commit {
		return Project{}, Entity{}, fail("write_conflict", "head")
	}
	p := snap.project
	var e Entity
	if creating(req.Operation) {
		next := map[Kind]*int64{Issue: &p.NextIssue, Comment: &p.NextComment, Wiki: &p.NextDocument}[kind]
		if *next >= maxID {
			return Project{}, Entity{}, fail("limit_exceeded", "counters")
		}
		prefix := map[Kind]string{Issue: "ISSUE-", Comment: "COMMENT-", Wiki: "DOC-"}[kind]
		e.Metadata = Metadata{Schema: SchemaVersion, Kind: kind, ID: prefix + strconv.FormatInt(*next, 10), ParentID: req.ParentID}
		(*next)++
		if kind == Comment {
			parent, err := snap.Get(ctx, Issue, req.ParentID)
			if err != nil {
				return Project{}, Entity{}, err
			}
			if parent.Metadata.State == "deleted" {
				return Project{}, Entity{}, fail("write_conflict", "parent_deleted")
			}
		}
	} else {
		e, err = snap.Get(ctx, kind, req.ID)
		if err != nil {
			return Project{}, Entity{}, err
		}
		if req.ExpectedRevision != "" && req.ExpectedRevision != e.Citation.Blob {
			return Project{}, Entity{}, fail("write_conflict", "revision")
		}
		if e.Metadata.State == "deleted" {
			return Project{}, Entity{}, fail("write_conflict", "deleted")
		}
	}
	if deleting(req.Operation) {
		e.Metadata.State = "deleted"
		e.Body = ""
	} else {
		e.Metadata.Title = req.Title
		e.Metadata.State = req.State
		if creating(req.Operation) && e.Metadata.State == "" {
			e.Metadata.State = "active"
			if kind == Issue {
				e.Metadata.State = "open"
			}
		}
		if e.Metadata.State == "deleted" {
			return Project{}, Entity{}, fail("invalid_argument", "state")
		}
		e.Metadata.Labels = req.Labels
		e.Metadata.Milestone = req.Milestone
		e.Metadata.Links = req.Links
		e.Body = req.Body
	}
	if err := validateMetadata(e.Metadata); err != nil {
		return Project{}, Entity{}, err
	}
	return p, e, nil
}

func (s *Store) renderMutationEntity(e Entity) ([]byte, error) {
	entityYAML, err := yaml.Marshal(e.Metadata)
	if err != nil {
		return nil, fail("invalid_schema", "metadata")
	}
	if len(entityYAML) > s.limits.MetadataBytes {
		return nil, fail("limit_exceeded", "metadata")
	}
	entityBytes := []byte("---\n" + string(entityYAML) + "---\n" + e.Body)
	if len(entityBytes) > s.limits.FileBytes {
		return nil, fail("limit_exceeded", "file_bytes")
	}
	return entityBytes, nil
}

// PreviewMutation validates the same typed intent and current Git guards as a
// write, but creates no objects, receipts, locks or ref updates. Apply still
// delegates to MutateProject so known receipts resolve before stale guards.
func (s *Store) PreviewMutation(ctx context.Context, ref, projectID string, req Mutation) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	kind, err := s.validateMutationInput(ref, req)
	if err != nil {
		return "", err
	}
	snap, err := s.Snapshot(ctx, ref)
	if err != nil {
		return "", err
	}
	if snap.Project().ID != projectID {
		return "", fail("invalid_configuration", "project")
	}
	if err := s.ValidateLocalBranch(ctx, ref); err != nil {
		return "", err
	}
	_, e, err := snap.prepareMutationEntity(ctx, req, kind)
	if err != nil {
		return "", err
	}
	if _, err := s.renderMutationEntity(e); err != nil {
		return "", err
	}
	return snap.Commit(), nil
}
