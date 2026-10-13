package localauthority

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"

	"gitcode-mcp/internal/authority"
)

func indexJobModelFixture(t *testing.T) indexJobRecord {
	t.Helper()
	job, err := authority.NewUUID()
	if err != nil {
		t.Fatal("synthetic index identity allocation failed")
	}
	epoch, err := authority.NewUUID()
	if err != nil {
		t.Fatal("synthetic index epoch allocation failed")
	}
	r := indexJobRecord{Selection: indexSelection{ProjectID: testProject, TrackerInstance: job, ManifestHash: hash([]byte("synthetic manifest")), Namespace: projectionNamespace, Epoch: epoch, Generation: 1, BuildID: job}, KeyHash: hash([]byte("synthetic caller key")), IntentHash: hash([]byte("synthetic intent")), ActorHash: hash([]byte("synthetic actor")), Generation: 1, AdmittedGeneration: 2, State: "queued"}
	if !r.valid() {
		t.Fatal("synthetic index lifecycle fixture invalid")
	}
	return r
}

func indexJobModelPublication(t *testing.T, r indexJobRecord) indexPublication {
	t.Helper()
	s := r.Selection
	c := projectionCatalog{Schema: 1, ProjectID: s.ProjectID, TrackerInstance: s.TrackerInstance, ManifestHash: s.ManifestHash, Namespace: s.Namespace, Epoch: s.Epoch, Generation: s.Generation, BuildID: s.BuildID, Sequence: s.Sequence, Entries: []projectionEntry{}}
	state, _ := json.Marshal(c)
	body, _ := json.Marshal(projectionEnvelope{Catalog: c, Digest: hash(state)})
	p, err := indexPublicationProof(s, c, hash(body))
	if err != nil {
		t.Fatal("synthetic exact catalog proof failed")
	}
	return p
}

func TestIndexJobModelCancellationRetryAndLatePublicationProof(t *testing.T) {
	queued := indexJobModelFixture(t)
	running := queued
	running.Generation, running.State, running.Attempts = 2, "running", 1
	if !indexJobTransition(queued, running) {
		t.Fatal("authorized first attempt refused")
	}
	cancelling := running
	cancelling.Generation, cancelling.State = 3, "cancelling"
	cancelling.CancellationRequested, cancelling.CancelFrom = true, 2
	cancelling.ActionActorHash = hash([]byte("synthetic action actor"))
	if !indexJobTransition(running, cancelling) {
		t.Fatal("generation-fenced cancellation refused")
	}
	cancelled := cancelling
	cancelled.Generation, cancelled.State, cancelled.Code = 4, "cancelled", "cancelled"
	if !indexJobTransition(cancelling, cancelled) {
		t.Fatal("joined cancellation terminal refused")
	}
	retry := cancelled
	retry.Generation, retry.State, retry.Code = 5, "queued", ""
	retry.RetryFrom, retry.CancellationRequested = 4, false
	if !indexJobTransition(cancelled, retry) || retry.CancelFrom != cancelling.CancelFrom {
		t.Fatal("explicit retry erased cancellation evidence")
	}
	resumed := retry
	resumed.Generation, resumed.State, resumed.Attempts = 6, "running", 2
	if !indexJobTransition(retry, resumed) {
		t.Fatal("explicit bounded retry refused")
	}
	succeeded := cancelled
	succeeded.Generation, succeeded.State, succeeded.Code = 5, "succeeded", ""
	proof := indexJobModelPublication(t, succeeded)
	succeeded.Publication = &proof
	if !indexJobTransition(cancelled, succeeded) || !succeeded.CancellationRequested {
		t.Fatal("exact late publication reconciliation erased cancellation or invented retry")
	}
	clone := cloneIndexJob(succeeded)
	clone.Publication.Digest = hash([]byte("different bytes"))
	if reflect.DeepEqual(clone, succeeded) || succeeded.Publication.Digest == clone.Publication.Digest {
		t.Fatal("protected index proof shares mutable memory")
	}
	bad := succeeded
	bad.Generation++
	bad.State, bad.Publication, bad.Code = "interrupted", nil, "interrupted"
	if indexJobTransition(succeeded, bad) {
		t.Fatal("terminal proved publication was rewritten")
	}
}

func TestIndexJobModelRejectsRetargetUnprovedSuccessAndFabricatedAttempt(t *testing.T) {
	before := indexJobModelFixture(t)
	next := before
	next.Generation, next.State, next.Attempts = 2, "running", 1
	for _, mutate := range []func(*indexJobRecord){
		func(r *indexJobRecord) { r.Selection.Sequence++ },
		func(r *indexJobRecord) { r.IntentHash = hash([]byte("changed intent")) },
		func(r *indexJobRecord) { r.KeyHash = hash([]byte("changed caller identity")) },
		func(r *indexJobRecord) { r.ActorHash = hash([]byte("changed origin actor")) },
		func(r *indexJobRecord) { r.AdmittedGeneration++ },
		func(r *indexJobRecord) { r.Attempts++ },
		func(r *indexJobRecord) { r.Generation++ },
		func(r *indexJobRecord) { r.State, r.Publication = "succeeded", nil },
		func(r *indexJobRecord) { r.Code = "arbitrary diagnostic text" },
	} {
		bad := cloneIndexJob(next)
		mutate(&bad)
		if indexJobTransition(before, bad) {
			t.Fatal("index lifecycle accepted retarget or unproved transition")
		}
	}
	max := before
	max.Generation, max.State, max.Attempts, max.Code = math.MaxInt64, "interrupted", 8, "interrupted"
	if !max.valid() {
		t.Fatal("bounded terminal control invalid")
	}
	bad := max
	bad.Generation = 1
	if indexJobTransition(max, bad) {
		t.Fatal("index generation wrapped")
	}
}

func TestIndexJobSnapshotConservesIdentityAndRefusesDuplicateKey(t *testing.T) {
	r := indexJobModelFixture(t)
	empty := indexJobSnapshot{Schema: 1, ProjectID: r.Selection.ProjectID, TrackerInstance: r.Selection.TrackerInstance, ManifestHash: r.Selection.ManifestHash, Generation: 1, Jobs: []indexJobRecord{}}
	if !indexSnapshotTransition(indexJobSnapshot{}, empty) {
		t.Fatal("explicit initial index ledger rejected")
	}
	admitted := empty
	admitted.Generation, admitted.Jobs = 2, []indexJobRecord{r}
	if !indexSnapshotTransition(empty, admitted) {
		t.Fatal("first persisted index intent rejected")
	}
	discarded := admitted
	discarded.Generation, discarded.Jobs = 3, []indexJobRecord{}
	if indexSnapshotTransition(admitted, discarded) {
		t.Fatal("ledger silently discarded caller or cancellation evidence")
	}
	duplicate := admitted
	duplicate.Generation, duplicate.Jobs = 3, []indexJobRecord{r, r}
	if duplicate.valid() {
		t.Fatal("ledger accepted duplicate caller/build identities")
	}
	retarget := admitted
	retarget.Generation, retarget.ManifestHash = 3, hash([]byte("another manifest"))
	if indexSnapshotTransition(admitted, retarget) {
		t.Fatal("ledger changed authority manifest identity")
	}
	state, _ := json.Marshal(admitted)
	envelope := indexJobEnvelope{State: admitted, Digest: hash(state)}
	body, _ := json.Marshal(envelope)
	var decoded indexJobEnvelope
	if authority.DecodeJSON(body, &decoded) != nil || !reflect.DeepEqual(envelope, decoded) {
		t.Fatal("closed index ledger did not round trip")
	}
}

func TestIndexJobPublicationProofRequiresExactCatalogBytesAndIdentity(t *testing.T) {
	r := indexJobModelFixture(t)
	proof := indexJobModelPublication(t, r)
	s := r.Selection
	c := projectionCatalog{Schema: 1, ProjectID: s.ProjectID, TrackerInstance: s.TrackerInstance, ManifestHash: s.ManifestHash, Namespace: s.Namespace, Epoch: s.Epoch, Generation: s.Generation, BuildID: s.BuildID, Sequence: s.Sequence, Entries: []projectionEntry{}}
	for _, mutate := range []func(*projectionCatalog){
		func(c *projectionCatalog) { c.Sequence++ },
		func(c *projectionCatalog) { c.Generation++ },
		func(c *projectionCatalog) { c.ManifestHash = hash([]byte("other manifest")) },
		func(c *projectionCatalog) { c.BuildID = c.Epoch },
		func(c *projectionCatalog) { c.TrackerInstance = c.Epoch },
	} {
		bad := c
		mutate(&bad)
		if _, err := indexPublicationProof(s, bad, proof.Digest); err == nil {
			t.Fatal("unrelated catalog reconciled index success")
		}
	}
	if _, err := indexPublicationProof(s, c, hash([]byte("other bytes"))); err == nil {
		t.Fatal("arbitrary digest became exact publication evidence")
	}
}
