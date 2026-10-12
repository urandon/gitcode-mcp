package localauthority

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gitcode-mcp/internal/authority"
)

const maxInitialStoreBytes = 1 << 20

type pendingInitialization struct {
	Schema            int            `json:"schema"`
	Project           authority.UUID `json:"project"`
	Instance          authority.UUID `json:"instance"`
	DirectoryHash     string         `json:"directory_hash"`
	ManifestHash      string         `json:"manifest_hash"`
	PhysicalHash      string         `json:"physical_hash"`
	ContentHash       string         `json:"content_hash"`
	StagePhysicalHash string         `json:"stage_physical_hash"`
}

func (p *pendingInitialization) Validate() error {
	if p.Schema != 1 || !p.Project.Valid() || !p.Instance.Valid() || !validDigest(p.DirectoryHash) || !validDigest(p.ManifestHash) || !validDigest(p.PhysicalHash) || !validDigest(p.ContentHash) || !validDigest(p.StagePhysicalHash) {
		return Error{Corrupt}
	}
	return nil
}

func manifestHash(m authority.Manifest) string { body, _ := json.Marshal(m); return hash(body) }
func stageName(instance authority.UUID) string { return ".authority-init-" + string(instance) }
func initPath(r *Registry, id authority.UUID) string {
	return filepath.Join(r.root, string(id)+".init")
}
func initTempPath(r *Registry, p pendingInitialization) string {
	return initPath(r, p.Project) + "-" + string(p.Instance) + ".tmp"
}

// Initialize is an explicit same-manifest publication intent, never promotion
// of a cache or an unknown file. No writer binding is published during staging.
func Initialize(ctx context.Context, registry *Registry, directory string, m authority.Manifest) (*Store, error) {
	return initialize(ctx, registry, directory, m, nil)
}

func initialize(ctx context.Context, registry *Registry, directory string, m authority.Manifest, fault func(string) error) (*Store, error) {
	if m.Validate() != nil {
		return nil, authority.Error{Code: authority.InvalidContract}
	}
	path, err := privateDirectory(directory)
	if err != nil {
		return nil, err
	}
	if err = qualifyFilesystem(path); err != nil {
		return nil, err
	}
	owner, err := registry.lockProject(ctx, m.ID)
	if err != nil {
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			_ = owner.close()
		}
	}()
	inject := func(phase string) error {
		if fault != nil {
			return fault(phase)
		}
		return nil
	}
	if err = inject("init_lease"); err != nil {
		return nil, err
	}
	p, exists, err := readPending(registry, m.ID)
	if err != nil {
		return nil, err
	}
	final := filepath.Join(path, "tracker.db")
	if exists {
		if p.Project != m.ID || p.DirectoryHash != hash([]byte(path)) || p.ManifestHash != manifestHash(m) {
			return nil, authority.Error{Code: authority.Conflict}
		}
	} else {
		// Existing final data is reusable only when already bound to this exact
		// immutable intent. Otherwise even a valid SQLite header is not consent.
		if _, statErr := os.Lstat(final); statErr == nil {
			s, err := openInitialized(ctx, owner, path, m)
			if err != nil {
				return nil, err
			}
			success = true
			return s, nil
		} else if !os.IsNotExist(statErr) {
			return nil, Error{DurabilityUnavailable}
		}
		body, err := readOwner(owner)
		if err != nil {
			return nil, err
		}
		if len(body) != 0 {
			return nil, authority.Error{Code: authority.Conflict}
		}
		if err = permitInitialStage(ctx, path); err != nil {
			return nil, err
		}
		instance, err := authority.NewUUID()
		if err != nil {
			return nil, err
		}
		stage := filepath.Join(path, stageName(instance))
		if os.Mkdir(stage, 0700) != nil {
			return nil, Error{DurabilityUnavailable}
		}
		if err = syncDirectory(path); err != nil {
			return nil, err
		}
		if err = inject("init_stage_directory"); err != nil {
			return nil, err
		}
		prepared, err := prepareEmpty(ctx, stage, m, instance, func(phase string) error { return inject(phase) })
		if err != nil {
			return nil, err
		}
		if err = prepared.Close(); err != nil {
			return nil, err
		}
		if err = syncDirectory(stage); err != nil {
			return nil, err
		}
		if err = inject("init_stage_closed"); err != nil {
			return nil, err
		}
		check, err := OpenReadOnly(ctx, stage, m.ID)
		if err != nil {
			return nil, err
		}
		if check.instance != instance || manifestHash(check.manifest) != manifestHash(m) {
			_ = check.Close()
			return nil, Error{Corrupt}
		}
		if err = check.Close(); err != nil {
			return nil, err
		}
		physical, content, err := initialFile(filepath.Join(stage, "tracker.db"))
		if err != nil {
			return nil, err
		}
		stagePhysical, err := initialDirectory(stage)
		if err != nil {
			return nil, err
		}
		p = pendingInitialization{Schema: 1, Project: m.ID, Instance: instance, DirectoryHash: hash([]byte(path)), ManifestHash: manifestHash(m), PhysicalHash: physical, ContentHash: content, StagePhysicalHash: stagePhysical}
		if err = publishPending(registry, p, inject); err != nil {
			return nil, err
		}
	}
	if err = finishInitialization(ctx, registry, owner, path, m, p, inject); err != nil {
		return nil, err
	}
	s, err := openInitialized(ctx, owner, path, m)
	if err != nil {
		return nil, err
	}
	if err = inject("init_acknowledgement"); err != nil {
		_ = s.Close()
		return nil, err
	}
	if ctx.Err() != nil {
		_ = s.Close()
		return nil, ctx.Err()
	}
	success = true
	return s, nil
}

func permitInitialStage(ctx context.Context, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return Error{DurabilityUnavailable}
	}
	defer f.Close()
	seen, stages := 0, 0
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		entries, err := f.ReadDir(128)
		for _, entry := range entries {
			seen++
			if strings.HasPrefix(entry.Name(), ".authority-init-") {
				stages++
			}
		}
		if seen > 4096 || stages >= 32 {
			return Error{Capacity}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return Error{DurabilityUnavailable}
		}
	}
}

func initialDirectory(path string) (string, error) {
	if _, err := privateDirectory(path); err != nil {
		return "", err
	}
	f, err := os.Open(path)
	if err != nil {
		return "", Error{DurabilityUnavailable}
	}
	defer f.Close()
	return physicalIdentity(f)
}

func initialFile(path string) (string, string, error) {
	f, err := openInitializationFile(path)
	if err != nil {
		return "", "", err
	}
	defer f.Close()
	physical, err := physicalIdentity(f)
	if err != nil {
		return "", "", err
	}
	bytes, err := io.ReadAll(io.LimitReader(f, maxInitialStoreBytes+1))
	if err != nil || len(bytes) > maxInitialStoreBytes {
		return "", "", Error{Corrupt}
	}
	return physical, hash(bytes), nil
}

func readPending(r *Registry, id authority.UUID) (pendingInitialization, bool, error) {
	var p pendingInitialization
	path := initPath(r, id)
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return p, false, nil
	} else if err != nil {
		return p, false, Error{Corrupt}
	}
	f, err := openInitializationFile(path)
	if err != nil {
		return p, false, err
	}
	defer f.Close()
	body, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(body) > 4096 || authority.DecodeJSON(body, &p) != nil || p.Project != id {
		return p, false, Error{Corrupt}
	}
	// An interrupted exclusive hard-link publication may retain the known
	// temporary metadata alias. Remove only an identical, verified inode.
	temp := initTempPath(r, p)
	if _, err := os.Lstat(temp); err == nil {
		other, err := openInitializationFile(temp)
		if err != nil {
			return p, false, err
		}
		a, _ := physicalIdentity(f)
		b, e := physicalIdentity(other)
		_ = other.Close()
		if e != nil || a != b {
			return p, false, Error{Corrupt}
		}
		if os.Remove(temp) != nil {
			return p, false, Error{DurabilityUnavailable}
		}
		if err = syncDirectory(r.root); err != nil {
			return p, false, err
		}
	} else if !os.IsNotExist(err) {
		return p, false, Error{Corrupt}
	}
	unique, err := openExistingFile(path)
	if err != nil {
		return p, false, err
	}
	a, e := physicalIdentity(unique)
	_ = unique.Close()
	b, e2 := physicalIdentity(f)
	if e != nil || e2 != nil || a != b {
		return p, false, Error{Corrupt}
	}
	return p, true, nil
}

func publishPending(r *Registry, p pendingInitialization, inject func(string) error) error {
	body, err := json.Marshal(p)
	if err != nil || p.Validate() != nil {
		return Error{Corrupt}
	}
	temp := initTempPath(r, p)
	f, err := os.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return Error{DurabilityUnavailable}
	}
	if _, err = f.Write(body); err == nil {
		err = syncFile(f)
	}
	_ = f.Close()
	if err != nil {
		return Error{DurabilityUnavailable}
	}
	if err = inject("init_journal_prepared"); err != nil {
		return err
	}
	if os.Link(temp, initPath(r, p.Project)) != nil {
		return authority.Error{Code: authority.Conflict}
	}
	if err = inject("init_journal_linked"); err != nil {
		return err
	}
	if err = syncDirectory(r.root); err != nil {
		return err
	}
	if os.Remove(temp) != nil {
		return Error{DurabilityUnavailable}
	}
	if err = syncDirectory(r.root); err != nil {
		return err
	}
	return inject("init_journal_published")
}

func readOwner(owner *lease) ([]byte, error) {
	info, err := owner.file.Stat()
	if err != nil || info.Size() > 256 {
		return nil, Error{Corrupt}
	}
	body := make([]byte, info.Size())
	if _, err = owner.file.ReadAt(body, 0); err != nil {
		return nil, Error{Corrupt}
	}
	return body, nil
}

func ownerIdentity(id, instance authority.UUID, directory, physical string) string {
	return string(id) + "\n" + hash([]byte(directory)) + "\n" + string(instance) + "\n" + physical + "\n"
}

func finishInitialization(ctx context.Context, r *Registry, owner *lease, path string, m authority.Manifest, p pendingInitialization, inject func(string) error) error {
	stageDir := filepath.Join(path, stageName(p.Instance))
	stage := filepath.Join(stageDir, "tracker.db")
	final := filepath.Join(path, "tracker.db")
	stagePhysical, err := initialDirectory(stageDir)
	if err != nil || stagePhysical != p.StagePhysicalHash {
		return Error{Corrupt}
	}
	if _, err := os.Lstat(final); os.IsNotExist(err) {
		physical, content, err := initialFile(stage)
		if err != nil || physical != p.PhysicalHash || content != p.ContentHash {
			return Error{Corrupt}
		}
		if os.Link(stage, final) != nil {
			return authority.Error{Code: authority.Conflict}
		}
		if err = inject("init_final_linked"); err != nil {
			return err
		}
		if err = syncDirectory(path); err != nil {
			return err
		}
	} else if err != nil {
		return Error{Corrupt}
	}
	physical, content, err := initialFile(final)
	if err != nil || physical != p.PhysicalHash || content != p.ContentHash {
		return Error{Corrupt}
	}
	if _, err := os.Lstat(stage); err == nil {
		a, b, e := initialFile(stage)
		if e != nil || a != physical || b != content {
			return Error{Corrupt}
		}
		if os.Remove(stage) != nil {
			return Error{DurabilityUnavailable}
		}
		if err = syncDirectory(stageDir); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return Error{Corrupt}
	}
	if err = syncDirectory(path); err != nil {
		return err
	}
	if err = inject("init_final_preserved"); err != nil {
		return err
	}
	verified, err := OpenReadOnly(ctx, path, m.ID)
	if err != nil {
		return err
	}
	if verified.instance != p.Instance || manifestHash(verified.manifest) != p.ManifestHash {
		_ = verified.Close()
		return Error{Corrupt}
	}
	if err = verified.Close(); err != nil {
		return err
	}
	want := ownerIdentity(m.ID, p.Instance, path, physical)
	body, err := readOwner(owner)
	if err != nil {
		return err
	}
	// A torn unacknowledged binding is recoverable only as an exact prefix
	// of this independently verified pending store, never a changed identity.
	if !strings.HasPrefix(want, string(body)) {
		return authority.Error{Code: authority.Conflict}
	}
	if _, err = owner.file.WriteAt([]byte(want), 0); err != nil {
		return Error{DurabilityUnavailable}
	}
	if err = inject("init_binding_written"); err != nil {
		return err
	}
	if err = syncFile(owner.file); err != nil {
		return err
	}
	if err = syncDirectory(r.root); err != nil {
		return err
	}
	if err = inject("init_binding_published"); err != nil {
		return err
	}
	loaded, exists, err := readPending(r, m.ID)
	if err != nil || !exists || loaded != p {
		return Error{Corrupt}
	}
	if os.Remove(initPath(r, m.ID)) != nil {
		return Error{DurabilityUnavailable}
	}
	if err = syncDirectory(r.root); err != nil {
		return err
	}
	// Empty staging directories are metadata only. Unknown extra files stay.
	_ = os.Remove(stageDir)
	if err = syncDirectory(path); err != nil {
		return err
	}
	return inject("init_pending_cleared")
}

func openInitialized(ctx context.Context, owner *lease, path string, m authority.Manifest) (*Store, error) {
	s, err := OpenReadOnly(ctx, path, m.ID)
	if err != nil {
		return nil, authority.Error{Code: authority.Conflict}
	}
	if manifestHash(s.manifest) != manifestHash(m) {
		_ = s.Close()
		return nil, authority.Error{Code: authority.Conflict}
	}
	f, err := openExistingFile(filepath.Join(path, "tracker.db"))
	if err != nil {
		_ = s.Close()
		return nil, err
	}
	physical, err := physicalIdentity(f)
	_ = f.Close()
	body, e := readOwner(owner)
	if err != nil || e != nil || string(body) != ownerIdentity(m.ID, s.instance, path, physical) {
		_ = s.Close()
		return nil, authority.Error{Code: authority.Conflict}
	}
	_ = s.db.Close()
	s.db, err = openDB(ctx, filepath.Join(path, "tracker.db"), false)
	if err == nil {
		err = verifyWriter(ctx, s.db)
	}
	if err == nil {
		err = s.verifyIdentity(ctx, m.ID)
	}
	if err == nil {
		err = s.publishBarrier()
	}
	if err != nil {
		_ = s.Close()
		return nil, err
	}
	s.lease = owner
	return s, nil
}
