package repositorydocs

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRevisionSetIdentityGoldenAndPathFree(t *testing.T) {
	identity := RevisionSetIdentity{
		RepoID: "owner/repo", SourceRegistrationID: "source-registration-a", SourceRegistrationGeneration: 3,
		GitStoreRef: "git-store-a", ObjectFormat: "sha1", CommitOID: "1111111111111111111111111111111111111111",
		PolicyHash: "policy-a", ConfigDigest: "config-a", WorktreeRef: "worktree-a", OverlayDigest: "overlay-a",
		Processing: DefaultProcessingPolicy(), NamespaceID: "namespace-a",
	}
	const want = "repo-doc-set-b044a0ce3161123acbf5a91076ef7f5004d03ef881fc91eb90c6fad5a894200e"
	if got := identity.ID(); got != want {
		t.Fatalf("identity=%q, want golden %q", got, want)
	}
	data, err := json.Marshal(identity.normalized())
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"/Users/", "/Volumes/", "/private/", "repository_path", "cache_path"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("identity leaks local path field %q: %s", forbidden, data)
		}
	}
}

func TestRevisionSetIdentityChangesForEverySemanticInput(t *testing.T) {
	base := RevisionSetIdentity{
		RepoID:                       "owner/repo",
		SourceRegistrationID:         "registration-a",
		SourceRegistrationGeneration: 7,
		GitStoreRef:                  "git-store-a",
		ObjectFormat:                 "sha1",
		CommitOID:                    "1111111111111111111111111111111111111111",
		PolicyHash:                   "policy-a",
		ConfigDigest:                 "config-a",
		WorktreeRef:                  "worktree-a",
		OverlayDigest:                "overlay-a",
		Processing:                   ProcessingPolicy{ChunkerRevision: "chunker-a", ChunkBytes: 4096, NormalizationRevision: "normalization-a", MaxFileBytes: 1 << 20, HardExclusionRevision: "exclusions-a"},
		NamespaceID:                  "namespace-a",
	}
	want := base.ID()
	cases := map[string]func(*RevisionSetIdentity){
		"identity revision":              func(v *RevisionSetIdentity) { v.Revision = "repo-doc-revision-set-identity-v2" },
		"canonical repo id":              func(v *RevisionSetIdentity) { v.RepoID = "owner/other" },
		"opaque source registration":     func(v *RevisionSetIdentity) { v.SourceRegistrationID = "registration-b" },
		"source registration generation": func(v *RevisionSetIdentity) { v.SourceRegistrationGeneration++ },
		"opaque git store":               func(v *RevisionSetIdentity) { v.GitStoreRef = "git-store-b" },
		"object format":                  func(v *RevisionSetIdentity) { v.ObjectFormat = "sha256" },
		"resolved commit":                func(v *RevisionSetIdentity) { v.CommitOID = "2222222222222222222222222222222222222222" },
		"semantic policy":                func(v *RevisionSetIdentity) { v.PolicyHash = "policy-b" },
		"semantic config":                func(v *RevisionSetIdentity) { v.ConfigDigest = "config-b" },
		"worktree identity":              func(v *RevisionSetIdentity) { v.WorktreeRef = "worktree-b" },
		"overlay identity":               func(v *RevisionSetIdentity) { v.OverlayDigest = "overlay-b" },
		"processing revision":            func(v *RevisionSetIdentity) { v.Processing.Revision = "repo-doc-processing-policy-v2" },
		"chunker revision":               func(v *RevisionSetIdentity) { v.Processing.ChunkerRevision = "chunker-b" },
		"chunk bytes":                    func(v *RevisionSetIdentity) { v.Processing.ChunkBytes++ },
		"normalization revision":         func(v *RevisionSetIdentity) { v.Processing.NormalizationRevision = "normalization-b" },
		"max file guard":                 func(v *RevisionSetIdentity) { v.Processing.MaxFileBytes++ },
		"hard exclusion revision":        func(v *RevisionSetIdentity) { v.Processing.HardExclusionRevision = "exclusions-b" },
		"exact namespace":                func(v *RevisionSetIdentity) { v.NamespaceID = "namespace-b" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			got := base
			mutate(&got)
			if got.ID() == want {
				t.Fatalf("semantic change did not change identity %q", want)
			}
		})
	}
}

func TestChunkDocumentIDsUseActualProcessingPolicy(t *testing.T) {
	data := []byte("# Guide\n\nDeterministic repository documentation.\n")
	base := DefaultProcessingPolicy()
	first, err := ChunkDocumentWithPolicy("blob-a", data, base)
	if err != nil {
		t.Fatal(err)
	}
	changed := base
	changed.HardExclusionRevision = "repo-doc-hard-exclusions-v2"
	second, err := ChunkDocumentWithPolicy("blob-a", data, changed)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || len(second) != 1 || first[0].ID == second[0].ID {
		t.Fatalf("chunk ids did not reflect processing policy: first=%#v second=%#v", first, second)
	}
}
