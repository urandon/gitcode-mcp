package gitcode

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

const bootstrapMetadataJSON = `{"id":17,"owner":{"login":"example-owner"},"name":"example-repo","full_name":"example-owner/example-repo","private":true,"default_branch":"main"}`

func bootstrapHTTPClient(t *testing.T, h http.HandlerFunc) *HTTPClient {
	t.Helper()
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)
	client, err := NewHTTPClient(Config{BaseURL: server.URL, Token: "fixture-token", MaxRetries: 3})
	if err != nil {
		t.Fatal(err)
	}
	return client
}
func bootstrapLabel(id int, name, color string) RepositoryLabel {
	return RepositoryLabel{ID: json.Number(strconv.Itoa(id)), Name: name, Color: color, RepositoryID: "17", Description: "Read-only description"}
}
func TestIssue157RepositoryIdentityFailsClosed(t *testing.T) {
	for _, change := range []func(map[string]any){nil, func(m map[string]any) { m["private"] = false }, func(m map[string]any) { delete(m, "private") }, func(m map[string]any) { m["private"] = nil }, func(m map[string]any) { m["id"] = 0 }, func(m map[string]any) { m["owner"] = map[string]any{"login": "wrong"} }, func(m map[string]any) { m["default_branch"] = "" }, func(m map[string]any) { m["name"] = "wrong" }, func(m map[string]any) { m["full_name"] = "wrong" }} {
		var raw map[string]any
		_ = json.Unmarshal([]byte(bootstrapMetadataJSON), &raw)
		if change != nil {
			change(raw)
		}
		client := bootstrapHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "GET" || r.URL.Path != "/api/v5/repos/example-owner/example-repo" {
				t.Error("unexpected metadata request")
			}
			_ = json.NewEncoder(w).Encode(raw)
		})
		result, err := client.GetRepositoryMetadata(context.Background(), RepoRequest{Owner: "example-owner", Repo: "example-repo"})
		_, privatePresent := raw["private"]
		good := fmt.Sprint(raw["id"]) == "17" && privatePresent && raw["private"] != nil && raw["default_branch"] != "" && raw["name"] == "example-repo" && raw["full_name"] == "example-owner/example-repo" && raw["owner"].(map[string]any)["login"] == "example-owner"
		if (err == nil) != good {
			t.Fatal("identity/visibility certainty changed")
		}
		if good && result.ProviderID != "17" {
			t.Fatal("missing provider identity")
		}
	}
}
func TestIssue157CompleteLabelPagination(t *testing.T) {
	for _, scenario := range []string{"complete", "short-pages", "duplicate", "duplicate-name", "foreign", "malformed", "null", "limit"} {
		t.Run(scenario, func(t *testing.T) {
			client := bootstrapHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasSuffix(r.URL.Path, "/labels") {
					fmt.Fprint(w, bootstrapMetadataJSON)
					return
				}
				page, _ := strconv.Atoi(r.URL.Query().Get("page"))
				if r.URL.Query().Get("per_page") != "100" {
					t.Error("unbounded list")
				}
				if scenario == "malformed" {
					fmt.Fprint(w, `{"labels":[]}`)
					return
				}
				if scenario == "null" {
					fmt.Fprint(w, `null`)
					return
				}
				labels := []RepositoryLabel{}
				if scenario == "short-pages" {
					if page <= 3 {
						labels = append(labels, bootstrapLabel(page, fmt.Sprintf("label-%d", page), "#ABC"))
					}
				} else if page == 1 || scenario == "limit" {
					for i := 0; i < 100; i++ {
						id := (page-1)*100 + i + 1
						labels = append(labels, bootstrapLabel(id, fmt.Sprintf("label-%d", id), "#AABBCC"))
					}
				} else if page == 2 {
					labels = append(labels, bootstrapLabel(101, "last", "#aabbcc"))
				}
				if scenario == "duplicate" && page == 2 {
					labels[0].ID = "1"
				}
				if scenario == "duplicate-name" && page == 2 {
					labels[0].Name = "label-1"
				}
				if scenario == "foreign" {
					labels[0].RepositoryID = "99"
				}
				_ = json.NewEncoder(w).Encode(labels)
			})
			result, err := client.ListRepositoryLabels(context.Background(), RepoRequest{Owner: "example-owner", Repo: "example-repo"})
			if scenario == "short-pages" {
				if err != nil || len(result.Labels) != 3 || result.Labels[0].Color != "#aabbcc" {
					t.Fatal("short provider pages incorrectly established absence")
				}
				return
			}
			if scenario != "complete" {
				if err == nil {
					t.Fatal("partial/invalid list authorized absence")
				}
				return
			}
			if err != nil || len(result.Labels) != 101 || result.Labels[0].Color != "#aabbcc" {
				t.Fatal("complete pagination/normalization failed")
			}
		})
	}
}
func TestIssue157SingleFormPostCanonicalReadback(t *testing.T) {
	for _, scenario := range []string{"confirmed", "mismatch", "ambiguous", "redirect"} {
		t.Run(scenario, func(t *testing.T) {
			var posts atomic.Int32
			client := bootstrapHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					posts.Add(1)
					if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" || r.Header.Get("Idempotency-Key") != "one-label" {
						t.Error("write headers not captured contract")
					}
					if err := r.ParseForm(); err != nil || r.PostForm.Get("name") != "state:ready" || r.PostForm.Get("color") != "#aabbcc" || len(r.PostForm) != 2 {
						t.Error("unexpected form payload")
					}
					if scenario == "ambiguous" {
						w.WriteHeader(502)
						return
					}
					if scenario == "redirect" {
						http.Redirect(w, r, "/redirect", 307)
						return
					}
					_ = json.NewEncoder(w).Encode(bootstrapLabel(7, "state:ready", "#aabbcc"))
					return
				}
				if strings.HasSuffix(r.URL.Path, "/labels") {
					if r.URL.Query().Get("page") != "1" {
						fmt.Fprint(w, `[]`)
						return
					}
					color := "#aabbcc"
					if scenario == "mismatch" {
						color = "#000000"
					}
					_ = json.NewEncoder(w).Encode([]RepositoryLabel{bootstrapLabel(7, "state:ready", color)})
					return
				}
				fmt.Fprint(w, bootstrapMetadataJSON)
			})
			result, err := client.CreateRepositoryLabel(context.Background(), RepositoryLabelRequest{Owner: "example-owner", Repo: "example-repo", Name: "state:ready", Color: "#AABBCC"}, WriteOptions{IdempotencyKey: "one-label"})
			if posts.Load() != 1 {
				t.Fatal("mutation retried")
			}
			if scenario == "confirmed" {
				if err != nil || !result.Confirmed || result.Record.Description != "Read-only description" {
					t.Fatal("canonical label not confirmed")
				}
			} else if err == nil {
				t.Fatal("false success")
			}
		})
	}
}
