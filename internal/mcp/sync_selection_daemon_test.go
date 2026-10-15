package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"gitcode-mcp/internal/servicectl"
)

func TestSyncSelectionMCPDaemonUsesAdmittedIntent(t *testing.T) {
	for _, mode := range []string{"daemon", "detach"} {
		for _, collection := range []string{"issues", "wiki", "legacy-unknown"} {
			t.Run(mode+"/"+collection, func(t *testing.T) {
				listener, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				defer listener.Close()
				t.Setenv("GITCODE_MCP_SERVICE_NETWORK", "tcp")
				t.Setenv("GITCODE_MCP_SERVICE_ADDRESS", listener.Addr().String())
				selected := []string{}
				if collection != "legacy-unknown" {
					selected = append(selected, collection)
				}
				done := make(chan error, 1)
				go func() {
					conn, err := listener.Accept()
					if err != nil {
						done <- err
						return
					}
					defer conn.Close()
					_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
					var req servicectl.RPCRequest
					if err := json.NewDecoder(conn).Decode(&req); err != nil {
						done <- err
						return
					}
					if req.Method != "Jobs.StartSync" {
						done <- io.ErrUnexpectedEOF
						return
					}
					// No asynchronous outcome rows exist yet. The daemon alone owns
					// scope resolution; a legacy daemon may omit intent metadata.
					job := servicectl.Job{ID: "job-fixture", Type: servicectl.SyncJobType, RepoID: "owner/repo", Status: servicectl.JobStatusQueued, SyncSelectedCollections: selected}
					done <- json.NewEncoder(conn).Encode(servicectl.RPCResponse{JSONRPC: "2.0", ID: req.ID, Result: job})
				}()
				var output bytes.Buffer
				// Nil service forbids re-resolving against an MCP-local cache.
				server := &Server{writer: &output, stderr: io.Discard}
				id := json.RawMessage(`1`)
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				server.callSyncLive(ctx, &id, json.RawMessage(fmt.Sprintf(`{"repo_id":"owner/repo",%q:true}`, mode)))
				select {
				case err := <-done:
					if err != nil {
						t.Fatal("fixture IPC failed")
					}
				case <-ctx.Done():
					t.Fatal("fixture IPC did not finish")
				}
				var frame struct {
					Error  any `json:"error"`
					Result struct {
						Content           []toolContentItem `json:"content"`
						StructuredContent syncLiveResult    `json:"structuredContent"`
					} `json:"result"`
				}
				if json.Unmarshal(output.Bytes(), &frame) != nil || frame.Error != nil || frame.Result.StructuredContent.Job == nil {
					t.Fatal("invalid MCP daemon result")
				}
				result := frame.Result.StructuredContent
				if !reflect.DeepEqual(result.Collections, selected) || len(frame.Result.Content) != 1 || !strings.HasSuffix(frame.Result.Content[0].Text, "collections="+strings.Join(selected, ",")) {
					t.Fatal("MCP collection metadata differs from daemon admitted intent")
				}
				if collection == "legacy-unknown" && (len(result.Diagnostics) != 1 || result.Diagnostics[0].Code != "service_sync_selection_unavailable") {
					t.Fatal("legacy daemon missing intent was silently inferred")
				}
			})
		}
	}
}
