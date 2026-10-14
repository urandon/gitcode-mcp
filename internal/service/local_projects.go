package service

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"sync/atomic"
	"time"

	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/localgit"
)

type localProjectStore struct {
	store          *localgit.Store
	projectID, ref string
	conflict       atomic.Pointer[string] // last outcome, never durable authority.
}
type localProjectBinding struct {
	runtime  *localProjectStore
	readOnly bool
	profile  string
}

// LocalProjects is independent of the remote Service constructor, cache,
// credential pipeline and provider graph. Git is its only durable authority.
type LocalProjects struct {
	bindings     map[string]localProjectBinding
	searchConfig config.Config // selected once; accessed only by explicit search/index.
}

func localProjectError(code, field string) error { return &localgit.Error{Code: code, Field: field} }

func NewLocalProjects(ctx context.Context, cfg map[string]config.LocalProjectConfig, resolve func(string) (string, error)) (*LocalProjects, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := config.ValidateLocalProjects(config.ServiceModeLocal, cfg); err != nil {
		return nil, err
	}
	if resolve == nil {
		return nil, config.LocalProjectConfigError{}
	}
	host := &LocalProjects{bindings: map[string]localProjectBinding{}}
	stores := map[string]*localProjectStore{}
	ids := map[string]string{}
	for selector, p := range cfg {
		path, err := resolve(p.StorageRef)
		if err != nil {
			return nil, config.LocalProjectConfigError{}
		}
		canonical, err := filepath.EvalSymlinks(path)
		if err != nil {
			return nil, localProjectError("unavailable", "store")
		}
		if previous, ok := ids[p.ProjectID]; ok && previous != canonical {
			return nil, config.LocalProjectConfigError{}
		}
		ids[p.ProjectID] = canonical
		runtime := stores[canonical]
		if runtime != nil && (runtime.projectID != p.ProjectID || runtime.ref != p.Ref) {
			return nil, config.LocalProjectConfigError{}
		}
		if runtime == nil {
			store, err := localgit.Open(ctx, canonical, localgit.Limits{})
			if err != nil {
				return nil, err
			}
			if err := store.ValidateLocalBranch(ctx, p.Ref); err != nil {
				return nil, err
			}
			snapshot, err := store.Snapshot(ctx, p.Ref)
			if err != nil {
				return nil, err
			}
			if snapshot.Project().ID != p.ProjectID {
				return nil, config.LocalProjectConfigError{}
			}
			runtime = &localProjectStore{store: store, projectID: p.ProjectID, ref: p.Ref}
			stores[canonical] = runtime
		}
		host.bindings[selector] = localProjectBinding{runtime: runtime, readOnly: p.ReadOnly, profile: p.RAGProfile}
	}
	return host, nil
}

type LocalProjectQuery struct {
	Project    string        `json:"project"`
	Kind       localgit.Kind `json:"kind,omitempty"`
	ID         string        `json:"id,omitempty"`
	Limit      int           `json:"limit,omitempty"`
	Offset     int           `json:"offset,omitempty"`
	MaxCommits int           `json:"max_commits,omitempty"`
}
type LocalProjectMutation struct {
	Project  string            `json:"project"`
	Mutation localgit.Mutation `json:"mutation"`
}
type LocalProjectStatus struct {
	Project        string `json:"project"`
	ProjectID      string `json:"project_id"`
	Readable       bool   `json:"readable"`
	ReadOnly       bool   `json:"read_only"`
	State          string `json:"state"`
	Commit         string `json:"commit,omitempty"`
	DiagnosticCode string `json:"diagnostic_code,omitempty"`
	LastConflict   string `json:"last_conflict,omitempty"`
}
type LocalProjectsStatus struct {
	Projects []LocalProjectStatus `json:"projects"`
}

func (s *LocalProjects) binding(project string) (localProjectBinding, error) {
	b, ok := s.bindings[project]
	if !ok {
		return b, localProjectError("unknown_project", "project")
	}
	return b, nil
}
func projectSnapshot(ctx context.Context, b localProjectBinding) (*localgit.Snapshot, error) {
	snap, err := b.runtime.store.Snapshot(ctx, b.runtime.ref)
	if err != nil {
		return nil, err
	}
	if snap.Project().ID != b.runtime.projectID {
		return nil, config.LocalProjectConfigError{}
	}
	return snap, nil
}
func (s *LocalProjects) Status(ctx context.Context) (LocalProjectsStatus, error) {
	result := LocalProjectsStatus{Projects: []LocalProjectStatus{}}
	names := make([]string, 0, len(s.bindings))
	for name := range s.bindings {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		b := s.bindings[name]
		p := LocalProjectStatus{Project: name, ProjectID: b.runtime.projectID, ReadOnly: b.readOnly, State: "ready"}
		if b.readOnly {
			p.State = "read_only"
		}
		snap, err := projectSnapshot(ctx, b)
		if err == nil {
			p.Readable = true
			p.Commit = snap.Commit()
		} else {
			p.State = "unavailable"
			p.DiagnosticCode = "local_git_unavailable"
			var coded interface{ DiagnosticCode() string }
			if errors.As(err, &coded) {
				p.DiagnosticCode = coded.DiagnosticCode()
			}
		}
		if c := b.runtime.conflict.Load(); c != nil {
			p.LastConflict = *c
		}
		result.Projects = append(result.Projects, p)
	}
	return result, nil
}
func (s *LocalProjects) Get(ctx context.Context, q LocalProjectQuery) (localgit.Entity, error) {
	b, err := s.binding(q.Project)
	if err != nil {
		return localgit.Entity{}, err
	}
	snap, err := projectSnapshot(ctx, b)
	if err != nil {
		return localgit.Entity{}, err
	}
	return snap.Get(ctx, q.Kind, q.ID)
}
func (s *LocalProjects) List(ctx context.Context, q LocalProjectQuery) ([]localgit.Entity, error) {
	b, err := s.binding(q.Project)
	if err != nil {
		return nil, err
	}
	snap, err := projectSnapshot(ctx, b)
	if err != nil {
		return nil, err
	}
	return snap.List(ctx, q.Kind, q.Offset, q.Limit)
}
func (s *LocalProjects) History(ctx context.Context, q LocalProjectQuery) ([]localgit.Citation, error) {
	b, err := s.binding(q.Project)
	if err != nil {
		return nil, err
	}
	snap, err := projectSnapshot(ctx, b)
	if err != nil {
		return nil, err
	}
	return snap.History(ctx, q.Kind, q.ID, q.MaxCommits)
}
func (s *LocalProjects) Mutate(ctx context.Context, q LocalProjectMutation) (localgit.MutationResult, error) {
	b, err := s.binding(q.Project)
	if err != nil {
		return localgit.MutationResult{}, err
	}
	if b.readOnly {
		return localgit.MutationResult{}, localProjectError("read_only", "project")
	}
	// Recheck configured project identity after external ref changes. No remote
	// fallback or automatic adoption of a different project is possible.
	if _, err := projectSnapshot(ctx, b); err != nil {
		return localgit.MutationResult{}, err
	}
	r, err := b.runtime.store.MutateProject(ctx, b.runtime.ref, b.runtime.projectID, q.Mutation)
	var e *localgit.Error
	if errors.As(err, &e) && (e.Code == "write_conflict" || e.Code == "writer_busy" || e.Code == "idempotency_conflict") {
		code := e.DiagnosticCode()
		b.runtime.conflict.Store(&code)
	}
	if err == nil {
		b.runtime.conflict.Store(nil)
	}
	return r, err
}
