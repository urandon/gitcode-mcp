package localauthority

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gitcode-mcp/internal/authority"
)

// This reader/writer understands exactly the owned loose format. It never
// consults ambient Git configuration, executes helpers or follows alternates.
// The history bound is a refusal, not permission to evict authoritative data.
const maxKnowledgeLooseObjects = 65536
const maxKnowledgeCompressedBytes = maxKnowledgeTreeBytes + 1024

type knowledgeCapacityError struct{ category string }

func (e knowledgeCapacityError) Error() string          { return (Error{Capacity}).Error() }
func (e knowledgeCapacityError) DiagnosticCode() string { return string(Capacity) }
func (e knowledgeCapacityError) Unwrap() error          { return Error{Capacity} }

type knowledgeRepository struct {
	directory      string // trusted local coordinate, never serialized
	physical       string
	fault          func(string) error
	ctx            context.Context // operation-local; never durable or configurable
	readCheckpoint func(string)    // test-only cancellation observation, never public input
}

func (g knowledgeRepository) withContext(ctx context.Context) knowledgeRepository {
	g.ctx = ctx
	return g
}

func (g knowledgeRepository) checkpoint(phase string) error {
	if g.ctx != nil && g.ctx.Err() != nil {
		return g.ctx.Err()
	}
	if phase != "" && g.readCheckpoint != nil {
		g.readCheckpoint(phase)
	}
	if g.ctx != nil {
		return g.ctx.Err()
	}
	return nil
}

func knowledgeDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 || directoryOwner(path) != nil {
		return Error{KnowledgeCorrupt}
	}
	return nil
}

func knowledgeDirectoryIdentity(path string) (string, error) {
	if err := knowledgeDirectory(path); err != nil {
		return "", err
	}
	f, err := os.Open(path)
	if err != nil {
		return "", Error{KnowledgeCorrupt}
	}
	defer f.Close()
	return physicalIdentity(f)
}

func readKnowledgeFile(path string, limit int) ([]byte, error) {
	f, err := openExistingFile(path)
	if err != nil {
		return nil, Error{KnowledgeCorrupt}
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() > int64(limit) {
		return nil, Error{KnowledgeCorrupt}
	}
	body, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil || len(body) > limit {
		return nil, Error{KnowledgeCorrupt}
	}
	return body, nil
}

func (g knowledgeRepository) verifyPhysical() error {
	if err := g.checkpoint(""); err != nil {
		return err
	}
	id, err := knowledgeDirectoryIdentity(g.directory)
	if err != nil || id != g.physical {
		return Error{KnowledgeCorrupt}
	}
	return nil
}

func (g knowledgeRepository) inject(phase string) error {
	if err := g.checkpoint(""); err != nil {
		return err
	}
	if g.fault != nil {
		if err := g.fault(phase); err != nil {
			return err
		}
	}
	return g.checkpoint("")
}

func knowledgeNames(path string, limit int) ([]os.DirEntry, error) {
	if err := knowledgeDirectory(path); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, Error{KnowledgeCorrupt}
	}
	defer f.Close()
	entries, err := f.ReadDir(limit + 1)
	if err != nil && err != io.EOF {
		return nil, Error{KnowledgeCorrupt}
	}
	if len(entries) > limit {
		return nil, Error{Capacity}
	}
	return entries, nil
}

func exactKnowledgeNames(path string, expected ...string) error {
	entries, err := knowledgeNames(path, len(expected))
	if err != nil {
		// A fixed namespace cannot legitimately exceed this count. Extra
		// entries are foreign layout, not ordinary history capacity pressure.
		return Error{KnowledgeCorrupt}
	}
	if len(entries) != len(expected) {
		return Error{KnowledgeCorrupt}
	}
	want := map[string]bool{}
	for _, name := range expected {
		want[name] = true
	}
	for _, entry := range entries {
		if !want[entry.Name()] {
			return Error{KnowledgeCorrupt}
		}
	}
	return nil
}

// verifyLayout refuses foreign formats/config, packed state, alternates,
// hooks, replacement refs, worktrees, symbolic links and hard-linked files.
// It inspects bounded names/metadata; object bytes are verified by exact OID.
func (g knowledgeRepository) verifyLayout() (objects, pins int, err error) {
	return g.verifyJournalLayout(nil)
}

// A narrowly scoped original journal proof can account for a paired lock.
// Ordinary layout/read/setup callers never receive that permission. The proof
// must name the exact durable candidate and is checked against actual objects,
// not inferred from equal OIDs or the current head.
func (g knowledgeRepository) verifyJournalLayout(original *documentOperation) (objects, pins int, err error) {
	if err = g.verifyPhysical(); err != nil {
		return
	}
	if err = exactKnowledgeNames(g.directory, "HEAD", "config", "objects", "refs"); err != nil {
		return
	}
	for name, expected := range map[string]string{"HEAD": knowledgeHEAD, "config": knowledgeConfig} {
		var body []byte
		body, err = readKnowledgeFile(filepath.Join(g.directory, name), len(expected))
		if err != nil || string(body) != expected {
			err = Error{KnowledgeCorrupt}
			return
		}
	}
	if err = exactKnowledgeNames(filepath.Join(g.directory, "refs"), "heads", "pins"); err != nil {
		return
	}
	var entries []os.DirEntry
	entries, err = knowledgeNames(filepath.Join(g.directory, "refs", "heads"), 2)
	if err != nil {
		err = Error{KnowledgeCorrupt}
		return
	}
	headNames := map[string]bool{}
	for _, entry := range entries {
		if err = g.checkpoint(""); err != nil {
			return
		}
		if entry.Name() != "knowledge" && entry.Name() != "knowledge.lock" {
			err = Error{KnowledgeCorrupt}
			return
		}
		headNames[entry.Name()] = true
		if _, err = g.readRef(filepath.Join("heads", entry.Name())); err != nil {
			return
		}
	}
	if headNames["knowledge"] && headNames["knowledge.lock"] {
		if original == nil || original.Phase != "bound" {
			err = Error{KnowledgeCorrupt}
			return
		}
		if err = g.verifyDocumentEvidence(*original); err != nil {
			return
		}
		current, e := g.readRef(filepath.Join("heads", "knowledge"))
		if e != nil {
			err = e
			return
		}
		lock, le := g.readRefCheckpoint(filepath.Join("heads", "knowledge.lock"), "knowledge_layout_head_pair")
		if le != nil {
			err = le
			return
		}
		if lock != original.commitOID() || current != original.Metadata.ExpectedRefOID && current != lock {
			err = Error{KnowledgeCorrupt}
			return
		}
	}
	entries, err = knowledgeNames(filepath.Join(g.directory, "refs", "pins"), maxRetainedKnowledgePins*2)
	if err != nil {
		var e Error
		if errors.As(err, &e) && e.Code == Capacity {
			err = knowledgeCapacityError{"retained_pins"}
		}
		return
	}
	seenPins := map[string]bool{}
	for _, entry := range entries {
		if err = g.checkpoint(""); err != nil {
			return
		}
		name := strings.TrimSuffix(entry.Name(), ".lock")
		if !authority.UUID(name).Valid() {
			err = Error{KnowledgeCorrupt}
			return
		}
		if seenPins[name] {
			// A paired final/lock is ambiguous evidence, even if both OIDs
			// match. It is not one ordinary retained pin. Only an original
			// journal-owned recovery transition may later interpret the pair.
			if original == nil || name != string(original.ID) || original.Phase == "settled" {
				err = Error{KnowledgeCorrupt}
				return
			}
			if err = g.verifyDocumentEvidence(*original); err != nil {
				return
			}
			final, e := g.readRef(filepath.Join("pins", name))
			if e != nil {
				err = e
				return
			}
			lock, le := g.readRefCheckpoint(filepath.Join("pins", name+".lock"), "knowledge_layout_pin_pair")
			if le != nil {
				err = le
				return
			}
			if final != original.commitOID() || lock != final {
				err = Error{KnowledgeCorrupt}
				return
			}
		}
		seenPins[name] = true
		if _, err = g.readRef(filepath.Join("pins", entry.Name())); err != nil {
			return
		}
	}
	pins = len(seenPins)
	if pins > maxRetainedKnowledgePins {
		err = knowledgeCapacityError{"retained_pins"}
		return
	}
	entries, err = knowledgeNames(filepath.Join(g.directory, "objects"), 256)
	if err != nil {
		err = Error{KnowledgeCorrupt}
		return
	}
	for _, fanout := range entries {
		if err = g.checkpoint(""); err != nil {
			return
		}
		name := fanout.Name()
		if len(name) != 2 || name != strings.ToLower(name) {
			err = Error{KnowledgeCorrupt}
			return
		}
		if _, e := hex.DecodeString(name); e != nil {
			err = Error{KnowledgeCorrupt}
			return
		}
		var files []os.DirEntry
		files, err = knowledgeNames(filepath.Join(g.directory, "objects", name), maxKnowledgeLooseObjects-objects)
		if err != nil {
			var e Error
			if errors.As(err, &e) && e.Code == Capacity {
				err = knowledgeCapacityError{"loose_objects"}
			}
			return
		}
		for _, file := range files {
			if err = g.checkpoint("knowledge_layout_object"); err != nil {
				return
			}
			if !knowledgeOIDValid(name+file.Name(), false) {
				err = Error{KnowledgeCorrupt}
				return
			}
			f, e := openExistingFile(filepath.Join(g.directory, "objects", name, file.Name()))
			if e != nil {
				err = Error{KnowledgeCorrupt}
				return
			}
			info, e := f.Stat()
			_ = f.Close()
			if e != nil || info.Size() < 1 || info.Size() > int64(maxKnowledgeCompressedBytes) {
				err = Error{KnowledgeCorrupt}
				return
			}
			objects++
		}
	}
	return
}

func (g knowledgeRepository) readObject(oid, kind string) (knowledgeObject, error) {
	if err := g.checkpoint(""); err != nil {
		return knowledgeObject{}, err
	}
	if !knowledgeOIDValid(oid, false) {
		return knowledgeObject{}, Error{KnowledgeCorrupt}
	}
	if err := g.verifyPhysical(); err != nil {
		return knowledgeObject{}, err
	}
	if knowledgeDirectory(filepath.Join(g.directory, "objects")) != nil || knowledgeDirectory(filepath.Join(g.directory, "objects", oid[:2])) != nil {
		return knowledgeObject{}, Error{KnowledgeCorrupt}
	}
	body, err := readKnowledgeFile(filepath.Join(g.directory, "objects", oid[:2], oid[2:]), maxKnowledgeCompressedBytes)
	if err != nil {
		return knowledgeObject{}, err
	}
	compressed := bytes.NewReader(body)
	r, err := zlib.NewReader(compressed)
	if err != nil {
		return knowledgeObject{}, Error{KnowledgeCorrupt}
	}
	frame, err := io.ReadAll(io.LimitReader(r, int64(maxKnowledgeTreeBytes)+65))
	closed := r.Close()
	if err != nil || closed != nil || compressed.Len() != 0 || len(frame) > maxKnowledgeTreeBytes+64 {
		return knowledgeObject{}, Error{KnowledgeCorrupt}
	}
	boundary := bytes.IndexByte(frame, 0)
	if boundary < 1 || boundary > 32 {
		return knowledgeObject{}, Error{KnowledgeCorrupt}
	}
	object, err := newKnowledgeObject(kind, frame[boundary+1:])
	if err != nil || object.oid != oid || string(frame[:boundary]) != kind+" "+strconv.Itoa(len(object.data)) {
		return knowledgeObject{}, Error{KnowledgeCorrupt}
	}
	if err := g.checkpoint(""); err != nil {
		return knowledgeObject{}, err
	}
	return object, nil
}

func (g knowledgeRepository) writeObject(object knowledgeObject) error {
	if err := g.checkpoint(""); err != nil {
		return err
	}
	canonical, err := newKnowledgeObject(object.kind, object.data)
	if err != nil || canonical.oid != object.oid {
		return Error{KnowledgeCorrupt}
	}
	if err := g.verifyPhysical(); err != nil {
		return err
	}
	objectsDir := filepath.Join(g.directory, "objects")
	if knowledgeDirectory(objectsDir) != nil {
		return Error{KnowledgeCorrupt}
	}
	fanout := filepath.Join(objectsDir, object.oid[:2])
	if err = os.Mkdir(fanout, 0700); err != nil && !os.IsExist(err) {
		return Error{DurabilityUnavailable}
	}
	if knowledgeDirectory(fanout) != nil {
		return Error{KnowledgeCorrupt}
	}
	path := filepath.Join(fanout, object.oid[2:])
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if os.IsExist(err) {
		saved, e := g.readObject(object.oid, object.kind)
		if e != nil || !bytes.Equal(saved.data, object.data) {
			return Error{KnowledgeCorrupt}
		}
		f, err = openExistingFile(path)
		if err != nil {
			return Error{KnowledgeCorrupt}
		}
	} else if err == nil {
		z := zlib.NewWriter(f)
		_, err = io.WriteString(z, object.kind+" "+strconv.Itoa(len(object.data))+"\x00")
		if err == nil {
			_, err = z.Write(object.data)
		}
		closed := z.Close()
		if err == nil {
			err = closed
		}
		if err != nil {
			_ = f.Close()
			// A torn original candidate is preserved, never overwritten or
			// interpreted as proof of rollback. Recovery needs operator evidence.
			return Error{DocumentRecoveryRequired}
		}
	} else {
		return Error{DurabilityUnavailable}
	}
	defer f.Close()
	if err = syncFile(f); err != nil {
		return err
	}
	if err = g.inject("knowledge_object_file"); err != nil {
		return err
	}
	if err = syncDirectory(fanout); err == nil {
		err = syncDirectory(objectsDir)
	}
	if err != nil {
		return err
	}
	if err = g.verifyPhysical(); err != nil {
		return err
	}
	if _, err = g.readObject(object.oid, object.kind); err != nil {
		return err
	}
	return g.inject("knowledge_object_directory")
}

func knowledgeRefName(name string) bool {
	if name == filepath.Join("heads", "knowledge") || name == filepath.Join("heads", "knowledge.lock") {
		return true
	}
	parts := strings.Split(filepath.ToSlash(name), "/")
	return len(parts) == 2 && parts[0] == "pins" && authority.UUID(strings.TrimSuffix(parts[1], ".lock")).Valid()
}

func (g knowledgeRepository) readRef(name string) (string, error) {
	return g.readRefCheckpoint(name, "")
}

// Preserve the established explicit-recovery classification for a missing or
// malformed journal ref without claiming interruption is damaged evidence.
// Call only after a non-nil read error at a recovery-classified boundary.
func recoveryRefError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return Error{DocumentRecoveryRequired}
}

// Fixed internal checkpoint names support deterministic interruption tests;
// neither a ref name nor a local coordinate is delivered to the observer.
func (g knowledgeRepository) readRefCheckpoint(name, phase string) (string, error) {
	if err := g.checkpoint(phase); err != nil {
		return "", err
	}
	if !knowledgeRefName(name) {
		return "", Error{KnowledgeCorrupt}
	}
	if err := g.verifyPhysical(); err != nil {
		return "", err
	}
	if knowledgeDirectory(filepath.Join(g.directory, "refs")) != nil || knowledgeDirectory(filepath.Join(g.directory, "refs", filepath.Dir(name))) != nil {
		return "", Error{KnowledgeCorrupt}
	}
	path := filepath.Join(g.directory, "refs", name)
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return authority.EmptyKnowledgeRef, nil
	}
	body, err := readKnowledgeFile(path, 65)
	if err != nil || len(body) != 65 || body[64] != '\n' || !knowledgeOIDValid(string(body[:64]), false) {
		return "", Error{KnowledgeCorrupt}
	}
	return string(body[:64]), nil
}

// publishRef is serialized by the authority writer, after verified SQL binding.
// An exact unborn-ref lock can resume. Paired final/lock recovery requires an
// explicit journal-owned transition; partial/foreign evidence is preserved.
// It never implements a hidden fetch, force-update or weaker ref acknowledgement.
func (g knowledgeRepository) publishRef(name, expected, next string) error {
	if err := g.checkpoint(""); err != nil {
		return err
	}
	if !knowledgeRefName(name) || strings.HasSuffix(name, ".lock") || !knowledgeOIDValid(expected, true) || !knowledgeOIDValid(next, false) {
		return Error{KnowledgeCorrupt}
	}
	current, err := g.readRef(name)
	if err != nil {
		return err
	}
	if current != authority.EmptyKnowledgeRef {
		if _, e := os.Lstat(filepath.Join(g.directory, "refs", name) + ".lock"); e == nil {
			// This primitive has no journal proof authorizing a paired-state
			// recovery. Do not infer ownership/cleanup from current == next.
			return Error{DocumentRecoveryRequired}
		} else if !os.IsNotExist(e) {
			return Error{KnowledgeCorrupt}
		}
	}
	if current != expected && current != next {
		return Error{DocumentRecoveryRequired}
	}
	path := filepath.Join(g.directory, "refs", name)
	if current != next {
		lock := path + ".lock"
		f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if os.IsExist(err) {
			saved, e := g.readRefCheckpoint(name+".lock", "knowledge_ref_reuse")
			if e != nil {
				return recoveryRefError(e)
			}
			if saved != next {
				return Error{DocumentRecoveryRequired}
			}
			f, err = openExistingFile(lock)
		} else if err == nil {
			_, err = io.WriteString(f, next+"\n")
		}
		if err != nil {
			if f != nil {
				_ = f.Close()
			}
			return Error{DocumentRecoveryRequired}
		}
		err = syncFile(f)
		_ = f.Close()
		if err != nil {
			return err
		}
		if err = g.inject("knowledge_ref_file"); err != nil {
			return err
		}
		current, err = g.readRef(name)
		if err != nil {
			return recoveryRefError(err)
		}
		if current != expected {
			return Error{DocumentRecoveryRequired}
		}
		if err = g.verifyPhysical(); err != nil {
			return recoveryRefError(err)
		}
		if err = os.Rename(lock, path); err != nil {
			return Error{DocumentRecoveryRequired}
		}
		if err = g.inject("knowledge_ref_rename"); err != nil {
			return err
		}
	}
	f, err := openExistingFile(path)
	if err != nil {
		return Error{KnowledgeCorrupt}
	}
	err = syncFile(f)
	_ = f.Close()
	if err == nil {
		err = syncDirectory(filepath.Dir(path))
	}
	if err == nil {
		err = syncDirectory(filepath.Join(g.directory, "refs"))
	}
	if err != nil {
		return err
	}
	if current, err = g.readRef(name); err != nil {
		return recoveryRefError(err)
	}
	if current != next {
		return Error{DocumentRecoveryRequired}
	}
	return g.inject("knowledge_ref_directory")
}

func (g knowledgeRepository) treeAt(commitOID string) (map[authority.UUID]string, error) {
	if commitOID == authority.EmptyKnowledgeRef {
		return map[authority.UUID]string{}, nil
	}
	commit, err := g.readObject(commitOID, "commit")
	if err != nil {
		return nil, err
	}
	rootOID, _, _, err := decodeKnowledgeCommit(commit.data)
	if err != nil {
		return nil, err
	}
	root, err := g.readObject(rootOID, "tree")
	if err != nil {
		return nil, err
	}
	documentsOID, err := decodeKnowledgeRoot(root.data)
	if err != nil {
		return nil, err
	}
	documents, err := g.readObject(documentsOID, "tree")
	if err != nil {
		return nil, err
	}
	return decodeDocumentTree(documents.data)
}
