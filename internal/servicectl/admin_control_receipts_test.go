package servicectl

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitcode-mcp/internal/adminhttp"
)

func TestAdminControlReceiptsSurviveRestartAndRejectChangedIntent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admin-controls.json")
	first := NewAdminControlReceiptManager(path)
	calls := 0
	result, err := first.Apply(context.Background(), "binding_apply", "cache-a/owner/repo", "secret-key", map[string]string{"plan_id": "plan-1"}, func() (map[string]any, error) {
		calls++
		return map[string]any{"outcome": "added"}, nil
	})
	if err != nil || result["outcome"] != "added" || result["replayed"] != false || calls != 1 {
		t.Fatalf("first result=%+v calls=%d err=%v", result, calls, err)
	}
	disk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(disk), "secret-key") {
		t.Fatalf("receipt file leaked idempotency key: %s", disk)
	}
	var payload map[string]any
	if err := json.Unmarshal(disk, &payload); err != nil {
		t.Fatal(err)
	}
	second := NewAdminControlReceiptManager(path)
	if err := second.Load(); err != nil {
		t.Fatal(err)
	}
	replayed, err := second.Apply(context.Background(), "binding_apply", "cache-a/owner/repo", "secret-key", map[string]string{"plan_id": "plan-1"}, func() (map[string]any, error) {
		calls++
		return nil, errors.New("must not run")
	})
	if err != nil || replayed["replayed"] != true || calls != 1 || replayed["receipt_id"] != result["receipt_id"] {
		t.Fatalf("replay=%+v calls=%d err=%v", replayed, calls, err)
	}
	_, err = second.Apply(context.Background(), "binding_apply", "cache-a/owner/repo", "secret-key", map[string]string{"plan_id": "plan-2"}, func() (map[string]any, error) {
		return nil, nil
	})
	var typed adminhttp.ControlError
	if !errors.As(err, &typed) || typed.Status != http.StatusConflict || typed.Code != "idempotency_conflict" {
		t.Fatalf("conflict err=%T %[1]v", err)
	}
}
