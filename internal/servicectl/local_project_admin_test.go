package servicectl

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitcode-mcp/internal/adminhttp"
	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/localgit"
	"gitcode-mcp/internal/service"
)

type localAdminBrowser struct {
	client     *http.Client
	base, csrf string
}

func openLocalAdmin(t *testing.T, ipc *RPCClient) localAdminBrowser {
	t.Helper()
	token, hash, err := adminhttp.NewLaunchToken()
	if err != nil {
		t.Fatal("launch material unavailable")
	}
	var opened adminhttp.OpenResult
	localCall(t, ipc, "Admin.Open", adminhttp.OpenRequest{LaunchTokenHash: hash}, &opened)
	if !opened.Status.Running || !strings.HasPrefix(opened.URL, "http://127.0.0.1:") {
		t.Fatal("explicit Admin did not publish a protected loopback address")
	}
	jar, _ := cookiejar.New(nil)
	b := localAdminBrowser{client: &http.Client{Jar: jar, Timeout: 12 * time.Second}, base: strings.TrimRight(opened.URL, "/")}
	status, raw := b.request(t, "POST", "/session", map[string]string{"launch_token": token}, nil)
	if status != http.StatusCreated || json.Unmarshal(raw, &struct {
		CSRF string `json:"csrf_token"`
	}{}) != nil {
		t.Fatal("local Admin launch exchange failed")
	}
	var session struct {
		CSRF string `json:"csrf_token"`
	}
	_ = json.Unmarshal(raw, &session)
	b.csrf = session.CSRF
	if b.csrf == "" {
		t.Fatal("local Admin session omitted CSRF")
	}
	return b
}

func (b localAdminBrowser) request(t *testing.T, method, route string, input any, headers map[string]string) (int, []byte) {
	t.Helper()
	var raw []byte
	if literal, ok := input.(json.RawMessage); ok {
		raw = literal
	} else if input != nil {
		var err error
		raw, err = json.Marshal(input)
		if err != nil {
			t.Fatal("fixture input encoding failed")
		}
	}
	req, err := http.NewRequest(method, b.base+"/api/admin/v1"+route, bytes.NewReader(raw))
	if err != nil {
		t.Fatal("fixture HTTP request unavailable")
	}
	if method == "POST" {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", b.base)
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		req.Header.Set("X-CSRF-Token", b.csrf)
	}
	for key, value := range headers {
		if key == "Host" {
			req.Host = value
		} else {
			req.Header.Set(key, value)
		}
	}
	resp, err := b.client.Do(req)
	if err != nil {
		t.Fatal("fixture HTTP exchange failed")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 65<<20))
	if err != nil {
		t.Fatal("fixture HTTP response unavailable")
	}
	if resp.StatusCode == http.StatusOK && resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("local observation could be cached")
	}
	return resp.StatusCode, data
}

func (b localAdminBrowser) operation(t *testing.T, operation string, input, output any) {
	t.Helper()
	method, route := "POST", "/local-projects/"+operation
	if operation == "status" {
		method, route = "GET", "/local-projects"
	}
	status, raw := b.request(t, method, route, input, nil)
	if status != http.StatusOK {
		t.Fatalf("local Admin %s HTTP status=%d", operation, status)
	}
	var envelope struct {
		Version string          `json:"api_version"`
		Result  json.RawMessage `json:"result"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.Version != "1" || json.Unmarshal(envelope.Result, output) != nil {
		t.Fatal("invalid typed local Admin response")
	}
}

func TestLocalProjectAdminRealIPCProtectedHTTPGitReplayAndIndex(t *testing.T) {
	m, path := localFixtureManager(t)
	m.AdminAutoStart = true
	m.EffectiveConfig.CachePath = filepath.Join(filepath.Dir(path), "derived.db")
	m.EffectiveConfig.RAG = config.RAGConfig{Providers: map[string]config.RAGProviderConfig{"fixture": {Type: "fake", DataBoundary: "local_process"}}, Profiles: map[string]config.RAGProfileConfig{"fixture": {Provider: "fixture", Model: "fixture-embedding", Dimensions: 8, MaxInputTokens: 512, BatchSize: 2}}}
	p := m.EffectiveConfig.LocalProjects["lab"]
	p.RAGProfile = "fixture"
	m.EffectiveConfig.LocalProjects["lab"] = p
	p.ReadOnly = true
	m.EffectiveConfig.LocalProjects["view"] = p
	ipc, stop := runLocalFixture(t, m)
	b := openLocalAdmin(t, ipc)
	var status service.LocalProjectsStatus
	b.operation(t, "status", nil, &status)
	if len(status.Projects) != 2 || !status.Projects[0].Readable || !status.Projects[1].ReadOnly {
		t.Fatal("configured local cohort unavailable")
	}
	head := status.Projects[0].Commit
	var snapshot adminhttp.ObservationSnapshot
	code, raw := b.request(t, "GET", "/snapshot", nil, nil)
	if code != 200 || json.Unmarshal(raw, &snapshot) != nil {
		t.Fatal("minimal local snapshot unavailable")
	}
	if len(snapshot.Caches) != 0 || len(snapshot.Jobs) != 0 || len(snapshot.Maintenance) != 0 {
		t.Fatal("local Admin bootstrapped remote cache or maintenance")
	}
	found := false
	for _, c := range snapshot.Capabilities {
		if c.ID == "local_project_status" && c.UIEnabled {
			found = true
		}
	}
	if !found {
		t.Fatal("local cohort was not explicitly capability gated")
	}
	create := service.LocalProjectMutationPlanRequest{LocalProjectMutation: service.LocalProjectMutation{Project: "lab", Mutation: localgit.Mutation{Operation: "create_issue", Title: "Question", Body: "AdminPrivateBodyMarker", WriteMode: "local", ExpectedHead: head, IdempotencyKey: "confirmed-create"}}}
	var plan service.LocalProjectPlan
	b.operation(t, "mutation-plan", create, &plan)
	if localFixtureGit(t, path, "", "rev-parse", "refs/heads/main") != head {
		t.Fatal("rendering a plan changed Git")
	}
	create.PlanID = plan.PlanID
	var created localgit.MutationResult
	b.operation(t, "mutation-apply", create, &created)
	if created.Entity.Metadata.ID != "ISSUE-1" || created.Replayed {
		t.Fatal("confirmed Admin write failed")
	}
	var entity localgit.Entity
	b.operation(t, "get", service.LocalProjectQuery{Project: "lab", Kind: localgit.Issue, ID: "ISSUE-1"}, &entity)
	if entity.Body != "AdminPrivateBodyMarker" || entity.Citation != created.Entity.Citation {
		t.Fatal("Admin did not read current Git bytes")
	}
	var list []localgit.Entity
	b.operation(t, "list", service.LocalProjectQuery{Project: "view", Kind: localgit.Issue, Limit: 1}, &list)
	if len(list) != 1 || list[0].Citation != entity.Citation {
		t.Fatal("bounded read-only list failed")
	}
	var search service.LocalProjectSearchResult
	b.operation(t, "search", service.LocalProjectSearchRequest{Project: "lab", Query: "AdminPrivateBodyMarker", Mode: "hybrid", Limit: 8}, &search)
	if search.EffectiveMode != "fulltext" || len(search.Hits) != 1 || search.Fallback != "index_unavailable" {
		t.Fatal("missing RAG hid lexical evidence")
	}
	index := service.LocalProjectIndexPlanRequest{LocalProjectIndexRequest: service.LocalProjectIndexRequest{Project: "lab", WriteMode: "local", MaxChunks: 1}, IdempotencyKey: "confirmed-index"}
	b.operation(t, "index-plan", index, &plan)
	index.PlanID = plan.PlanID
	var indexed service.LocalProjectIndexResult
	b.operation(t, "index-apply", index, &indexed)
	if indexed.EmbeddedChunks != 1 || indexed.State != "ready" {
		t.Fatal("confirmed bounded index failed")
	}
	update := service.LocalProjectMutation{Project: "lab", Mutation: localgit.Mutation{Operation: "update_issue", ID: "ISSUE-1", Title: "Answered", Body: "LaterAdminBodyMarker", State: "closed", WriteMode: "local", ExpectedRevision: entity.Citation.Blob, IdempotencyKey: "later-ipc"}}
	var later localgit.MutationResult
	localCall(t, ipc, "LocalProjects.Mutate", update, &later)
	var history []localgit.Citation
	b.operation(t, "history", service.LocalProjectQuery{Project: "lab", Kind: localgit.Issue, ID: "ISSUE-1", MaxCommits: 8}, &history)
	if len(history) != 2 {
		t.Fatal("Admin history omitted a revision")
	}
	b.operation(t, "search", service.LocalProjectSearchRequest{Project: "lab", Query: "LaterAdminBodyMarker", Mode: "hybrid"}, &search)
	if search.IndexState != "stale" || search.EffectiveMode != "fulltext" || len(search.Hits) != 1 {
		t.Fatal("Admin stale RAG state was hidden")
	}
	stale := service.LocalProjectMutationPlanRequest{LocalProjectMutation: update}
	stale.Mutation.Body = "RetainedDraftMarker"
	stale.Mutation.IdempotencyKey = "stale-draft"
	code, raw = b.request(t, "POST", "/local-projects/mutation-plan", stale, nil)
	if code != http.StatusConflict || !bytes.Contains(raw, []byte(`"code":"local_git_write_conflict"`)) || bytes.Contains(raw, []byte("RetainedDraftMarker")) {
		t.Fatal("stale draft was not safely rejected")
	}
	readonly := create
	readonly.Project = "view"
	code, _ = b.request(t, "POST", "/local-projects/mutation-plan", readonly, nil)
	if code != http.StatusForbidden {
		t.Fatal("read-only registration permitted a plan")
	}
	paths, _ := m.ResolvePaths()
	for _, file := range []string{paths.JobsPath, paths.RegistryPath} {
		if _, err := os.Stat(file); !os.IsNotExist(err) {
			t.Fatal("local Admin created remote maintenance state")
		}
	}
	receipt, err := os.ReadFile(paths.JobsPath + ".controls")
	if err != nil || bytes.Contains(receipt, []byte("AdminPrivateBodyMarker")) || bytes.Contains(receipt, []byte("LaterAdminBodyMarker")) || bytes.Contains(receipt, []byte(path)) {
		t.Fatal("derived index receipts copied project bytes or private paths")
	}
	stop()
	// A fresh daemon/controller proves replay is durable, not held in the UI,
	// HTTP session or process-local plan map. Original guards are now stale.
	ipc, stop = runLocalFixture(t, m)
	defer stop()
	b = openLocalAdmin(t, ipc)
	var replay localgit.MutationResult
	b.operation(t, "mutation-apply", create, &replay)
	if !replay.Replayed || replay.Entity.Citation != created.Entity.Citation || replay.Entity.Body != created.Entity.Body {
		t.Fatal("restart lost Git receipt-first replay")
	}
	var indexReplay map[string]any
	b.operation(t, "index-apply", index, &indexReplay)
	if indexReplay["replayed"] != true || indexReplay["commit"] != indexed.Commit {
		t.Fatal("derived index result replay was not durable")
	}
	if localFixtureGit(t, path, "", "rev-parse", "refs/heads/main") != later.Entity.Citation.Commit {
		t.Fatal("replay advanced project authority")
	}
}

func TestLocalProjectAdminRejectsUntrustedBrowserInputsWithoutWrites(t *testing.T) {
	m, path := localFixtureManager(t)
	m.AdminAutoStart = true
	ipc, _ := runLocalFixture(t, m)
	b := openLocalAdmin(t, ipc)
	head := localFixtureGit(t, path, "", "rev-parse", "refs/heads/main")
	anonymous := b
	anonymous.client = &http.Client{Timeout: 3 * time.Second}
	code, _ := anonymous.request(t, "GET", "/local-projects", nil, nil)
	if code != http.StatusUnauthorized {
		t.Fatal("unauthenticated local content admitted")
	}
	for _, headers := range []map[string]string{{"X-CSRF-Token": ""}, {"Origin": "https://other.invalid"}, {"Sec-Fetch-Site": "cross-site"}} {
		code, _ := b.request(t, "POST", "/local-projects/list", map[string]string{"project": "lab", "kind": "issue"}, headers)
		if code != http.StatusForbidden {
			t.Fatal("cross-origin or unprotected local request admitted")
		}
	}
	code, _ = b.request(t, "POST", "/local-projects/list", map[string]string{"project": "lab", "kind": "issue"}, map[string]string{"Host": "other.invalid"})
	if code != http.StatusBadRequest {
		t.Fatal("untrusted Host admitted")
	}
	// Give every malformed input otherwise-valid pagination. A second invalid
	// field must not mask a decoder that accidentally admits unknown/null keys.
	code, _ = b.request(t, "POST", "/local-projects/list", service.LocalProjectQuery{Project: "lab", Kind: localgit.Issue, Limit: 1}, nil)
	if code != http.StatusOK {
		t.Fatal("legitimate closed-request control failed")
	}
	for _, raw := range []string{
		`{"project":"lab","kind":"issue","limit":1,"storage_ref":"private-sentinel"}`,
		`{"project":"lab","kind":"issue","limit":1,"path":"private-sentinel"}`,
		`{"project":"lab","project":"lab","kind":"issue","limit":1}`,
		`{"Project":"lab","kind":"issue","limit":1}`,
		`{"project":"lab","kind":"issue","limit":129}`,
		`{"project":"lab","kind":"issue","limit":1} {}`,
		`{"project":"lab","kind":"issue","limit":1,"id":null}`,
	} {
		code, result := b.request(t, "POST", "/local-projects/list", json.RawMessage(raw), nil)
		if code != http.StatusBadRequest || bytes.Contains(result, []byte("private-sentinel")) || bytes.Contains(result, []byte(path)) {
			t.Fatal("closed local browser contract accepted untrusted fields or reflected input")
		}
	}
	code, _ = b.request(t, "GET", "/local-projects?path=private-sentinel", nil, nil)
	if code != http.StatusBadRequest {
		t.Fatal("URL storage selector accepted")
	}
	code, _ = b.request(t, "POST", "/local-projects/list", json.RawMessage(`{"project":"`+strings.Repeat("x", 1<<20)+`"}`), nil)
	if code != http.StatusBadRequest {
		t.Fatal("unbounded browser payload accepted")
	}
	if localFixtureGit(t, path, "", "rev-parse", "refs/heads/main") != head {
		t.Fatal("rejected browser requests published a write")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var opened adminhttp.OpenResult
	if err := ipc.Call(ctx, "Admin.Open", adminhttp.OpenRequest{}, &opened); err == nil {
		t.Fatal("cancelled IPC call succeeded")
	}
}
