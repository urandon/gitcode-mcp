// Package localgit reads the Git-only project format. It has no cache or provider.
package localgit

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

const SchemaVersion = 1
const maxID int64 = 9007199254740991

type Kind string

const (
	Issue   Kind = "issue"
	Comment Kind = "comment"
	Wiki    Kind = "wiki"
)

// Error exposes only fixed codes/fields, never Git stderr, paths or input bytes.
type Error struct{ Code, Field string }

func (e *Error) Error() string          { return "localgit: " + e.Code + " (" + e.Field + ")" }
func (e *Error) DiagnosticCode() string { return "local_git_" + e.Code }
func fail(code, field string) error     { return &Error{Code: code, Field: field} }

type Project struct {
	Schema       int    `yaml:"schema" json:"schema"`
	ID           string `yaml:"project_id" json:"project_id"`
	NextIssue    int64  `yaml:"next_issue" json:"next_issue"`
	NextComment  int64  `yaml:"next_comment" json:"next_comment"`
	NextDocument int64  `yaml:"next_document" json:"next_document"`
}

type Reference struct {
	Kind Kind   `yaml:"kind" json:"kind"`
	ID   string `yaml:"id" json:"id"`
}

type Metadata struct {
	Schema    int         `yaml:"schema" json:"schema"`
	ID        string      `yaml:"id" json:"id"`
	Kind      Kind        `yaml:"kind" json:"kind"`
	Title     string      `yaml:"title" json:"title"`
	State     string      `yaml:"state" json:"state"`
	ParentID  string      `yaml:"parent_id,omitempty" json:"parent_id,omitempty"`
	Labels    []string    `yaml:"labels,omitempty" json:"labels,omitempty"`
	Milestone string      `yaml:"milestone,omitempty" json:"milestone,omitempty"`
	Links     []Reference `yaml:"links,omitempty" json:"links,omitempty"`
}

type Citation struct {
	ProjectID string `json:"project_id"`
	Kind      Kind   `json:"kind,omitempty"`
	ID        string `json:"id,omitempty"`
	Commit    string `json:"commit"`
	Blob      string `json:"blob"`
	Path      string `json:"path"` // Git-tree path, not a machine filesystem path.
}

type Entity struct {
	Metadata Metadata `json:"metadata"`
	Body     string   `json:"body"`
	Citation Citation `json:"citation"`
}

// Receipt contains result locators, not payload copies or its producing commit.
type Receipt struct {
	Schema     int       `json:"schema"`
	ProjectID  string    `json:"project_id"`
	KeyHash    string    `json:"key_hash"`
	IntentHash string    `json:"intent_hash"`
	Operation  string    `json:"operation"`
	Result     Reference `json:"result"`
	ResultBlob string    `json:"result_blob"`
}

var projectID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

func idNumber(kind Kind, id string) (int64, error) {
	prefix := map[Kind]string{Issue: "ISSUE-", Comment: "COMMENT-", Wiki: "DOC-"}[kind]
	if prefix == "" || !strings.HasPrefix(id, prefix) {
		return 0, fail("invalid_argument", "entity_id")
	}
	s := strings.TrimPrefix(id, prefix)
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 1 || n > maxID || strconv.FormatInt(n, 10) != s {
		return 0, fail("invalid_argument", "entity_id")
	}
	return n, nil
}

func entityPath(kind Kind, id, parent string) (string, error) {
	if _, err := idNumber(kind, id); err != nil {
		return "", err
	}
	switch kind {
	case Issue:
		return "issues/" + id + "/issue.md", nil
	case Wiki:
		return "wiki/" + id + ".md", nil
	case Comment:
		if _, err := idNumber(Issue, parent); err != nil {
			return "", fail("invalid_argument", "parent_id")
		}
		return "issues/" + parent + "/comments/" + id + ".md", nil
	}
	return "", fail("invalid_argument", "kind")
}

func parseEntityPath(path string) (Reference, string, bool) {
	p := strings.Split(path, "/")
	var r Reference
	parent := ""
	switch {
	case len(p) == 3 && p[0] == "issues" && p[2] == "issue.md":
		r = Reference{Issue, p[1]}
	case len(p) == 4 && p[0] == "issues" && p[2] == "comments" && strings.HasSuffix(p[3], ".md"):
		r = Reference{Comment, strings.TrimSuffix(p[3], ".md")}
		parent = p[1]
	case len(p) == 2 && p[0] == "wiki" && strings.HasSuffix(p[1], ".md"):
		r = Reference{Wiki, strings.TrimSuffix(p[1], ".md")}
	default:
		return r, parent, false
	}
	want, err := entityPath(r.Kind, r.ID, parent)
	return r, parent, err == nil && want == path
}

func checkSchema(schema int) error {
	if schema != SchemaVersion {
		return fail("unsupported_schema", "schema")
	}
	return nil
}

func validateProject(p Project) error {
	if err := checkSchema(p.Schema); err != nil {
		return err
	}
	if !projectID.MatchString(p.ID) {
		return fail("invalid_schema", "project_id")
	}
	for _, n := range []int64{p.NextIssue, p.NextComment, p.NextDocument} {
		if n < 1 || n > maxID {
			return fail("invalid_schema", "counters")
		}
	}
	return nil
}

func validText(s string, max int) bool {
	return s != "" && len(s) <= max && utf8.ValidString(s) && !strings.ContainsAny(s, "\x00\r\n")
}

func validateMetadata(m Metadata) error {
	if err := checkSchema(m.Schema); err != nil {
		return err
	}
	if _, err := idNumber(m.Kind, m.ID); err != nil {
		return fail("invalid_schema", "entity_id")
	}
	if !validText(m.Title, 512) {
		return fail("invalid_schema", "title")
	}
	wantState := m.State == "active" || m.State == "deleted"
	if m.Kind == Issue {
		wantState = m.State == "open" || m.State == "closed" || m.State == "deleted"
	}
	if !wantState {
		return fail("invalid_schema", "state")
	}
	if m.Kind == Comment {
		if _, err := idNumber(Issue, m.ParentID); err != nil {
			return fail("invalid_schema", "parent_id")
		}
	} else if m.ParentID != "" {
		return fail("invalid_schema", "parent_id")
	}
	if len(m.Labels) > 32 || len(m.Links) > 64 {
		return fail("limit_exceeded", "metadata")
	}
	seen := map[string]bool{}
	for _, s := range m.Labels {
		if !validText(s, 128) || seen[s] {
			return fail("invalid_schema", "labels")
		}
		seen[s] = true
	}
	if m.Milestone != "" && !validText(m.Milestone, 128) {
		return fail("invalid_schema", "milestone")
	}
	if m.Kind != Issue && (len(m.Labels) > 0 || m.Milestone != "") {
		return fail("invalid_schema", "issue_metadata")
	}
	seen = map[string]bool{}
	for _, r := range m.Links {
		if _, err := idNumber(r.Kind, r.ID); err != nil || seen[r.ID] {
			return fail("invalid_schema", "links")
		}
		seen[r.ID] = true
	}
	return nil
}

func decodeYAML(data []byte, into any) error {
	var n yaml.Node
	if err := yaml.Unmarshal(data, &n); err != nil {
		return fail("invalid_schema", "yaml")
	}
	var bounded func(*yaml.Node, int) bool
	bounded = func(n *yaml.Node, depth int) bool {
		if depth > 8 || n.Kind == yaml.AliasNode || n.Anchor != "" {
			return false
		}
		if n.Kind == yaml.MappingNode {
			keys := map[string]bool{}
			for i := 0; i < len(n.Content); i += 2 {
				key := n.Content[i]
				if key.Kind != yaml.ScalarNode || key.Tag == "!!merge" || key.Value == "<<" || keys[key.Value] {
					return false
				}
				keys[key.Value] = true
			}
		}
		for _, v := range n.Content {
			if !bounded(v, depth+1) {
				return false
			}
		}
		return true
	}
	if !bounded(&n, 0) {
		return fail("invalid_schema", "yaml")
	}
	d := yaml.NewDecoder(bytes.NewReader(data))
	d.KnownFields(true)
	if err := d.Decode(into); err != nil {
		return fail("invalid_schema", "yaml")
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return fail("invalid_schema", "yaml")
	}
	return nil
}

func decodeEntity(data []byte, maxMetadata int) (Entity, error) {
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 || !bytes.HasPrefix(data, []byte("---\n")) {
		return Entity{}, fail("invalid_schema", "frontmatter")
	}
	i := bytes.Index(data[4:], []byte("\n---\n"))
	if i < 0 {
		return Entity{}, fail("invalid_schema", "frontmatter")
	}
	if i > maxMetadata {
		return Entity{}, fail("limit_exceeded", "metadata")
	}
	var m Metadata
	if err := decodeYAML(data[4:4+i], &m); err != nil {
		return Entity{}, err
	}
	if err := validateMetadata(m); err != nil {
		return Entity{}, err
	}
	return Entity{Metadata: m, Body: string(data[4+i+5:])}, nil
}

func hashValid(s string, size int) bool {
	if len(s) != size {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil && s == strings.ToLower(s)
}
func oidValid(s string) bool { return hashValid(s, 40) || hashValid(s, 64) }

func decodeReceipt(data []byte, project, key string) (Receipt, error) {
	var r Receipt
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil || raw == nil {
		return r, fail("invalid_schema", "receipt")
	}
	allowed := map[string]bool{"schema": true, "project_id": true, "key_hash": true, "intent_hash": true, "operation": true, "result": true, "result_blob": true}
	for key := range raw {
		if !allowed[key] {
			return r, fail("invalid_schema", "receipt")
		}
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(raw["result"], &result); err != nil || result == nil {
		return r, fail("invalid_schema", "receipt_result")
	}
	for key := range result {
		if key != "kind" && key != "id" {
			return r, fail("invalid_schema", "receipt_result")
		}
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&r); err != nil {
		return r, fail("invalid_schema", "receipt")
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return r, fail("invalid_schema", "receipt")
	}
	// YAML's strict map decoder also detects duplicate JSON object keys, which
	// encoding/json otherwise silently accepts with last-value-wins semantics.
	var unique map[string]any
	if err := decodeYAML(data, &unique); err != nil {
		return r, fail("invalid_schema", "receipt")
	}
	if err := checkSchema(r.Schema); err != nil {
		return r, err
	}
	if r.ProjectID != project || r.KeyHash != key || !hashValid(r.KeyHash, 64) || !hashValid(r.IntentHash, 64) || !oidValid(r.ResultBlob) {
		return r, fail("invalid_schema", "receipt")
	}
	if _, err := idNumber(r.Result.Kind, r.Result.ID); err != nil {
		return r, fail("invalid_schema", "receipt_result")
	}
	kinds := map[string]Kind{"create_issue": Issue, "update_issue": Issue, "delete_issue": Issue, "add_comment": Comment, "update_comment": Comment, "delete_comment": Comment, "create_wiki": Wiki, "update_wiki": Wiki, "delete_wiki": Wiki}
	if kinds[r.Operation] != r.Result.Kind {
		return r, fail("invalid_schema", "receipt_operation")
	}
	return r, nil
}

func (r Reference) String() string { return fmt.Sprintf("%s:%s", r.Kind, r.ID) }
