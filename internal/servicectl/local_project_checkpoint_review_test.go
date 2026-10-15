package servicectl

import (
	"context"
	"database/sql"
	"gitcode-mcp/internal/localgit"
	"gitcode-mcp/internal/repositorydocs"
	"gitcode-mcp/internal/service"
	"strings"
	"testing"
)

func TestG5ReviewCheckpointReplayPlus128NewChunksHasValidResult(t *testing.T) {
	client, path, old := g5ReviewIndexedFixture(t)
	ctx := context.Background()
	var updated localgit.MutationResult
	q := service.LocalProjectMutation{Project: "lab", Mutation: localgit.Mutation{Operation: "update_issue", ID: old.Metadata.ID, Title: "Question", State: "open", Body: strings.Repeat("abcdefg ", (4096*130)/8), ExpectedRevision: old.Citation.Blob, WriteMode: "local", IdempotencyKey: "large"}}
	if client.Call(ctx, "LocalProjects.Mutate", q, &updated) != nil {
		t.Fatal("large valid Git fixture unavailable")
	}
	var first service.LocalProjectIndexResult
	if client.Call(ctx, "LocalProjects.Index", service.LocalProjectIndexRequest{Project: "lab", WriteMode: "local", MaxChunks: 1}, &first) != nil || first.State != "partial" || first.EmbeddedChunks != 1 || first.EligibleChunks < 129 {
		t.Fatal("bounded first index control failed")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal("checkpoint fixture unavailable")
	}
	var cp repositorydocs.VectorCheckpoint
	cp.RepoID = "local-project/lab"
	err = db.QueryRow(`SELECT v.namespace_id,v.chunk_id,v.vector,v.dimensions,v.dtype FROM repo_doc_vectors v JOIN repo_doc_membership m ON m.repo_id=v.repo_id AND m.chunk_id=v.chunk_id WHERE m.revision_set_id=? LIMIT 1`, first.RevisionSetID).Scan(&cp.NamespaceID, &cp.ChunkID, &cp.Vector, &cp.Dimensions, &cp.DType)
	if err != nil {
		db.Close()
		t.Fatal("genuine produced vector unavailable")
	}
	handoff, err := repositorydocs.NewFileVectorCheckpointStore(path + ".repository-doc-vector-checkpoints")
	if err != nil || handoff.Save(ctx, cp) != nil {
		db.Close()
		t.Fatal("existing vector-only handoff unavailable")
	}
	// Reconstruct the supported handoff cut: the exact produced vector is in
	// the checkpoint but its primary vector publication is not yet present.
	_, err = db.Exec("DELETE FROM repo_doc_vectors WHERE repo_id=? AND namespace_id=? AND chunk_id=?", cp.RepoID, cp.NamespaceID, cp.ChunkID)
	closeErr := db.Close()
	if err != nil || closeErr != nil {
		t.Fatal("checkpoint cut fixture failed")
	}
	var resumed service.LocalProjectIndexResult
	if client.Call(ctx, "LocalProjects.Index", service.LocalProjectIndexRequest{Project: "lab", WriteMode: "local", MaxChunks: 128}, &resumed) != nil {
		t.Fatal("legitimate checkpoint replay plus bounded new work was rejected by IPC")
	}
	if resumed.EmbeddedChunks+resumed.ReusedChunks < 129 {
		t.Fatal("retained checkpoint or bounded new work was lost")
	}
}
