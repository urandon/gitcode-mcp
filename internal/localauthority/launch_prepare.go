package localauthority

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"gitcode-mcp/internal/authority"
)

const launchFrameLimit = 64 << 10
const strictLaunchProfile = "deny_ipc_only"

// All coordinates below are transient authenticated bootstrap data. They are
// never public configuration, IPC operation selectors or diagnostic fields.
type launchProject struct {
	ProjectID authority.UUID `json:"project_id"`
	Directory string         `json:"directory"`
	Instance  authority.UUID `json:"instance"`
	Physical  string         `json:"physical"`
	Database  string         `json:"database"`
	Manifest  string         `json:"manifest"`
}

type launchFrame struct {
	Schema      int             `json:"schema"`
	Protocol    string          `json:"protocol"`
	Profile     string          `json:"profile"`
	Generation  authority.UUID  `json:"generation"`
	Challenge   authority.UUID  `json:"challenge"`
	Root        string          `json:"root"`
	Identity    string          `json:"identity"`
	Projects    []launchProject `json:"projects"`
	Observation string          `json:"observation,omitempty"`
}

func (f *launchFrame) Validate() error {
	if f.Observation != "" && f.Observation != nativeObservationProtocol {
		return authority.Error{Code: authority.InvalidContract}
	}
	if f.Schema != 1 || f.Protocol != authority.Protocol || f.Profile != strictLaunchProfile || !f.Generation.Valid() || !f.Challenge.Valid() || !validDigest(f.Identity) || !launchPath(f.Root) || len(f.Projects) < 1 || len(f.Projects) > CoordinatorProjectLimit {
		return authority.Error{Code: authority.InvalidContract}
	}
	ids, directories := map[authority.UUID]bool{}, map[string]bool{}
	for _, p := range f.Projects {
		if !p.ProjectID.Valid() || !p.Instance.Valid() || !launchPath(p.Directory) || !validDigest(p.Physical) || !validDigest(p.Database) || !validDigest(p.Manifest) || ids[p.ProjectID] || directories[p.Directory] {
			return authority.Error{Code: authority.InvalidContract}
		}
		ids[p.ProjectID], directories[p.Directory] = true, true
	}
	return nil
}

func launchPath(path string) bool {
	return len(path) > 0 && len(path) <= 4096 && filepath.IsAbs(path) && filepath.Clean(path) == path
}

// NativePreparation is single-use trusted launch input. Only an existing
// opaque PreparedRegistry can create it; there is no root/config constructor.
// Duplicated descriptors have separate lifetime ownership, not another writer.
type NativePreparation struct {
	mu               sync.Mutex
	file             *os.File
	frame            launchFrame
	used             bool
	closed           bool
	activatedBinding string
}

func (p *PreparedRegistry) PrepareLaunch(ctx context.Context, bindings []ProjectBinding) (*NativePreparation, error) {
	if p == nil {
		return nil, authority.Error{Code: authority.InitializationRequired}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if !p.mu.TryLock() {
		return nil, authority.Error{Code: authority.Conflict}
	}
	defer p.mu.Unlock()
	if p.closed || p.registry == nil || p.directory == nil || p.registry.runtime != nil {
		return nil, authority.Error{Code: authority.InitializationRequired}
	}
	if len(bindings) < 1 || len(bindings) > CoordinatorProjectLimit {
		return nil, authority.Error{Code: authority.InvalidContract}
	}
	ctx, cancel := context.WithTimeout(ctx, CoordinatorCallBudget)
	defer cancel()
	if err := (runtimeRegistry{p.directory, p.identity}).verify(p.registry.root); err != nil {
		return nil, err
	}
	generation, err := authority.NewUUID()
	if err != nil {
		return nil, authority.Error{Code: authority.Unavailable}
	}
	f := launchFrame{Schema: 1, Protocol: authority.Protocol, Profile: strictLaunchProfile, Generation: generation, Root: p.registry.root, Identity: p.identity}
	ids, directories := map[authority.UUID]bool{}, map[string]bool{}
	for _, b := range bindings {
		if !b.ProjectID.Valid() || ids[b.ProjectID] || directories[b.Directory] || !launchPath(b.Directory) {
			return nil, authority.Error{Code: authority.InvalidContract}
		}
		ids[b.ProjectID], directories[b.Directory] = true, true
		project, err := inspectLaunchProject(ctx, p.directory, b)
		if err != nil {
			return nil, err
		}
		f.Projects = append(f.Projects, project)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	file, err := duplicateLaunchDirectory(p.directory)
	if err != nil {
		return nil, err
	}
	return &NativePreparation{file: file, frame: f}, nil
}

func inspectLaunchProject(ctx context.Context, registry *os.File, b ProjectBinding) (launchProject, error) {
	var zero launchProject
	s, err := OpenReadOnly(ctx, b.Directory, b.ProjectID)
	if err != nil {
		return zero, err
	}
	defer s.Close()
	if s.manifest.Network.DataEgress != "deny" || s.manifest.Network.ControlTransport != "ipc_only" {
		return zero, authority.Error{Code: authority.PolicyDenied}
	}
	dir, err := openPrivateDirectory(s.directory)
	if err != nil {
		return zero, err
	}
	physical, err := physicalIdentity(dir)
	_ = dir.Close()
	if err != nil {
		return zero, err
	}
	database, err := openExistingFile(filepath.Join(s.directory, "tracker.db"))
	if err != nil {
		return zero, err
	}
	dbIdentity, err := physicalIdentity(database)
	_ = database.Close()
	if err != nil {
		return zero, err
	}
	owner, err := openExistingOwnerAt(registry, string(b.ProjectID)+".owner")
	if err != nil {
		return zero, err
	}
	body, err := io.ReadAll(io.LimitReader(owner, 257))
	_ = owner.Close()
	if err != nil || string(body) != ownerIdentity(b.ProjectID, s.instance, s.directory, dbIdentity) {
		return zero, authority.Error{Code: authority.Conflict}
	}
	return launchProject{b.ProjectID, s.directory, s.instance, physical, dbIdentity, manifestHash(s.manifest)}, nil
}

// Descriptor duplicates the retained private registry role for ExtraFiles.
// It never resolves a home/account/root. The receiver owns its inherited copy.
func (p *NativePreparation) Descriptor() (*os.File, error) {
	if p == nil {
		return nil, authority.Error{Code: authority.InitializationRequired}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.used || p.file == nil {
		return nil, authority.Error{Code: authority.InitializationRequired}
	}
	return duplicateLaunchDirectory(p.file)
}

func (p *NativePreparation) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	if p.file != nil && p.file.Close() != nil {
		return Error{DurabilityUnavailable}
	}
	return nil
}

// Send waits for validated writer readiness, then explicitly activates it.
// Failed or cancelled handshake burns this preparation; retry needs a fresh
// preparation/challenge and an actual joined child, never another writer key.
func (p *NativePreparation) Send(ctx context.Context, conn *net.UnixConn) error {
	_, err := p.send(ctx, conn, false)
	return err
}

// SendObserved selects retained delivery only from an already bound concrete
// parent collector. Absence preserves unobserved launch; requested delivery is
// included in the authenticated binding and cannot silently downgrade.
func (p *NativePreparation) SendObserved(ctx context.Context, conn *net.UnixConn) (*NativeObservationReader, error) {
	return p.send(ctx, conn, true)
}

// ActivatedRuntimeBinding is available only after successful trusted handshake
// cleanup. Never learn an expected owner epoch from a health response.
func (p *NativePreparation) ActivatedRuntimeBinding() (string, error) {
	if p == nil {
		return "", authority.Error{Code: authority.InitializationRequired}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !validDigest(p.activatedBinding) {
		return "", authority.Error{Code: authority.InitializationRequired}
	}
	return p.activatedBinding, nil
}

func (p *NativePreparation) send(ctx context.Context, conn *net.UnixConn, observe bool) (_ *NativeObservationReader, resultErr error) {
	if p == nil {
		return nil, authority.Error{Code: authority.InitializationRequired}
	}
	p.mu.Lock()
	if p.used || p.closed || p.file == nil {
		p.mu.Unlock()
		return nil, authority.Error{Code: authority.InitializationRequired}
	}
	p.used = true
	f := p.frame
	p.mu.Unlock()
	var observation *NativeObservationReader
	binding := ""
	handoff := false
	if observe {
		observation = newNativeObservationReader(ctx, conn, f.Projects)
		if observation != nil {
			f.Observation = nativeObservationProtocol
		}
	}
	ctx, release, err := launchHandshake(ctx, conn)
	if err != nil {
		return nil, err
	}
	defer func() {
		release()
		// Reset only after the startup cancellation callback has joined. The
		// process owner can subsequently impose a bounded post-exit drain.
		if resultErr == nil && observation != nil && conn.SetDeadline(time.Time{}) != nil {
			resultErr = authority.Error{Code: authority.Unavailable}
		}
		if resultErr != nil && observation != nil && handoff {
			observation.lossOwner.UnconfirmedCommit()
		}
		if resultErr == nil && validDigest(binding) {
			p.mu.Lock()
			p.activatedBinding = binding
			p.mu.Unlock()
		}
	}()
	var challenge launchMessage
	if err = readLaunch(conn, &challenge); err != nil || challenge.Phase != "challenge" || challenge.Generation != "" || challenge.Binding != "" {
		return nil, authority.Error{Code: authority.InvalidContract}
	}
	f.Challenge = challenge.Challenge
	if f.Validate() != nil {
		return nil, authority.Error{Code: authority.InvalidContract}
	}
	handoff = true // an interrupted full-frame write can leave child admission unknown
	if writeLaunch(conn, f) != nil {
		return nil, authority.Error{Code: authority.InvalidContract}
	}
	binding = launchBinding(f)
	if observation != nil {
		observation.binding = binding
	}
	if err = expectLaunch(conn, f, binding, "ready"); err != nil {
		return nil, err
	}
	if err = writeLaunch(conn, launchReply(f, binding, "activate")); err != nil {
		return nil, err
	}
	if err = expectLaunch(conn, f, binding, "active"); err != nil {
		return nil, err
	}
	return observation, ctx.Err()
}

func launchBinding(f launchFrame) string { body, _ := json.Marshal(f); return hash(body) }
