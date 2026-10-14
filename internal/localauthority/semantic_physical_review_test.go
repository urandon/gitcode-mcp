//go:build darwin || linux

package localauthority

import (
	"context"
	"os"
	"testing"
)

func TestReviewSemanticPhysicalInitialReplacementFencesQuota(t *testing.T) {
	q, scope, expected := semanticPhysicalFixture(t)
	image, err := scanSemanticPhysical(context.Background(), scope, expected)
	if err != nil || q.observe(scope, image) != nil {
		t.Fatal("initial physical qualification unavailable")
	}
	original := scope.store.directory
	saved := original + ".review-saved"
	if os.Rename(original, saved) != nil || os.Mkdir(original, 0700) != nil {
		t.Fatal("physical replacement control unavailable")
	}
	defer func() {
		if os.Remove(original) != nil || os.Rename(saved, original) != nil {
			t.Fatal("physical replacement cleanup failed")
		}
	}()
	bad, err := scanSemanticPhysical(context.Background(), scope, expected)
	if err == nil || bad.valid() {
		t.Fatal("replaced source root qualified")
	}
	r, admissionErr := q.reserve(scope, semanticUsage{})
	if admissionErr == nil {
		r.joinedUncertain()
		t.Fatal("observed physical replacement did not fence fresh quota admission")
	}
	if !q.uncertain.Load() {
		t.Fatal("observed physical replacement lost semantic uncertainty")
	}
}
