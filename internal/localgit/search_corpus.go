package localgit

import (
	"context"
	"sort"

	"gitcode-mcp/internal/repositorydocs"
)

// SearchCorpus hydrates one validated snapshot into bounded request-local
// memory for the existing search/index engine. No content is persisted here.
func (s *Snapshot) SearchCorpus(ctx context.Context) (*repositorydocs.Repository, map[string]Citation, error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	paths := make([]string, 0, len(s.entities))
	for _, path := range s.entities {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	files := make([]repositorydocs.PinnedFile, 0, len(paths))
	citations := map[string]Citation{}
	total := int64(0)
	for _, path := range paths {
		total += s.entries[path].Size
		if total > int64(s.store.limits.ReadBytes) {
			return nil, nil, fail("limit_exceeded", "read_bytes")
		}
		ref, _, _ := parseEntityPath(path)
		data, entry, err := s.file(ctx, path, s.store.limits.FileBytes)
		if err != nil {
			return nil, nil, err
		}
		entity, err := s.entityFromFile(path, ref.Kind, ref.ID, data, entry)
		if err != nil {
			return nil, nil, err
		}
		if entity.Metadata.State == "deleted" {
			continue
		}
		files = append(files, repositorydocs.PinnedFile{Entry: entry, Data: data})
		citations[path] = entity.Citation
	}
	repo, err := repositorydocs.NewPinnedProjectRepository(s.store.dir, s.commit, files)
	return repo, citations, err
}
