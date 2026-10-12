package localauthority

import (
	"os"
	"path/filepath"
)

// A bound, unpublished store may contain only canonical fixed metadata and
// empty known directories. This inspection completes before any new entry is
// created; unknown/foreign/torn state is preserved, never promoted or repaired.
func (g knowledgeRepository) verifyUnpublished() error {
	if err := g.verifyPhysical(); err != nil {
		return err
	}
	groups := []struct {
		path    string
		allowed []string
	}{
		{"", []string{"HEAD", "config", "objects", "refs"}},
		{"objects", nil},
		{"refs", []string{"heads", "pins"}},
		{"refs/heads", nil},
		{"refs/pins", nil},
	}
	for _, group := range groups {
		path := filepath.Join(g.directory, group.path)
		if _, err := os.Lstat(path); os.IsNotExist(err) && group.path != "" {
			continue
		}
		entries, err := knowledgeNames(path, len(group.allowed))
		if err != nil {
			return Error{KnowledgeCorrupt}
		}
		allowed := map[string]bool{}
		for _, name := range group.allowed {
			allowed[name] = true
		}
		for _, entry := range entries {
			if !allowed[entry.Name()] {
				return Error{KnowledgeCorrupt}
			}
			if group.path == "" && (entry.Name() == "HEAD" || entry.Name() == "config") {
				expected := knowledgeHEAD
				if entry.Name() == "config" {
					expected = knowledgeConfig
				}
				body, err := readKnowledgeFile(filepath.Join(path, entry.Name()), len(expected))
				if err != nil || string(body) != expected {
					return Error{KnowledgeCorrupt}
				}
			} else if knowledgeDirectory(filepath.Join(path, entry.Name())) != nil {
				return Error{KnowledgeCorrupt}
			}
		}
	}
	return nil
}

// populate is a private fixed-format primitive for a durably physically bound
// original setup. It does not choose/adopt a root or reserve a caller key. The
// shared service must establish that journal proof before calling it.
func (g knowledgeRepository) populate() error {
	if err := g.verifyUnpublished(); err != nil {
		return err
	}
	if err := qualifyFilesystem(g.directory); err != nil {
		return err
	}
	for _, name := range []string{"objects", "refs", "refs/heads", "refs/pins"} {
		if err := g.verifyPhysical(); err != nil {
			return err
		}
		path := filepath.Join(g.directory, name)
		if err := os.Mkdir(path, 0700); err != nil && !os.IsExist(err) {
			return Error{DurabilityUnavailable}
		}
		if err := knowledgeDirectory(path); err != nil {
			return err
		}
		if err := qualifyFilesystem(path); err != nil {
			return err
		}
		if err := syncDirectory(filepath.Dir(path)); err != nil {
			return err
		}
		if err := g.inject("knowledge_setup_directory"); err != nil {
			return err
		}
	}
	for _, name := range []string{"config", "HEAD"} {
		if err := g.verifyPhysical(); err != nil {
			return err
		}
		body := knowledgeConfig
		if name == "HEAD" {
			body = knowledgeHEAD
		}
		path := filepath.Join(g.directory, name)
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if os.IsExist(err) {
			saved, e := readKnowledgeFile(path, len(body))
			if e != nil || string(saved) != body {
				return Error{KnowledgeCorrupt}
			}
			f, err = openExistingFile(path)
		} else if err == nil {
			_, err = f.WriteString(body)
		}
		if err != nil {
			if f != nil {
				_ = f.Close()
			}
			return Error{KnowledgeInitializationPending}
		}
		err = syncFile(f)
		_ = f.Close()
		if err != nil {
			return err
		}
		if err = syncDirectory(g.directory); err != nil {
			return err
		}
		if err = g.inject("knowledge_setup_metadata"); err != nil {
			return err
		}
	}
	if _, _, err := g.verifyLayout(); err != nil {
		return err
	}
	return g.inject("knowledge_setup_layout")
}

func (g knowledgeRepository) syncSetupMetadata() error {
	if _, _, err := g.verifyLayout(); err != nil {
		return err
	}
	for _, name := range []string{"config", "HEAD"} {
		f, err := openExistingFile(filepath.Join(g.directory, name))
		if err != nil {
			return Error{KnowledgeCorrupt}
		}
		err = syncFile(f)
		_ = f.Close()
		if err != nil {
			return err
		}
	}
	for _, name := range []string{"refs/heads", "refs/pins", "refs", "objects", ""} {
		path := filepath.Join(g.directory, name)
		if err := knowledgeDirectory(path); err != nil {
			return err
		}
		if err := qualifyFilesystem(path); err != nil {
			return err
		}
	}
	return g.verifyPhysical()
}
