package embedding

import (
	"context"
	"net/http"
	"testing"
)

func TestReviewEmptyFragmentCannotRetargetFixedRoute(t *testing.T) {
	p, err := Prepare(fixtureSpec("http://127.0.0.1:1#"))
	if err != nil {
		return
	}
	defer p.Close(context.Background())
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, p.spec.Endpoint+"/api/tags", nil)
	if err != nil {
		t.Fatal("accepted origin cannot construct fixed route")
	}
	if request.URL.EscapedPath() != "/api/tags" || request.URL.Fragment != "" {
		t.Fatal("accepted origin changes fixed request path into a fragment")
	}
}
