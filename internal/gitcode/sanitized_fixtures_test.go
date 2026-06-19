package gitcode

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestSanitizedFixtures(t *testing.T) {
	fixtureRoot := filepath.Join("..", "..", "fixtures")
	if _, err := os.Stat(fixtureRoot); err != nil {
		if os.IsNotExist(err) {
			return
		}
		t.Fatalf("stat fixtures: %v", err)
	}
	forbidden := []string{
		"Authorization",
		"raw-owner",
		"raw-repo",
		"raw-project",
		"gitcode.example.invalid",
	}
	hostPattern := regexp.MustCompile(`(?i)\b(?:[a-z0-9-]+\.)+[a-z]{2,}\b`)
	checked := 0
	err := filepath.WalkDir(fixtureRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(fixtureRoot, path)
		if err != nil {
			return err
		}
		pathText := filepath.ToSlash(rel)
		for _, token := range forbidden {
			if strings.Contains(pathText, token) {
				t.Fatalf("fixture path contains forbidden token %q: %s", token, pathText)
			}
		}
		if entry.IsDir() {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		content := string(body)
		for _, token := range forbidden {
			if strings.Contains(content, token) {
				t.Fatalf("fixture content contains forbidden token %q: %s", token, pathText)
			}
		}
		for _, host := range hostPattern.FindAllString(content, -1) {
			if host != "api.example.com" {
				t.Fatalf("fixture content contains disallowed hostname %q: %s", host, pathText)
			}
		}
		checked++
		return nil
	})
	if err != nil {
		t.Fatalf("walk fixtures: %v", err)
	}
	t.Logf("checked %d sanitized fixture files", checked)
}
