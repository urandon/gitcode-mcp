package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCLIWriterAuthorityErrorDoesNotExposeSymlinkTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("filesystem symlink creation requires privileges on Windows")
	}
	root := t.TempDir()
	public := filepath.Join(root, "public")
	if err := os.Mkdir(public, 0o700); err != nil {
		t.Fatal("fixture directory creation failed")
	}
	// The target is outside all configured redaction roots and is not a
	// configured value. Only the cache boundary can safely hide this cause.
	targetParent := filepath.Join(root, "private-target-missing")
	alias := filepath.Join(public, "alias.db")
	if err := os.Symlink(filepath.Join(targetParent, "cache.db"), alias); err != nil {
		t.Fatal("fixture symlink creation failed")
	}
	src := &repoInitLocalSource{env: map[string]string{}, cwd: public, homeDir: public, configDir: filepath.Join(public, "config"), cacheDir: filepath.Join(public, "cache")}
	var out, stderr bytes.Buffer
	code := ExecuteWithSourceContext(context.Background(), []string{"get", "DOC-1", "--cache-path", alias, "--repo", "fixture", "--format", "json"}, &out, &stderr, src)
	if code == 0 || stderr.Len() == 0 || !json.Valid(stderr.Bytes()) {
		t.Fatal("expected a structured cached-read error")
	}
	for _, forbidden := range []string{root, targetParent, "private-target-missing"} {
		if strings.Contains(out.String(), forbidden) || strings.Contains(stderr.String(), forbidden) {
			t.Fatal("cached-read error disclosed a symlink target coordinate")
		}
	}
}
