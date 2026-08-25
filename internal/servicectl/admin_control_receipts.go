package servicectl

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	adminControlReceiptSchema = "gitcode-mcp.admin-controls.v1"
	maxAdminControlReceipts   = 256
)

type adminControlReceiptDisk struct {
	KeyHash    string          `json:"key_hash"`
	IntentHash string          `json:"intent_hash"`
	CreatedAt  time.Time       `json:"created_at"`
	Result     json.RawMessage `json:"result"`
}

type adminControlReceiptFile struct {
	Schema   string                    `json:"schema"`
	Receipts []adminControlReceiptDisk `json:"receipts"`
}

type AdminControlReceiptManager struct {
	mu       sync.Mutex
	path     string
	receipts map[string]adminControlReceiptDisk
	now      func() time.Time
}

func NewAdminControlReceiptManager(path string) *AdminControlReceiptManager {
	return &AdminControlReceiptManager{
		path: path, receipts: map[string]adminControlReceiptDisk{},
		now: func() time.Time { return time.Now().UTC() },
	}
}

func (m *AdminControlReceiptManager) Load() error {
	if m == nil || m.path == "" {
		return nil
	}
	data, err := os.ReadFile(m.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var file adminControlReceiptFile
	if err := json.Unmarshal(data, &file); err != nil {
		return err
	}
	if file.Schema != adminControlReceiptSchema {
		return errors.New("admin controls: unsupported receipt schema")
	}
	for _, receipt := range file.Receipts {
		m.receipts[receipt.KeyHash] = receipt
	}
	m.pruneLocked()
	return nil
}

func (m *AdminControlReceiptManager) Apply(_ context.Context, action, target, idempotencyKey string, intent any, mutate func() (map[string]any, error)) (map[string]any, error) {
	if m == nil {
		return nil, controlError(http.StatusNotImplemented, "capability_unavailable", "Durable admin control receipts are unavailable.", "Use the CLI control surface.")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" || strings.TrimSpace(action) == "" || strings.TrimSpace(target) == "" {
		return nil, controlError(http.StatusBadRequest, "invalid_request", "The control target and idempotency key are required.", "Refresh the target and generate one key for this intent.")
	}
	intentJSON, err := json.Marshal(intent)
	if err != nil {
		return nil, err
	}
	keyHash := hashJobAction(idempotencyKey)
	intentHash := hashJobAction(strings.TrimSpace(action) + "\x00" + strings.TrimSpace(target) + "\x00" + string(intentJSON))
	if stored, ok := m.receipts[keyHash]; ok {
		if stored.IntentHash != intentHash {
			return nil, controlError(http.StatusConflict, "idempotency_conflict", "The idempotency key was already used for a different admin control intent.", "Generate a new key for the changed intent.")
		}
		var result map[string]any
		if err := json.Unmarshal(stored.Result, &result); err != nil {
			return nil, err
		}
		result["replayed"] = true
		return result, nil
	}
	result, err := mutate()
	if err != nil {
		return nil, err
	}
	createdAt := m.now()
	result["receipt_id"] = "receipt-" + keyHash[:16]
	result["created_at"] = createdAt
	result["replayed"] = false
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	m.receipts[keyHash] = adminControlReceiptDisk{KeyHash: keyHash, IntentHash: intentHash, CreatedAt: createdAt, Result: encoded}
	m.pruneLocked()
	if err := m.saveLocked(); err != nil {
		// The mutation has already happened. Retain the in-process receipt so a
		// browser retry cannot repeat it while durable storage is unavailable.
		return nil, err
	}
	return result, nil
}

func (m *AdminControlReceiptManager) saveLocked() error {
	if m.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(m.path), 0o700); err != nil {
		return err
	}
	file := adminControlReceiptFile{Schema: adminControlReceiptSchema}
	for _, receipt := range m.receipts {
		file.Receipts = append(file.Receipts, receipt)
	}
	sort.Slice(file.Receipts, func(i, j int) bool {
		if file.Receipts[i].CreatedAt.Equal(file.Receipts[j].CreatedAt) {
			return file.Receipts[i].KeyHash < file.Receipts[j].KeyHash
		}
		return file.Receipts[i].CreatedAt.Before(file.Receipts[j].CreatedAt)
	})
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, m.path)
}

func (m *AdminControlReceiptManager) pruneLocked() {
	if maxAdminControlReceipts <= 0 || len(m.receipts) <= maxAdminControlReceipts {
		return
	}
	ordered := make([]adminControlReceiptDisk, 0, len(m.receipts))
	for _, receipt := range m.receipts {
		ordered = append(ordered, receipt)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].CreatedAt.Equal(ordered[j].CreatedAt) {
			return ordered[i].KeyHash < ordered[j].KeyHash
		}
		return ordered[i].CreatedAt.Before(ordered[j].CreatedAt)
	})
	for _, receipt := range ordered[:len(ordered)-maxAdminControlReceipts] {
		delete(m.receipts, receipt.KeyHash)
	}
}
