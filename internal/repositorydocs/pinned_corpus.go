package repositorydocs

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"sort"
	"strings"
)

// PinnedFile is transient input from an already validated local Git snapshot.
// Existing indexing persists its locators/digests/vectors, never these bytes.
type PinnedFile struct {
	Entry TreeEntry
	Data  []byte
}

type pinnedCorpus struct {
	commit string
	files  []TreeEntry
	blobs  map[string][]byte
}

// NewPinnedProjectRepository adapts an exact, bounded LocalGit corpus to the
// existing retriever/indexer. It cannot execute Git or discover a filesystem.
func NewPinnedProjectRepository(storeIdentity, commit string, files []PinnedFile) (*Repository, error) {
	format := "sha1"
	if len(commit) == 64 {
		format = "sha256"
	}
	if storeIdentity == "" || !validOID(commit, format) || len(files) > 10000 {
		return nil, ErrMalformedGitTree
	}
	p := &pinnedCorpus{commit: commit, blobs: map[string][]byte{}}
	seen := map[string]bool{}
	total := 0
	for _, file := range files {
		e := file.Entry
		total += len(file.Data)
		if total > 8<<20 || len(file.Data) > int(DefaultMaxDocumentBytes) || e.Size != int64(len(file.Data)) || e.Mode != "100644" || e.Type != "blob" || !validOID(e.OID, format) || seen[e.Path] || e.Path != path.Clean(e.Path) || strings.Contains(e.Path, "\\") || !projectCorpusPolicy().Matches(e.Path) || ClassifyDocumentContent(e, file.Data) != DocumentContentRegular {
			return nil, ErrMalformedGitTree
		}
		// Do not allow synthetic bytes to acquire an exact Git blob citation.
		payload := append([]byte(fmt.Sprintf("blob %d\x00", len(file.Data))), file.Data...)
		var digest string
		if format == "sha256" {
			h := sha256.Sum256(payload)
			digest = hex.EncodeToString(h[:])
		} else {
			h := sha1.Sum(payload)
			digest = hex.EncodeToString(h[:])
		}
		if digest != e.OID {
			return nil, ErrMalformedGitTree
		}
		seen[e.Path] = true
		p.files = append(p.files, e)
		p.blobs[e.OID] = bytes.Clone(file.Data)
	}
	sort.Slice(p.files, func(i, j int) bool { return p.files[i].Path < p.files[j].Path })
	return &Repository{GitStoreRef: opaqueRef("git-store", storeIdentity+"\x00"+format), ObjectFormat: format, pinned: p}, nil
}

func projectCorpusPolicy() Policy {
	return Policy{Schema: PolicySchemaV1, Enabled: true, Preset: "none", Include: []string{"issues/**", "wiki/**"}, Exclude: []string{".harness/**"}}
}

func (p *pinnedCorpus) resolve(ctx context.Context, revision string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if revision != "" && revision != "HEAD" && revision != p.commit {
		return "", ErrGitObjectUnavailable
	}
	return p.commit, nil
}

func (p *pinnedCorpus) walk(ctx context.Context, commit string, visit func(TreeEntry) error) error {
	if commit != p.commit || visit == nil {
		return ErrGitObjectUnavailable
	}
	for _, e := range p.files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := visit(e); err != nil {
			return err
		}
	}
	return nil
}

func (p *pinnedCorpus) file(ctx context.Context, commit, path string, maxBytes int64) ([]byte, bool, error) {
	if commit != p.commit {
		return nil, false, ErrGitObjectUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	for _, e := range p.files {
		if e.Path == path {
			data := p.blobs[e.OID]
			if maxBytes > 0 && int64(len(data)) > maxBytes {
				return nil, true, ErrDocumentTooLarge
			}
			return bytes.Clone(data), true, nil
		}
	}
	return nil, false, nil
}

func pinnedProjectPolicy() PolicyResolution {
	return resolvedPolicy(projectCorpusPolicy(), "local_project_v1", []byte(strings.Join([]string{"issues/**", "wiki/**", "active-only-v1"}, "\n")))
}
