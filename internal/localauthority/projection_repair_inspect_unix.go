//go:build darwin || linux

package localauthority

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"reflect"
	"sort"

	"gitcode-mcp/internal/authority"
)

type projectionRepairFile struct {
	Name, Identity, Digest string
	Bytes                  int64
}

// Metadata bodies are bounded transient inputs, never retained plan fields.
// Zero-length or undecodable final JSON may be quarantined; special/foreign,
// oversized or unknown objects cannot be treated as disposable known files.
func readProjectionRepairFile(ctx context.Context, directory *os.File, name string) (projectionRepairFile, []byte, error) {
	if ctx.Err() != nil {
		return projectionRepairFile{}, nil, ctx.Err()
	}
	f, exists, err := openProjectionFile(directory, name, false)
	if err != nil || !exists {
		return projectionRepairFile{}, nil, Error{ProjectionCorrupt}
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() < 0 || info.Size() > MaxProjectionBytes {
		return projectionRepairFile{}, nil, Error{ProjectionCorrupt}
	}
	body, err := io.ReadAll(io.LimitReader(f, MaxProjectionBytes+1))
	if err != nil || int64(len(body)) != info.Size() || len(body) > MaxProjectionBytes {
		return projectionRepairFile{}, nil, Error{ProjectionCorrupt}
	}
	identity, err := physicalIdentity(f)
	if err != nil {
		return projectionRepairFile{}, nil, err
	}
	if ctx.Err() != nil {
		return projectionRepairFile{}, nil, ctx.Err()
	}
	return projectionRepairFile{Name: name, Identity: identity, Digest: hash(body), Bytes: int64(len(body))}, body, nil
}

func projectionRepairNames(directory *os.File) ([]string, error) {
	scan, err := scanProjectionNamespace(directory)
	if err != nil {
		return nil, err
	}
	defer scan.Close()
	entries, err := scan.ReadDir(MaxProjectionStages + 2)
	if err != nil && err != io.EOF {
		return nil, Error{ProjectionCorrupt}
	}
	if len(entries) > MaxProjectionStages+1 {
		return nil, Error{Capacity}
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !projectionFileName(e.Name()) {
			return nil, Error{ProjectionCorrupt}
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names, nil
}

func inspectProjectionRepair(ctx context.Context, s *Store) (projectionRepairImage, error) {
	var zero projectionRepairImage
	if s == nil || s.lease == nil || s.lease.runtimeGuard == nil {
		return zero, Error{ReadOnly}
	}
	if ctx.Err() != nil {
		return zero, ctx.Err()
	}
	if err := s.verifyPhysicalBinding(); err != nil {
		return zero, err
	}
	root, err := openPrivateDirectory(s.directory)
	if err != nil {
		return zero, err
	}
	defer root.Close()
	rootID, err := physicalIdentity(root)
	if err != nil || rootID != s.lease.runtimeDirectoryIdentity {
		return zero, Error{ProjectionCorrupt}
	}
	directory, _, err := openProjectionNamespace(root, false)
	if err != nil {
		return zero, err
	}
	if directory == nil {
		return projectionRepairImage{State: "missing", Fingerprint: hash([]byte("missing\x00" + rootID))}, nil
	}
	defer directory.Close()
	directoryID, err := physicalIdentity(directory)
	if err != nil {
		return zero, err
	}
	names, err := projectionRepairNames(directory)
	if err != nil {
		return zero, err
	}
	files := make([]projectionRepairFile, 0, len(names))
	image := projectionRepairImage{State: "corrupt", Files: len(names)}
	stages, stageBytes := 0, int64(0)
	for _, name := range names {
		f, body, err := readProjectionRepairFile(ctx, directory, name)
		if err != nil {
			return zero, err
		}
		files = append(files, f)
		image.Bytes += f.Bytes
		if name != "catalog.json" {
			stages++
			stageBytes += f.Bytes
		} else {
			var envelope projectionEnvelope
			if authority.DecodeStoredJSON(body, &envelope, MaxProjectionBytes) == nil && envelope.Catalog.ProjectID == s.manifest.ID && envelope.Catalog.TrackerInstance == s.instance && envelope.Catalog.ManifestHash == manifestHash(s.manifest) {
				status, e := s.Status(ctx)
				if e != nil {
					return zero, e
				}
				if envelope.Catalog.Sequence <= status.AuthorityGeneration {
					image.State = "valid"
				}
			}
		}
		clear(body)
	}
	if stages > MaxProjectionStages || stageBytes > MaxProjectionStageBytes || image.Bytes > projectionRepairByteLimit {
		return zero, Error{Capacity}
	}
	// Qualify one stable bounded image, not a mixture of different path/inode
	// generations. Apply still needs an independent revalidation before effect.
	after, err := projectionRepairNames(directory)
	if err != nil {
		return zero, err
	}
	if !reflect.DeepEqual(names, after) {
		return zero, Error{ProjectionCorrupt}
	}
	for _, old := range files {
		current, body, err := readProjectionRepairFile(ctx, directory, old.Name)
		clear(body)
		if err != nil {
			return zero, err
		}
		if current != old {
			return zero, Error{ProjectionCorrupt}
		}
	}
	current, _, err := openProjectionNamespace(root, false)
	if err != nil || current == nil {
		return zero, Error{ProjectionCorrupt}
	}
	defer current.Close()
	id, err := physicalIdentity(current)
	if err != nil || id != directoryID || s.verifyPhysicalBinding() != nil {
		return zero, Error{ProjectionCorrupt}
	}
	if ctx.Err() != nil {
		return zero, ctx.Err()
	}
	guard, err := json.Marshal(struct {
		Root, Directory string
		Files           []projectionRepairFile
	}{rootID, directoryID, files})
	if err != nil {
		return zero, Error{ProjectionCorrupt}
	}
	image.Fingerprint = hash(guard)
	return image, nil
}
