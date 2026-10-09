package servicectl

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteStatePreservesOpenReadersAcrossReplacement(t *testing.T) {
	dir := t.TempDir()
	paths := Paths{StatePath: filepath.Join(dir, "state.json"), PIDPath: filepath.Join(dir, "service.pid")}
	old := State{PID: 123, Version: "old-version", Commit: "old-commit", SchemaMin: 18, SchemaMax: 18}
	if err := writeState(paths, old); err != nil {
		t.Fatal(err)
	}
	stateReader, err := os.Open(paths.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	defer stateReader.Close()
	pidReader, err := os.Open(paths.PIDPath)
	if err != nil {
		t.Fatal(err)
	}
	defer pidReader.Close()
	next := State{PID: 456, Version: "new", Commit: "new", SchemaMin: 21, SchemaMax: 21}
	if err := writeState(paths, next); err != nil {
		t.Fatal(err)
	}
	var observed State
	if err := json.NewDecoder(stateReader).Decode(&observed); err != nil {
		t.Fatalf("old state reader cannot decode complete identity: %v", err)
	}
	if observed != old {
		t.Fatal("publication modified the document held by an existing reader")
	}
	pid, err := io.ReadAll(pidReader)
	if err != nil || string(pid) != "123\n" {
		t.Fatal("publication modified the PID held by an existing reader")
	}
	fresh, ok, err := readState(paths.StatePath)
	if err != nil || !ok || fresh != next {
		t.Fatal("fresh state reader did not observe the complete replacement identity")
	}
	pid, err = os.ReadFile(paths.PIDPath)
	if err != nil || string(pid) != "456\n" {
		t.Fatal("fresh PID reader did not observe the complete replacement")
	}
	for _, path := range []string{paths.StatePath, paths.PIDPath} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatal("runtime identity file is not private")
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 2 {
		t.Fatal("publication left temporary files")
	}
}

func TestWriteStateFailedReplacementPreservesExistingIdentity(t *testing.T) {
	dir := t.TempDir()
	// A destination directory forces rename failure without depending on host
	// privilege or filesystem permission behavior.
	statePath := filepath.Join(dir, "state.json")
	if err := os.Mkdir(statePath, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(statePath, "existing")
	if err := os.WriteFile(sentinel, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	paths := Paths{StatePath: statePath, PIDPath: filepath.Join(dir, "service.pid")}
	if err := writeState(paths, State{PID: 123}); err == nil {
		t.Fatal("invalid state destination was accepted")
	}
	data, err := os.ReadFile(sentinel)
	if err != nil || string(data) != "unchanged" {
		t.Fatal("failed publication damaged the existing destination")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatal("failed publication left temporary files or published a PID")
	}
}

func TestReadStateStillRejectsPersistentMalformedIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(`{"pid":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := readState(path); err == nil || ok {
		t.Fatal("malformed runtime identity was silently accepted")
	}
}

func TestWriteStatePIDFailureKeepsAuthoritativeStateComplete(t *testing.T) {
	dir := t.TempDir()
	paths := Paths{StatePath: filepath.Join(dir, "state.json"), PIDPath: filepath.Join(dir, "service.pid")}
	if err := os.Mkdir(paths.PIDPath, 0o700); err != nil {
		t.Fatal(err)
	}
	state := State{PID: 123, Version: "new-version", SchemaMin: 21, SchemaMax: 21}
	if err := writeState(paths, state); err == nil {
		t.Fatal("PID publication failure was not reported")
	}
	observed, ok, err := readState(paths.StatePath)
	if err != nil || !ok || observed != state {
		t.Fatal("PID failure damaged the authoritative runtime identity")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 2 {
		t.Fatal("PID publication failure left temporary files")
	}
}
