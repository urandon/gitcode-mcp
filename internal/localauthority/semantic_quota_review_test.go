package localauthority

import "testing"

func TestReviewSemanticQuotaRejectsAdmissionAfterWriterClose(t *testing.T) {
	q, s, _ := semanticQuotaFixture(t, semanticUsage{})
	if s.store.Close() != nil {
		t.Fatal("actual prepared writer close failed")
	}
	if _, err := q.reserve(s, semanticUsage{1, 1}); err == nil {
		t.Error("closed prepared writer still admits a semantic growth envelope")
	}
	if lease, err := q.acquireIO(s); err == nil {
		t.Error("closed prepared writer still grants a semantic I/O capability")
		_ = lease.release()
	}
}

func TestReviewSemanticQuotaCannotReleaseWriterBeforeJoinedIO(t *testing.T) {
	q, s, _ := semanticQuotaFixture(t, semanticUsage{})
	lease, err := q.acquireIO(s)
	if err != nil {
		t.Fatal("owned I/O fixture unavailable")
	}
	defer lease.release()
	if s.store.Close() == nil {
		t.Fatal("prepared writer lease released while semantic I/O remains unjoined")
	}
}
